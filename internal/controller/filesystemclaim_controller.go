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

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fusionv1alpha1 "github.com/openshift-storage-scale/openshift-fusion-access-operator/api/v1alpha1"
	"github.com/openshift-storage-scale/openshift-fusion-access-operator/internal/utils"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
)

const (
	// FileSystemClaimFinalizer is the finalizer name for cleanup operations
	FileSystemClaimFinalizer = "fusion.storage.openshift.io/filesystemclaim-finalizer"

	// Reasource Creation Condition types
	ConditionTypeLocalDiskCreated    = "LocalDiskCreated"
	ReasonLocalDiskCreationFailed = "LocalDiskCreationFailed"
	ReasonLocalDiskCreationSucceeded = "LocalDiskCreationSucceeded"
	ReasonLocalDiskCreationInProgress = "LocalDiskCreationInProgress"

	ConditionTypeFileSystemCreated   = "FileSystemCreated"
	ReasonFileSystemCreationFailed = "FileSystemCreationFailed"
	ReasonFileSystemCreationSucceeded = "FileSystemCreationSucceeded"
	ReasonFileSystemCreationInProgress = "FileSystemCreationInProgress"

	ConditionTypeStorageClassCreated = "StorageClassCreated"
	ReasonStorageClassCreationFailed = "StorageClassCreationFailed"
	ReasonStorageClassCreationSucceeded = "StorageClassCreationSucceeded"
	ReasonStorageClassCreationInProgress = "StorageClassCreationInProgress"
	
	ConditionTypeNodeValidated = "NodeValidated"
	ReasonNodeValidationFailed = "NodeValidationFailed"
	ReasonNodeValidationSucceeded = "NodeValidationSucceeded"

	ConditionTypeDeviceValidated = "DeviceValidated" 
	ReasonDeviceValidationFailed = "DeviceValidationFailed"
	ReasonDeviceValidationSucceeded = "DeviceValidationSucceeded"

	// Overall status conditions
	ConditionTypeReady        = "Ready"
	ReasonProvisioningFailed  = "ProvisioningFailed"
	ReasonProvisioningSucceeded = "ProvisioningSucceeded"
	ReasonProvisioningInProgress = "ProvisioningInProgress"

	ReasonValidationFailed    = "ValidationFailed"
	ReasonDeviceNotFound      = "DeviceNotFound"
	ReasonDeviceInUse         = "DeviceInUse"

	// IBM Spectrum Scale resource information
	LocalDiskGroup   = "scale.spectrum.ibm.com"
	LocalDiskVersion = "v1beta1"
	LocalDiskKind    = "LocalDisk"

	FileSystemGroup   = "scale.spectrum.ibm.com"
	FileSystemVersion = "v1beta1"
	FileSystemKind    = "FileSystem"

	// Node validation labels
	ScaleStorageRoleLabel = "scale.spectrum.ibm.com/role"
	ScaleStorageRoleValue = "storage"
	WorkerNodeRoleLabel   = "node-role.kubernetes.io/worker"
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

	// REQUEST CATEGORIES AND ASSUMPTIONS:
	// Based on our bulletproof predicates, we can make the following assumptions:
	//
	// 1. FileSystemClaim Requests (Direct):
	//    ✅ GUARANTEED: Namespace = "ibm-spectrum-scale", Resource exists, Valid request
	//    ❓ UNKNOWN: Change type (create/update/delete), What changed (spec/status/metadata)
	//
	// 2. LocalDisk Requests (Via Watches):
	//    ✅ GUARANTEED: Namespace = "ibm-spectrum-scale", Owned by FileSystemClaim (controlling owner)
	//    ✅ GUARANTEED: Status/metadata change (NOT spec change), Generation unchanged, ResourceVersion changed
	//    ❓ UNKNOWN: Which FileSystemClaim owns it, What status changed, LocalDisk state
	//
	// 3. FileSystem Requests (Via Watches):
	//    ✅ GUARANTEED: Namespace = "ibm-spectrum-scale", Owned by FileSystemClaim (controlling owner)
	//    ✅ GUARANTEED: Status/metadata change (NOT spec change), Generation unchanged, ResourceVersion changed
	//    ❓ UNKNOWN: Which FileSystemClaim owns it, What status changed, FileSystem state
	//
	// 4. StorageClass Requests (Via Watches):
	//    ✅ GUARANTEED: Owned by FileSystemClaim (controlling owner), Status/metadata change (NOT spec change)
	//    ✅ GUARANTEED: Generation unchanged, ResourceVersion changed, Cluster-scoped (no namespace)
	//    ❓ UNKNOWN: Which FileSystemClaim owns it, What status changed, StorageClass state

	// Fetch the request
	resource := &unstructured.Unstructured{}
	err := r.Get(ctx, req.NamespacedName, resource)
	if err != nil {
		logger.Error(err, "Failed to get resource:" + req.NamespacedName.Name)
		return ctrl.Result{}, err
	}

	// Route request based on resource Kind
	kind := resource.GetKind()
	switch kind {
	case "FileSystemClaim":
		return r.handleFileSystemClaimRequest(ctx, resource)
	case "LocalDisk", "FileSystem", "StorageClass":
		return r.handleStatusUpdateRequest(ctx, req, kind)
	default:
		logger.Info("Unknown resource type", "name", req.NamespacedName.Name)
		return ctrl.Result{}, nil
	}
}

// isConditionTrue checks if a condition is true in the FileSystemClaim status
func (r *FileSystemClaimReconciler) isConditionTrue(fsc *fusionv1alpha1.FileSystemClaim, conditionType string) bool {
    for _, condition := range fsc.Status.Conditions {
        if condition.Type == conditionType && condition.Status == metav1.ConditionTrue {
            return true
        }
    }
    return false
}

// handleFileSystemClaimRequest handles direct FileSystemClaim events
func (r *FileSystemClaimReconciler) handleFileSystemClaimRequest(ctx context.Context, resource client.Object) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	
	// Convert unstructured resource to typed FileSystemClaim for direct handling
	fsc := &fusionv1alpha1.FileSystemClaim{}
	if err := r.Scheme.Convert(resource, fsc, nil); err != nil {
		logger.Error(err, "Failed to convert resource to FileSystemClaim")
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

    // Step 1: Validate nodes
    if !r.isConditionTrue(fsc, ConditionTypeNodeValidated) {
	// Validate node exists in the cluster
		err := r.validateNode(ctx, fsc)
		if err != nil {
			logger.Error(err, "Node validation failed")
			fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeNodeValidated, metav1.ConditionFalse, ReasonNodeValidationFailed, err.Error(), fsc.Generation)
		}

		fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeNodeValidated, metav1.ConditionTrue, ReasonNodeValidationSucceeded, "Node/s validation succeeded", fsc.Generation)
    }

	// Step 2: Validate devices
	if !r.isConditionTrue(fsc, ConditionTypeDeviceValidated) {
	// Validate devices exist in LocalVolumeDiscoveryResult
		err := r.validateDevices(ctx, fsc)
		if err != nil {
			logger.Error(err, "Device validation failed")
			fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeReady, metav1.ConditionFalse, ReasonValidationFailed, err.Error(), fsc.Generation)
		}

		fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeDeviceValidated, metav1.ConditionTrue, ReasonDeviceValidationSucceeded, "Device/s validation succeeded", fsc.Generation)
	}

	// Step 3: Create LocalDisk/s
	if !r.isConditionTrue(fsc, ConditionTypeLocalDiskCreated) {
		localDiskReady, err := r.ensureLocalDisk(ctx, fsc)
		if err != nil {
			logger.Error(err, "Failed to ensure LocalDisk")
			fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeLocalDiskCreated, metav1.ConditionFalse, ReasonProvisioningFailed, err.Error(), fsc.Generation)
			fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeReady, metav1.ConditionFalse, ReasonProvisioningFailed, "LocalDisk creation failed", fsc.Generation)
			if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
				logger.Error(statusErr, "Failed to update status after LocalDisk failure")
			}
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}

		if !localDiskReady {
			logger.Info("LocalDisk not ready yet, waiting...")
			// Status is already updated by copyLocalDiskStatusToFSC
			return ctrl.Result{RequeueAfter: time.Second * 30}, nil
		}
	}

    // Step 4: Create FileSystem
    if !r.isConditionTrue(fsc, ConditionTypeFileSystemCreated) {
        fileSystemReady, err := r.ensureFileSystem(ctx, fsc)
		if err != nil {
			logger.Error(err, "Failed to ensure FileSystem")
			fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeFileSystemCreated, metav1.ConditionFalse, ReasonProvisioningFailed, err.Error(), fsc.Generation)
			fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeReady, metav1.ConditionFalse, ReasonProvisioningFailed, "FileSystem creation failed", fsc.Generation)
			if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
				logger.Error(statusErr, "Failed to update status after FileSystem failure")
			}
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}

		if !fileSystemReady {
			logger.Info("FileSystem not ready yet, waiting...")
			fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeFileSystemCreated, metav1.ConditionFalse, "Creating", "FileSystem is being created", fsc.Generation)
			if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
				logger.Error(statusErr, "Failed to update status")
			}
			return ctrl.Result{RequeueAfter: time.Second * 30}, nil
		}

		fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeFileSystemCreated, metav1.ConditionTrue, ReasonFileSystemCreationSucceeded, "FileSystem created successfully", fsc.Generation)

    }
    
    // Step 5: Create StorageClass
    if !r.isConditionTrue(fsc, ConditionTypeStorageClassCreated) {
        storageClassReady, err := r.ensureStorageClass(ctx, fsc)
		if err != nil {
			logger.Error(err, "Failed to ensure StorageClass")
			fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeStorageClassCreated, metav1.ConditionFalse, ReasonProvisioningFailed, err.Error(), fsc.Generation)
			fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeReady, metav1.ConditionFalse, ReasonProvisioningFailed, "StorageClass creation failed", fsc.Generation)
			if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
				logger.Error(statusErr, "Failed to update status after StorageClass failure")
			}
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}

		if !storageClassReady {
			logger.Info("StorageClass not ready yet, waiting...")
			fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeStorageClassCreated, metav1.ConditionFalse, "Creating", "StorageClass is being created", fsc.Generation)
			if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
				logger.Error(statusErr, "Failed to update status")
			}
			return ctrl.Result{RequeueAfter: time.Second * 30}, nil
		}

		fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeStorageClassCreated, metav1.ConditionTrue, ReasonStorageClassCreationSucceeded, "StorageClass created successfully", fsc.Generation)
    }
    
    // All done - mark as Ready
    if !r.isConditionTrue(fsc, ConditionTypeReady) {
        fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, ConditionTypeReady, metav1.ConditionTrue, ReasonProvisioningSucceeded, "All resources created successfully", fsc.Generation)
        return ctrl.Result{Requeue: true}, nil
    }
    
	err := r.Status().Update(ctx, fsc)
	if err != nil {
		logger.Error(err, "Failed to update final status")
		return ctrl.Result{}, err
	}

	logger.Info("FileSystemClaim reconciliation completed successfully")
	return ctrl.Result{}, nil
}

// handleStatusUpdateRequest handles status update events from watched resources
func (r *FileSystemClaimReconciler) handleStatusUpdateRequest(ctx context.Context, req ctrl.Request, resourceType string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling status update", "type", resourceType, "name", req.NamespacedName.Name, "namespace", req.NamespacedName.Namespace)
	
	// We know this is a status update event based on our predicates:
	// - It's in ibm-spectrum-scale namespace (for LocalDisk/FileSystem)
	// - It's owned by a FileSystemClaim (controlling owner)
	// - It's a status/metadata change (NOT spec change)
	// - Generation unchanged, ResourceVersion changed
	
	// Get the resource that triggered this event
	resource, err := r.getResourceFromRequest(ctx, req, resourceType)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Resource: " + req.Name + " of type " + resourceType + " not found, may have been deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get resource: " + req.Name + " of type " + resourceType)
		return ctrl.Result{}, err
	}
	
	// Find the FileSystemClaim that owns this resource
	fsc, err := r.findFileSystemClaimOwner(ctx, resource)
	if err != nil {
		logger.Error(err, "Failed to find FileSystemClaim owner for resource: " + req.Name + " of type " + resourceType)
		return ctrl.Result{}, err
	}
	
	if fsc == nil {
		logger.Info("No FileSystemClaim owner found for resource: " + req.Name + " of type " + resourceType)
		return ctrl.Result{}, nil
	}
	
	logger.Info("Found FileSystemClaim owner", "fsc", fsc.Name, "resource", resource.GetName(), "type", resourceType)
	
	// Update FSC status based on resource status
	err = r.updateFileSystemClaimStatusFromResource(ctx, fsc, resource, resourceType)
	if err != nil {
		logger.Error(err, "Failed to update FileSystemClaim status from resource", "type", resourceType)
		return ctrl.Result{}, err
	}
	
	return ctrl.Result{}, nil
}

// getResourceFromRequest gets the resource based on the request and resource type
func (r *FileSystemClaimReconciler) getResourceFromRequest(ctx context.Context, req ctrl.Request, resourceType string) (client.Object, error) {
	switch resourceType {
	case "LocalDisk":
		resource := &unstructured.Unstructured{}
		resource.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   LocalDiskGroup,
			Version: LocalDiskVersion,
			Kind:    LocalDiskKind,
		})
		err := r.Get(ctx, req.NamespacedName, resource)
		return resource, err
		
	case "FileSystem":
		resource := &unstructured.Unstructured{}
		resource.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   FileSystemGroup,
			Version: FileSystemVersion,
			Kind:    FileSystemKind,
		})
		err := r.Get(ctx, req.NamespacedName, resource)
		return resource, err
		
	case "StorageClass":
		resource := &storagev1.StorageClass{}
		err := r.Get(ctx, req.NamespacedName, resource)
		return resource, err
		
	default:
		return nil, fmt.Errorf("unknown resource type: %s", resourceType)
	}
}

// findFileSystemClaimOwner finds the FileSystemClaim that owns the given resource
func (r *FileSystemClaimReconciler) findFileSystemClaimOwner(ctx context.Context, resource client.Object) (*fusionv1alpha1.FileSystemClaim, error) {
	ownerRefs := resource.GetOwnerReferences()
	for _, ownerRef := range ownerRefs {
		if ownerRef.Kind == "FileSystemClaim" && 
		   ownerRef.APIVersion == "fusion.storage.openshift.io/v1alpha1" &&
		   ownerRef.Controller != nil && *ownerRef.Controller {
			
			// Get the FileSystemClaim
			fsc := &fusionv1alpha1.FileSystemClaim{}
			err := r.Get(ctx, types.NamespacedName{
				Name:      ownerRef.Name,
				Namespace: resource.GetNamespace(), // Use resource namespace for LocalDisk/FileSystem, empty for StorageClass
			}, fsc)
			if err != nil {
				return nil, err
			}
			return fsc, nil
		}
	}
	return nil, nil // No owner found
}

// updateFileSystemClaimStatusFromResource updates FSC status based on the resource status
func (r *FileSystemClaimReconciler) updateFileSystemClaimStatusFromResource(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim, resource client.Object, resourceType string) error {
	logger := log.FromContext(ctx)
	
	// Get the status from the resource
	var status map[string]interface{}
	var found bool
	var err error
	
	// Handle different resource types
	switch resourceType {
	case "LocalDisk", "FileSystem":
		// For unstructured resources (LocalDisk, FileSystem)
		unstructuredResource := resource.(*unstructured.Unstructured)
		status, found, err = unstructured.NestedMap(unstructuredResource.Object, "status")
		if err != nil || !found {
			// No status yet, update with basic info
			conditionType := r.getConditionTypeForResource(resourceType)
			fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, conditionType, metav1.ConditionFalse, "Creating", fmt.Sprintf("%s is being created", resourceType), fsc.Generation)
			return r.Status().Update(ctx, fsc)
		}
		
	case "StorageClass":
		// For StorageClass (structured resource)
		storageClass := resource.(*storagev1.StorageClass)
		// StorageClass doesn't have a status field in the standard API
		// We'll treat it as ready if it exists
		conditionType := r.getConditionTypeForResource(resourceType)
		fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, conditionType, metav1.ConditionTrue, "Ready", "StorageClass is ready", fsc.Generation)
		return r.Status().Update(ctx, fsc)
		
	default:
		return fmt.Errorf("unknown resource type: %s", resourceType)
	}
	
	// Get conditions from status
	conditions, found, err := unstructured.NestedSlice(status, "conditions")
	if err != nil || !found {
		conditionType := r.getConditionTypeForResource(resourceType)
		fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, conditionType, metav1.ConditionFalse, "Creating", fmt.Sprintf("%s conditions not available yet", resourceType), fsc.Generation)
		return r.Status().Update(ctx, fsc)
	}
	
	var readyStatus, readyMessage string
	var statusMessage string
	
	// Find Ready condition
	for _, condition := range conditions {
		conditionMap, ok := condition.(map[string]interface{})
		if !ok {
			continue
		}
		
		condType, found, err := unstructured.NestedString(conditionMap, "type")
		if err != nil || !found || condType != "Ready" {
			continue
		}
		
		readyStatus, _, _ = unstructured.NestedString(conditionMap, "status")
		readyMessage, _, _ = unstructured.NestedString(conditionMap, "message")
		break
	}
	
	// Build status message
	if readyMessage != "" {
		statusMessage = fmt.Sprintf("Ready: %s (%s)", readyStatus, readyMessage)
	}
	
	// Update FSC status based on resource status
	conditionType := r.getConditionTypeForResource(resourceType)
	if readyStatus == "True" {
		fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, conditionType, metav1.ConditionTrue, "Ready", statusMessage, fsc.Generation)
	} else {
		fsc.Status.Conditions = utils.UpdateCondition(fsc.Status.Conditions, conditionType, metav1.ConditionFalse, "Creating", statusMessage, fsc.Generation)
	}
	
	logger.Info("Updated FileSystemClaim status from resource", "resourceType", resourceType, "resourceName", resource.GetName(), "readyStatus", readyStatus)
	return r.Status().Update(ctx, fsc)
}

// getConditionTypeForResource returns the appropriate condition type for the resource
func (r *FileSystemClaimReconciler) getConditionTypeForResource(resourceType string) string {
	switch resourceType {
	case "LocalDisk":
		return ConditionTypeLocalDiskCreated
	case "FileSystem":
		return ConditionTypeFileSystemCreated
	case "StorageClass":
		return ConditionTypeStorageClassCreated
	default:
		return "Unknown"
	}
}

// isStatusUpdateEvent checks if this is a status update event from watched resources
func (r *FileSystemClaimReconciler) isStatusUpdateEvent(ctx context.Context, req ctrl.Request) bool {
	// Check if the request is for a LocalDisk, FileSystem, or StorageClass
	// by looking at the resource name pattern
	if req.Name == "" {
		return false
	}
	
	// Check if this is a LocalDisk owned by a FileSystemClaim
	localDiskName := req.Name
	localDisk := &unstructured.Unstructured{}
	localDisk.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   LocalDiskGroup,
		Version: LocalDiskVersion,
		Kind:    LocalDiskKind,
	})
	
	err := r.Get(ctx, types.NamespacedName{
		Name:      localDiskName,
		Namespace: req.Namespace,
	}, localDisk)
	
	if err == nil {
		// Check if this LocalDisk is owned by a FileSystemClaim
		ownerRefs := localDisk.GetOwnerReferences()
		for _, ownerRef := range ownerRefs {
			if ownerRef.Kind == "FileSystemClaim" && ownerRef.APIVersion == "fusion.storage.openshift.io/v1alpha1" {
				return true
			}
		}
	}
	
	return false
}

// handleStatusUpdate handles status update events from watched resources
func (r *FileSystemClaimReconciler) handleStatusUpdate(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling status update", "fsc", fsc.Name)
	
	// Find the LocalDisk owned by this FileSystemClaim
	localDiskName := fsc.Name + "-ld"
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
	
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("LocalDisk not found, skipping status update")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	
	// Copy LocalDisk status to FileSystemClaim
	err = r.copyLocalDiskStatusToFSC(ctx, fsc, localDisk)
	if err != nil {
		logger.Error(err, "Failed to copy LocalDisk status to FileSystemClaim")
		return ctrl.Result{}, err
	}
	
	return ctrl.Result{}, nil
}

// ensureLocalDisk creates LocalDisk/s
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

		// Set owner reference - now both resources are in the same namespace
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

	// Copy LocalDisk status to FileSystemClaim status
	err = r.copyLocalDiskStatusToFSC(ctx, fsc, localDisk)
	if err != nil {
		return false, fmt.Errorf("failed to copy LocalDisk status: %w", err)
	}

	// Check if LocalDisk is ready
	status, found, err := unstructured.NestedMap(localDisk.Object, "status")
	if err != nil || !found {
		return false, nil // Status not available yet
	}

	// Check disk type - must be "shared" for filesystem creation
	diskType, found, err := unstructured.NestedString(status, "type")
	if err != nil || !found {
		return false, nil // Type not available yet
	}
	
	if diskType != "shared" {
		return false, fmt.Errorf("LocalDisk type is '%s', but 'shared' is required for filesystem creation", diskType)
	}

	// Check Ready condition
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
	return false, nil
}

// ensureStorageClass creates StorageClass if it doesn't exist and returns its ready status
func (r *FileSystemClaimReconciler) ensureStorageClass(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	return false, nil
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

// validateNode checks if the specified node:
// 1. is a node with the label scale.spectrum.ibm.com/role=storage
// 2. is a node with the label node-role.kubernetes.io/worker
// return a human readable error message.
func (r *FileSystemClaimReconciler) validateNode(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) error {
	logger := log.FromContext(ctx)

	// Get unique nodes from LocalDisks
	nodeMap := make(map[string]bool)
	for _, localDisk := range fsc.Spec.LocalDisks {
		nodeMap[localDisk.Node] = true
	}

	// Validate each unique node
	for nodeName := range nodeMap {
		// Using PartialObjectMetadata instead of corev1.Node to keep it light weight
		node := &metav1.PartialObjectMetadata{}
		node.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Node"))

		err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node)
		if err != nil {
			if errors.IsNotFound(err) {
				return fmt.Errorf("node %s not found in cluster", nodeName)
			}
			return fmt.Errorf("failed to get node %s error: %w", nodeName, err)
		}

		// Check for required labels
		labels := node.GetLabels()

		// Check for scale.spectrum.ibm.com/role=storage label
		if role, exists := labels[ScaleStorageRoleLabel]; !exists || role != ScaleStorageRoleValue {
			return fmt.Errorf("node %s does not have required label %s=%s", nodeName, ScaleStorageRoleLabel, ScaleStorageRoleValue)
		}

		// Check for node-role.kubernetes.io/worker label
		if _, exists := labels[WorkerNodeRoleLabel]; !exists {
			return fmt.Errorf("node %s does not have required label %s", nodeName, WorkerNodeRoleLabel)
		}

		logger.Info("Node validation of node: %s successful for fsc: %s ", nodeName, fsc.Name)
	}

	return nil
}

// validateDevices checks if the specified devices are present in ALL LocalVolumeDiscoveryResult
// which ensures both the device is valid and shared across all nodes.
// When this function is called, it is assumed that the node is valid.
// return a human readable error message.
func (r *FileSystemClaimReconciler) validateDevices(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) error {
	logger := log.FromContext(ctx)

	allNodes := &metav1.PartialObjectMetadataList{}
	allNodes.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("NodeList"))

	// List all nodes
	err := r.List(ctx, allNodes)
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	lvdrs := make(map[string]*fusionv1alpha1.LocalVolumeDiscoveryResult)
	for _, node := range allNodes.Items {
		labels := node.GetLabels()
		_, hasWorkerLabel := labels[WorkerNodeRoleLabel]
		hasStorageLabel := labels[ScaleStorageRoleLabel] == ScaleStorageRoleValue

		// Filter nodes with both worker and storage labels
		if hasWorkerLabel && hasStorageLabel {
			lvdrName := fmt.Sprintf("discovery-result-%s", node.Name)
			lvdr := &fusionv1alpha1.LocalVolumeDiscoveryResult{}

			// Get the LVDR for the node (LVDRs are in the operator's namespace)
			operatorNamespace, err := utils.GetDeploymentNamespace()
			if err != nil {
				return fmt.Errorf("failed to get operator deployment namespace: %w", err)
			}

			err = r.Get(ctx, types.NamespacedName{
				Name:      lvdrName,
				Namespace: operatorNamespace,
			}, lvdr)

			if err != nil {
				if errors.IsNotFound(err) {
					return fmt.Errorf("LocalVolumeDiscoveryResult: %s not found for node: %s", lvdrName, node.Name)
				}
				return fmt.Errorf("failed to get LocalVolumeDiscoveryResult for node %s: %w", node.Name, err)
			}

			lvdrs[node.Name] = lvdr
		}
	}

	if len(lvdrs) == 0 {
		return fmt.Errorf("no nodes found with both %s and %s=%s labels", WorkerNodeRoleLabel, ScaleStorageRoleLabel, ScaleStorageRoleValue)
	}

	// For each device, check if it exists in ALL LVDRs
	for _, localDisk := range fsc.Spec.LocalDisks {
		for nodeName, lvdr := range lvdrs {
			// Check if DiscoveredDevices exists and is not empty
			if len(lvdr.Status.DiscoveredDevices) == 0 {
				return fmt.Errorf("no discovered devices available for node %s. "+
					"Device: %s may be in use in another filesystem or is not "+
					"shared across all nodes", nodeName, localDisk.Device)
			}

			deviceFound := false
			for _, device := range lvdr.Status.DiscoveredDevices {
				if device.Path == localDisk.Device {
					deviceFound = true
					break
				}
			}

			if !deviceFound {
				return fmt.Errorf("device %s not found in LocalVolumeDiscoveryResult for node %s", localDisk.Device, nodeName)
			}
		}

		logger.Info("Device validation successful", "device", localDisk.Device, "availableOnAllNodesWithWorkerAndstorageLabel", len(lvdrs))
	}

	return nil
}

// Handles events from watched resources and enqueues reconciliation
// requests for the specific FileSystemClaim that owns the resource
func (r *FileSystemClaimReconciler) fileSystemClaimHandler(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	ownerRefs := obj.GetOwnerReferences()
	
	var requests []reconcile.Request
	for _, ownerRef := range ownerRefs {
		if ownerRef.Kind == "FileSystemClaim" && 
		   ownerRef.APIVersion == "fusion.storage.openshift.io/v1alpha1" &&
		   ownerRef.Controller != nil && *ownerRef.Controller {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      ownerRef.Name,
					Namespace: obj.GetNamespace(),
				},
			})
		}
	}
	
	return requests
}

// isInTargetNamespace checks if the resource is in the ibm-spectrum-scale namespace
func isInTargetNamespace(obj client.Object) bool {
	return obj.GetNamespace() == "ibm-spectrum-scale"
}

// isOwnedByFileSystemClaim checks if the resource is owned by a FileSystemClaim
func isOwnedByFileSystemClaim(obj client.Object) bool {
	ownerRefs := obj.GetOwnerReferences()
	for _, ownerRef := range ownerRefs {
		if ownerRef.Kind == "FileSystemClaim" && 
		   ownerRef.APIVersion == "fusion.storage.openshift.io/v1alpha1" &&
		   ownerRef.Controller != nil && *ownerRef.Controller {
			return true
		}
	}
	return false
}

// didLocalDiskStatusChange returns true if the LocalDisk status has changed
func didLocalDiskStatusChange() builder.WatchesOption {
	return builder.WithPredicates(predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return false // We don't care about create events for LocalDisk
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Check namespace and ownership first (using metadata only)
			if !isInTargetNamespace(e.ObjectNew) || !isOwnedByFileSystemClaim(e.ObjectNew) {
				return false
			}

			// Check if generation is unchanged (no spec change)
			generationUnchanged := e.ObjectOld.GetGeneration() == e.ObjectNew.GetGeneration()
			
			// Check if resourceVersion changed (something changed)
			resourceVersionChanged := e.ObjectOld.GetResourceVersion() != e.ObjectNew.GetResourceVersion()
			
			// Only trigger if generation unchanged (no spec change) but 
			// resourceVersion changed meaning status/metadata change
			return generationUnchanged && resourceVersionChanged
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false // We don't care about delete events for LocalDisk
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	})
}

// didFileSystemStatusChange returns true if the FileSystem status has changed
func didFileSystemStatusChange() builder.WatchesOption {
	return builder.WithPredicates(predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return false // We don't care about create events for FileSystem
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Check namespace and ownership first (using metadata only)
			if !isInTargetNamespace(e.ObjectNew) || !isOwnedByFileSystemClaim(e.ObjectNew) {
				return false
			}

			// Check if generation is unchanged (no spec change)
			generationUnchanged := e.ObjectOld.GetGeneration() == e.ObjectNew.GetGeneration()
			
			// Check if resourceVersion changed (something changed)
			resourceVersionChanged := e.ObjectOld.GetResourceVersion() != e.ObjectNew.GetResourceVersion()
			
			// Only trigger if generation unchanged (no spec change) but 
			// resourceVersion changed meaning status/metadata change
			return generationUnchanged && resourceVersionChanged
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false // We don't care about delete events for FileSystem
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	})
}

// didStorageClassStatusChange returns true if the StorageClass status has changed
func didStorageClassStatusChange() builder.WatchesOption {
	return builder.WithPredicates(predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return false 
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Check ownership first (StorageClass is cluster-scoped, so no namespace check needed)
			if !isOwnedByFileSystemClaim(e.ObjectNew) {
				return false
			}

			// Check if generation is unchanged (no spec change)
			generationUnchanged := e.ObjectOld.GetGeneration() == e.ObjectNew.GetGeneration()
			
			// Check if resourceVersion changed (something changed)
			resourceVersionChanged := e.ObjectOld.GetResourceVersion() != e.ObjectNew.GetResourceVersion()
			
			// Only trigger if generation unchanged (no spec change) but 
			// resourceVersion changed meaning status/metadata change
			return generationUnchanged && resourceVersionChanged
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	})
}

// SetupWithManager sets up the controller with the Manager
func (r *FileSystemClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&fusionv1alpha1.FileSystemClaim{}, builder.OnlyMetadata, builder.WithPredicates(predicate.NewPredicateFuncs(isInTargetNamespace))).
		Watches(
			&unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "scale.spectrum.ibm.com/v1beta1",
					"kind":       "LocalDisk",
				},
			},
			handler.EnqueueRequestsFromMapFunc(r.fileSystemClaimHandler),
			didLocalDiskStatusChange(),
			builder.OnlyMetadata,
		).
		Watches(
			&unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "scale.spectrum.ibm.com/v1beta1",
					"kind":       "FileSystem",
				},
			},
			handler.EnqueueRequestsFromMapFunc(r.fileSystemClaimHandler),
			didFileSystemStatusChange(),
			builder.OnlyMetadata,
		).
		Watches(
			&storagev1.StorageClass{},
			handler.EnqueueRequestsFromMapFunc(r.fileSystemClaimHandler),
			didStorageClassStatusChange(),
			builder.OnlyMetadata,
		).
		Named("filesystemclaim").
		Complete(r)
}
