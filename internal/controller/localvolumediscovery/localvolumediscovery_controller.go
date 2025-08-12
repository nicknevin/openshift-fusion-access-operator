/*
Copyright 2021 The Local Storage Operator Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package localvolumediscovery

import (
	"context"
	"fmt"
	"time"

	localv1alpha1 "github.com/openshift-storage-scale/openshift-fusion-access-operator/api/v1alpha1"
	"github.com/openshift-storage-scale/openshift-fusion-access-operator/assets"
	"github.com/openshift-storage-scale/openshift-fusion-access-operator/internal/common"
	operatorv1 "github.com/openshift/api/operator/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	v1helper "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	waitForRequeueIfDaemonsNotReady = ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}
)

const (
	DeviceFinderDiscovery = "devicefinder-discovery"
)

// LocalVolumeDiscoveryReconciler reconciles a LocalVolumeDiscovery object
type LocalVolumeDiscoveryReconciler struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	Client client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=fusion.storage.openshift.io,resources=localvolumediscoveries,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=fusion.storage.openshift.io,resources=localvolumediscoveries/status,verbs=get;list;watch;create;update;patch;delete
// This is needed for the binary running in the containers (daemonset) to sync the results
//+kubebuilder:rbac:groups=fusion.storage.openshift.io,resources=localvolumediscoveryresults,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=fusion.storage.openshift.io,resources=localvolumediscoveryresults/status,verbs=get;list;watch;create;update;patch;delete

// Reconcile reads that state of the cluster for a LocalVolumeDiscovery object and makes changes based on the state read
// and what is in the LocalVolumeDiscovery.Spec
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *LocalVolumeDiscoveryReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	klog.InfoS("Reconciling LocalVolumeDiscovery", "namespace", request.Namespace, "name", request.Name)

	// Fetch the LocalVolumeDiscovery instance
	instance := &localv1alpha1.LocalVolumeDiscovery{}
	err := r.Client.Get(ctx, request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return ctrl.Result{}, err
	}

	diskMakerDSMutateFn := getDeviceFinderDiscoveryDSMutateFn(request, instance.Spec.Tolerations,
		getEnvVars(instance.Name, string(instance.UID)),
		getOwnerRefs(instance),
		instance.Spec.NodeSelector, r.Scheme)
	ds, opResult, err := CreateOrUpdateDaemonset(ctx, r.Client, diskMakerDSMutateFn)
	if err != nil {
		message := fmt.Sprintf("failed to create discovery daemonset. Error %+v", err)
		err := r.updateDiscoveryStatus(ctx, instance, operatorv1.OperatorStatusTypeDegraded, message,
			metav1.ConditionFalse, localv1alpha1.DiscoveryFailed)
		if err != nil {
			return ctrl.Result{}, err
		}
	} else if opResult == controllerutil.OperationResultUpdated || opResult == controllerutil.OperationResultCreated {
		klog.InfoS("daemonset changed", "daemonset.Name", ds.GetName(), "op.Result", opResult)
	}

	desiredDaemons, readyDaemons, err := r.getDaemonSetStatus(ctx, instance.Namespace)
	if err != nil {
		klog.ErrorS(err, "failed to get discovery daemonset")
		return ctrl.Result{}, err
	}

	if desiredDaemons == 0 {
		message := "no discovery daemons are scheduled for running"
		err := r.updateDiscoveryStatus(ctx, instance, operatorv1.OperatorStatusTypeDegraded, message,
			metav1.ConditionFalse, localv1alpha1.DiscoveryFailed)
		if err != nil {
			return ctrl.Result{}, err
		}
		return waitForRequeueIfDaemonsNotReady, nil
	} else if desiredDaemons != readyDaemons {
		message := fmt.Sprintf("running %d out of %d discovery daemons", readyDaemons, desiredDaemons)
		err := r.updateDiscoveryStatus(ctx, instance, operatorv1.OperatorStatusTypeProgressing, message,
			metav1.ConditionFalse, localv1alpha1.Discovering)
		if err != nil {
			return ctrl.Result{}, err
		}
		klog.Info(message)
		return waitForRequeueIfDaemonsNotReady, nil
	}

	message := fmt.Sprintf("successfully running %d out of %d discovery daemons", desiredDaemons, readyDaemons)
	err = r.updateDiscoveryStatus(ctx, instance, operatorv1.OperatorStatusTypeAvailable, message,
		metav1.ConditionTrue, localv1alpha1.Discovering)
	if err != nil {
		return ctrl.Result{}, err
	}

	klog.Info("deleting orphan discovery result instances")
	err = r.deleteOrphanDiscoveryResults(ctx, instance)
	if err != nil {
		klog.ErrorS(err, "failed to delete orphan discovery results")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func getDeviceFinderDiscoveryDSMutateFn(request reconcile.Request,
	tolerations []corev1.Toleration,
	envVars []corev1.EnvVar,
	ownerRefs []metav1.OwnerReference,
	nodeSelector *corev1.NodeSelector,
	scheme *runtime.Scheme) func(*appsv1.DaemonSet) error {
	return func(ds *appsv1.DaemonSet) error {
		// read template for default values
		dsBytes, err := assets.ReadFileAndReplace(
			common.DeviceFinderDiscoveryDaemonSetTemplate,
			[]string{
				"${OBJECT_NAMESPACE}", request.Namespace,
				"${CONTAINER_IMAGE}", common.GetDeviceFinderImage(),
			},
		)
		if err != nil {
			return err
		}

		dsTemplate, err := decodeDaemonSet(dsBytes, scheme)
		if err != nil {
			return err
		}

		MutateAggregatedSpec(
			ds,
			tolerations,
			ownerRefs,
			nodeSelector,
			dsTemplate,
		)

		ds.Spec.Template.Spec.Containers[0].Env = append(ds.Spec.Template.Spec.Containers[0].Env, envVars...)

		return nil
	}
}

// updateDiscoveryStatus updates the discovery state with conditions and phase
func (r *LocalVolumeDiscoveryReconciler) updateDiscoveryStatus(ctx context.Context, instance *localv1alpha1.LocalVolumeDiscovery,
	conditionType, message string,
	status metav1.ConditionStatus,
	phase localv1alpha1.DiscoveryPhase) error {
	// avoid frequently updating the same status in the CR
	if len(instance.Status.Conditions) < 1 || instance.Status.Conditions[0].Message != message {
		condition := metav1.Condition{
			Type:               conditionType,
			Status:             status,
			Reason:             "DiscoveryUpdate",
			Message:            message,
			LastTransitionTime: metav1.Now(),
		}
		newConditions := []metav1.Condition{condition}
		instance.Status.Conditions = newConditions
		instance.Status.Phase = phase
		instance.Status.ObservedGeneration = instance.Generation
		err := r.updateStatus(ctx, instance)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *LocalVolumeDiscoveryReconciler) deleteOrphanDiscoveryResults(ctx context.Context, instance *localv1alpha1.LocalVolumeDiscovery) error {
	if instance.Spec.NodeSelector == nil || len(instance.Spec.NodeSelector.NodeSelectorTerms) == 0 {
		klog.Info("skip deleting orphan discovery results as no NodeSelectors are provided")
		return nil
	}

	discoveryResultList := &localv1alpha1.LocalVolumeDiscoveryResultList{}
	err := r.Client.List(ctx, discoveryResultList, client.InNamespace(instance.Namespace))
	if err != nil {
		return fmt.Errorf("failed to list LocalVolumeDiscoveryResult instances in namespace %q", instance.Namespace)
	}

	for idx := range discoveryResultList.Items {
		nodeName := discoveryResultList.Items[idx].Spec.NodeName
		node := &corev1.Node{}
		err = r.Client.Get(ctx, types.NamespacedName{Name: nodeName}, node)
		if err != nil {
			return fmt.Errorf("failed to get instance of node %q", nodeName)
		}

		matches, err := v1helper.MatchNodeSelectorTerms(node, instance.Spec.NodeSelector)
		if err != nil {
			return err
		}
		if !matches {
			err := r.Client.Delete(ctx, discoveryResultList.Items[idx].DeepCopy())
			if err != nil {
				return fmt.Errorf("failed to delete orphan discovery result %q in node %q", discoveryResultList.Items[idx].Name, nodeName)
			}
		}
	}

	return nil
}

func (r *LocalVolumeDiscoveryReconciler) updateStatus(ctx context.Context, lvd *localv1alpha1.LocalVolumeDiscovery) error {
	err := r.Client.Status().Update(ctx, lvd)
	if err != nil {
		return err
	}

	return nil
}

func (r *LocalVolumeDiscoveryReconciler) getDaemonSetStatus(ctx context.Context, namespace string) (desiredNumberScheduled, numberReady int32, err error) {
	existingDS := &appsv1.DaemonSet{}
	err = r.Client.Get(ctx, types.NamespacedName{Name: DeviceFinderDiscovery, Namespace: namespace}, existingDS)
	if err != nil {
		return 0, 0, err
	}

	return existingDS.Status.DesiredNumberScheduled, existingDS.Status.NumberReady, nil
}

func getOwnerRefs(cr *localv1alpha1.LocalVolumeDiscovery) []metav1.OwnerReference {
	trueVal := true
	return []metav1.OwnerReference{
		{
			APIVersion: localv1alpha1.GroupVersion.String(),
			Kind:       "LocalVolumeDiscovery",
			Name:       cr.Name,
			UID:        cr.UID,
			Controller: &trueVal,
		},
	}
}

func getEnvVars(objName, uid string) []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name:  "DISCOVERY_OBJECT_UID",
			Value: uid,
		},
		{
			Name:  "DISCOVERY_OBJECT_NAME",
			Value: objName,
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *LocalVolumeDiscoveryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&localv1alpha1.LocalVolumeDiscovery{}).
		Watches(&appsv1.DaemonSet{}, handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &localv1alpha1.LocalVolumeDiscovery{})).
		Complete(r)
}

// decodeDaemonSet decodes YAML/JSON bytes into a DaemonSet object using the provided scheme
func decodeDaemonSet(objBytes []byte, scheme *runtime.Scheme) (*appsv1.DaemonSet, error) {
	codecs := serializer.NewCodecFactory(scheme)

	requiredObj, err := runtime.Decode(codecs.UniversalDecoder(appsv1.SchemeGroupVersion), objBytes)
	if err != nil {
		return nil, err
	}
	return requiredObj.(*appsv1.DaemonSet), nil
}
