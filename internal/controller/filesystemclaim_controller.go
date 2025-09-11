/*
Copyright 2025.

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

package controller

import (
	"context"
	"fmt"
	"time"

	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	fusionv1alpha1 "github.com/openshift-storage-scale/openshift-fusion-access-operator/api/v1alpha1"
)

const (
	// FileSystemClaimFinalizer is the finalizer name for cleanup operations
	FileSystemClaimFinalizer = "fusion.storage.openshift.io/filesystemclaim-finalizer"
	
	// Condition types
	ConditionTypeLocalDiskCreated   = "LocalDiskCreated"
	ConditionTypeFileSystemCreated  = "FileSystemCreated"
	ConditionTypeStorageClassCreated = "StorageClassCreated"
	ConditionTypeReady              = "Ready"
	
	// Condition reasons
	ReasonLocalDiskCreated     = "LocalDiskCreated"
	ReasonFileSystemCreated    = "FileSystemCreated"
	ReasonStorageClassCreated  = "StorageClassCreated"
	ReasonProvisioningFailed   = "ProvisioningFailed"
	ReasonValidationFailed     = "ValidationFailed"
	ReasonDeviceNotFound       = "DeviceNotFound"
	ReasonDeviceInUse          = "DeviceInUse"
	
	// IBM Spectrum Scale resource information
	LocalDiskGroup    = "scale.spectrum.ibm.com"
	LocalDiskVersion  = "v1beta1"
	LocalDiskKind     = "LocalDisk"
	
	FileSystemGroup   = "scale.spectrum.ibm.com"
	FileSystemVersion = "v1beta1"
	FileSystemKind    = "FileSystem"
)

// FileSystemClaimReconciler reconciles a FileSystemClaim object
type FileSystemClaimReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=fusion.storage.openshift.io,resources=filesystemclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=fusion.storage.openshift.io,resources=filesystemclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=fusion.storage.openshift.io,resources=filesystemclaims/finalizers,verbs=update
// +kubebuilder:rbac:groups=fusion.storage.openshift.io,resources=localvolumediscoveryresults,verbs=get;list;watch
// +kubebuilder:rbac:groups=scale.spectrum.ibm.com,resources=localdisks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scale.spectrum.ibm.com,resources=filesystems,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch;create;update;patch;delete

func (r *FileSystemClaimReconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the FileSystemClaim instance
	fsc := &fusionv1alpha1.FileSystemClaim{}
	err := r.Get(ctx, req.NamespacedName, fsc)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("FileSystemClaim resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get FileSystemClaim")
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling FileSystemClaim", "name", fsc.Name, "namespace", fsc.Namespace)

	// Handle deletion
	if fsc.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, fsc)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(fsc, FileSystemClaimFinalizer) {
		controllerutil.AddFinalizer(fsc, FileSystemClaimFinalizer)
		err := r.Update(ctx, fsc)
		if err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Validate devices exist in LocalVolumeDiscoveryResult
	err = r.validateDevices(ctx, fsc)
	if err != nil {
		logger.Error(err, "Device validation failed")
		r.updateCondition(fsc, ConditionTypeReady, metav1.ConditionFalse, ReasonValidationFailed, err.Error())
		if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
			logger.Error(statusErr, "Failed to update status after validation failure")
		}
		return ctrl.Result{RequeueAfter: time.Minute * 2}, nil
	}

	// Step 1: Create LocalDisk
	localDiskReady, err := r.ensureLocalDisk(ctx, fsc)
	if err != nil {
		logger.Error(err, "Failed to ensure LocalDisk")
		r.updateCondition(fsc, ConditionTypeLocalDiskCreated, metav1.ConditionFalse, ReasonProvisioningFailed, err.Error())
		r.updateCondition(fsc, ConditionTypeReady, metav1.ConditionFalse, ReasonProvisioningFailed, "LocalDisk creation failed")
		if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
			logger.Error(statusErr, "Failed to update status after LocalDisk failure")
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	if !localDiskReady {
		logger.Info("LocalDisk not ready yet, waiting...")
		r.updateCondition(fsc, ConditionTypeLocalDiskCreated, metav1.ConditionFalse, "Creating", "LocalDisk is being created")
		if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
			logger.Error(statusErr, "Failed to update status")
		}
		return ctrl.Result{RequeueAfter: time.Second * 30}, nil
	}

	r.updateCondition(fsc, ConditionTypeLocalDiskCreated, metav1.ConditionTrue, ReasonLocalDiskCreated, "LocalDisk created successfully")

	// Step 2: Create FileSystem
	fileSystemReady, err := r.ensureFileSystem(ctx, fsc)
	if err != nil {
		logger.Error(err, "Failed to ensure FileSystem")
		r.updateCondition(fsc, ConditionTypeFileSystemCreated, metav1.ConditionFalse, ReasonProvisioningFailed, err.Error())
		r.updateCondition(fsc, ConditionTypeReady, metav1.ConditionFalse, ReasonProvisioningFailed, "FileSystem creation failed")
		if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
			logger.Error(statusErr, "Failed to update status after FileSystem failure")
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	if !fileSystemReady {
		logger.Info("FileSystem not ready yet, waiting...")
		r.updateCondition(fsc, ConditionTypeFileSystemCreated, metav1.ConditionFalse, "Creating", "FileSystem is being created")
		if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
			logger.Error(statusErr, "Failed to update status")
		}
		return ctrl.Result{RequeueAfter: time.Second * 30}, nil
	}

	r.updateCondition(fsc, ConditionTypeFileSystemCreated, metav1.ConditionTrue, ReasonFileSystemCreated, "FileSystem created successfully")

	// Step 3: Create StorageClass
	storageClassReady, err := r.ensureStorageClass(ctx, fsc)
	if err != nil {
		logger.Error(err, "Failed to ensure StorageClass")
		r.updateCondition(fsc, ConditionTypeStorageClassCreated, metav1.ConditionFalse, ReasonProvisioningFailed, err.Error())
		r.updateCondition(fsc, ConditionTypeReady, metav1.ConditionFalse, ReasonProvisioningFailed, "StorageClass creation failed")
		if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
			logger.Error(statusErr, "Failed to update status after StorageClass failure")
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	if !storageClassReady {
		logger.Info("StorageClass not ready yet, waiting...")
		r.updateCondition(fsc, ConditionTypeStorageClassCreated, metav1.ConditionFalse, "Creating", "StorageClass is being created")
		if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
			logger.Error(statusErr, "Failed to update status")
		}
		return ctrl.Result{RequeueAfter: time.Second * 30}, nil
	}

	r.updateCondition(fsc, ConditionTypeStorageClassCreated, metav1.ConditionTrue, ReasonStorageClassCreated, "StorageClass created successfully")

	// All resources created successfully
	r.updateCondition(fsc, ConditionTypeReady, metav1.ConditionTrue, "Ready", "All resources created successfully")

	err = r.Status().Update(ctx, fsc)
	if err != nil {
		logger.Error(err, "Failed to update final status")
		return ctrl.Result{}, err
	}

	logger.Info("FileSystemClaim reconciliation completed successfully")
	return ctrl.Result{}, nil
}

// validateDevices checks if the specified devices exist in LocalVolumeDiscoveryResult
func (r *FileSystemClaimReconciler) validateDevices(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) error {
	logger := log.FromContext(ctx)

	for _, localDisk := range fsc.Spec.LocalDisks {
		// Check if device exists in LocalVolumeDiscoveryResult for the specified node
		lvdr := &fusionv1alpha1.LocalVolumeDiscoveryResult{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      localDisk.Node, // Assuming LVDR name matches node name
			Namespace: fsc.Namespace,
		}, lvdr)
		
		if err != nil {
			if errors.IsNotFound(err) {
				return fmt.Errorf("LocalVolumeDiscoveryResult not found for node %s", localDisk.Node)
			}
			return fmt.Errorf("failed to get LocalVolumeDiscoveryResult for node %s: %w", localDisk.Node, err)
		}

		// Check if the device exists in discovered devices
		deviceFound := false
		for _, discoveredDevice := range lvdr.Status.DiscoveredDevices {
			if discoveredDevice.Path == localDisk.Device || discoveredDevice.DeviceID == localDisk.Device {
				deviceFound = true
				break
			}
		}

		if !deviceFound {
			return fmt.Errorf("device %s not found in LocalVolumeDiscoveryResult for node %s", localDisk.Device, localDisk.Node)
		}

		logger.Info("Device validation successful", "device", localDisk.Device, "node", localDisk.Node)
	}

	return nil
}

// ensureLocalDisk creates LocalDisk if it doesn't exist and returns its ready status
func (r *FileSystemClaimReconciler) ensureLocalDisk(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	logger := log.FromContext(ctx)
	localDiskName := fsc.Name + "-ld"

	// Check if LocalDisk already exists
	localDisk := &unstructured.Unstructured{}
	localDisk.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   LocalDiskGroup,
		Version: LocalDiskVersion,
		Kind:    LocalDiskKind,
	})

	err := r.Get(ctx, types.NamespacedName{
		Name:      localDiskName,
		Namespace: fsc.Namespace,
	}, localDisk)

	if err != nil && errors.IsNotFound(err) {
		// Create LocalDisk
		logger.Info("Creating LocalDisk", "name", localDiskName)

		// Assuming we use the first local disk for now
		if len(fsc.Spec.LocalDisks) == 0 {
			return false, fmt.Errorf("no local disks specified in FileSystemClaim")
		}
		
		firstDisk := fsc.Spec.LocalDisks[0]
		
		localDisk.SetName(localDiskName)
		localDisk.SetNamespace(fsc.Namespace)
		
		// Set owner reference
		err = controllerutil.SetControllerReference(fsc, localDisk, r.Scheme)
		if err != nil {
			return false, fmt.Errorf("failed to set owner reference: %w", err)
		}

		// Set LocalDisk spec
		spec := map[string]interface{}{
			"device": firstDisk.Device,
			"node":   firstDisk.Node,
		}
		localDisk.Object["spec"] = spec

		err = r.Create(ctx, localDisk)
		if err != nil {
			return false, fmt.Errorf("failed to create LocalDisk: %w", err)
		}

		logger.Info("LocalDisk created successfully", "name", localDiskName)
		return false, nil // Not ready yet
	} else if err != nil {
		return false, fmt.Errorf("failed to get LocalDisk: %w", err)
	}

	// Check if LocalDisk is ready
	status, found, err := unstructured.NestedMap(localDisk.Object, "status")
	if err != nil || !found {
		return false, nil // Status not available yet
	}

	conditions, found, err := unstructured.NestedSlice(status, "conditions")
	if err != nil || !found {
		return false, nil // Conditions not available yet
	}

	for _, condition := range conditions {
		conditionMap, ok := condition.(map[string]interface{})
		if !ok {
			continue
		}
		
		condType, found, err := unstructured.NestedString(conditionMap, "type")
		if err != nil || !found {
			continue
		}
		
		if condType == "Ready" {
			condStatus, found, err := unstructured.NestedString(conditionMap, "status")
			if err != nil || !found {
				continue
			}
			return condStatus == "True", nil
		}
	}

	return false, nil // Ready condition not found
}

// ensureFileSystem creates FileSystem if it doesn't exist and returns its ready status
func (r *FileSystemClaimReconciler) ensureFileSystem(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	logger := log.FromContext(ctx)
	fileSystemName := fsc.Name + "-fs"
	localDiskName := fsc.Name + "-ld"

	// Check if FileSystem already exists
	fileSystem := &unstructured.Unstructured{}
	fileSystem.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   FileSystemGroup,
		Version: FileSystemVersion,
		Kind:    FileSystemKind,
	})

	err := r.Get(ctx, types.NamespacedName{
		Name:      fileSystemName,
		Namespace: fsc.Namespace,
	}, fileSystem)

	if err != nil && errors.IsNotFound(err) {
		// Create FileSystem
		logger.Info("Creating FileSystem", "name", fileSystemName)

		fileSystem.SetName(fileSystemName)
		fileSystem.SetNamespace(fsc.Namespace)
		
		// Set owner reference
		err = controllerutil.SetControllerReference(fsc, fileSystem, r.Scheme)
		if err != nil {
			return false, fmt.Errorf("failed to set owner reference: %w", err)
		}

		// Set FileSystem spec
		spec := map[string]interface{}{
			"localDisk": localDiskName,
		}
		fileSystem.Object["spec"] = spec

		err = r.Create(ctx, fileSystem)
		if err != nil {
			return false, fmt.Errorf("failed to create FileSystem: %w", err)
		}

		logger.Info("FileSystem created successfully", "name", fileSystemName)
		return false, nil // Not ready yet
	} else if err != nil {
		return false, fmt.Errorf("failed to get FileSystem: %w", err)
	}

	// Check if FileSystem is ready
	status, found, err := unstructured.NestedMap(fileSystem.Object, "status")
	if err != nil || !found {
		return false, nil // Status not available yet
	}

	conditions, found, err := unstructured.NestedSlice(status, "conditions")
	if err != nil || !found {
		return false, nil // Conditions not available yet
	}

	for _, condition := range conditions {
		conditionMap, ok := condition.(map[string]interface{})
		if !ok {
			continue
		}
		
		condType, found, err := unstructured.NestedString(conditionMap, "type")
		if err != nil || !found {
			continue
		}
		
		if condType == "Ready" {
			condStatus, found, err := unstructured.NestedString(conditionMap, "status")
			if err != nil || !found {
				continue
			}
			return condStatus == "True", nil
		}
	}

	return false, nil // Ready condition not found
}

// ensureStorageClass creates StorageClass if it doesn't exist and returns its ready status
func (r *FileSystemClaimReconciler) ensureStorageClass(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	logger := log.FromContext(ctx)
	storageClassName := fsc.Name + "-sc"
	fileSystemName := fsc.Name + "-fs"

	// Check if StorageClass already exists
	storageClass := &storagev1.StorageClass{}
	err := r.Get(ctx, types.NamespacedName{Name: storageClassName}, storageClass)

	if err != nil && errors.IsNotFound(err) {
		// Create StorageClass
		logger.Info("Creating StorageClass", "name", storageClassName)

		storageClass = &storagev1.StorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: storageClassName,
			},
			Provisioner: "scale.spectrum.ibm.com", // IBM Spectrum Scale provisioner
			Parameters: map[string]string{
				"fileSystem": fileSystemName,
			},
		}
		
		// Set owner reference
		err = controllerutil.SetControllerReference(fsc, storageClass, r.Scheme)
		if err != nil {
			return false, fmt.Errorf("failed to set owner reference: %w", err)
		}

		err = r.Create(ctx, storageClass)
		if err != nil {
			return false, fmt.Errorf("failed to create StorageClass: %w", err)
		}

		logger.Info("StorageClass created successfully", "name", storageClassName)
		return true, nil // StorageClass is immediately available
	} else if err != nil {
		return false, fmt.Errorf("failed to get StorageClass: %w", err)
	}

	return true, nil // StorageClass exists and is ready
}

// handleDeletion handles the deletion of FileSystemClaim and cleans up resources
func (r *FileSystemClaimReconciler) handleDeletion(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling FileSystemClaim deletion", "name", fsc.Name)

	if controllerutil.ContainsFinalizer(fsc, FileSystemClaimFinalizer) {
		// TODO: Implement cleanup logic for created resources
		// This is a placeholder for now as requested
		logger.Info("Cleanup logic placeholder - would delete LocalDisk, FileSystem, and StorageClass")

		// Remove finalizer
		controllerutil.RemoveFinalizer(fsc, FileSystemClaimFinalizer)
		err := r.Update(ctx, fsc)
		if err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// updateCondition updates the condition in the FileSystemClaim status
func (r *FileSystemClaimReconciler) updateCondition(fsc *fusionv1alpha1.FileSystemClaim, conditionType string, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: fsc.Generation,
	}

	// Find and update existing condition or append new one
	for i, existingCondition := range fsc.Status.Conditions {
		if existingCondition.Type == conditionType {
			fsc.Status.Conditions[i] = condition
			return
		}
	}
	
	fsc.Status.Conditions = append(fsc.Status.Conditions, condition)
}

// SetupWithManager sets up the controller with the Manager
func (r *FileSystemClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create predicate for FileSystemClaim events
	fscPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Only reconcile on generation changes (spec updates)
			return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration()
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}

	// Create predicate for watching created resources
	statusChangePredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Watch for status changes
			return true
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&fusionv1alpha1.FileSystemClaim{}).
		WithOptions(ctrl.Options{}).
		Watches(&storagev1.StorageClass{}, &ctrl.EnqueueRequestForOwner{
			OwnerType:    &fusionv1alpha1.FileSystemClaim{},
			IsController: true,
		}).
		WithEventFilter(fscPredicate).
		Named("filesystemclaim").
		Complete(r)
}
