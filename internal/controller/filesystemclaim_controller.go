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
	"math/rand"
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
)

const (
	// FileSystemClaimFinalizer is the finalizer name for cleanup operations
	FileSystemClaimFinalizer = "fusion.storage.openshift.io/filesystemclaim-finalizer"

	// Reasource Creation Condition types
	ConditionTypeLocalDiskCreated     = "LocalDiskCreated"
	ReasonLocalDiskCreationFailed     = "LocalDiskCreationFailed"
	ReasonLocalDiskCreationSucceeded  = "LocalDiskCreationSucceeded"
	ReasonLocalDiskCreationInProgress = "LocalDiskCreationInProgress"

	ConditionTypeFileSystemCreated     = "FileSystemCreated"
	ReasonFileSystemCreationFailed     = "FileSystemCreationFailed"
	ReasonFileSystemCreationSucceeded  = "FileSystemCreationSucceeded"
	ReasonFileSystemCreationInProgress = "FileSystemCreationInProgress"

	ConditionTypeStorageClassCreated     = "StorageClassCreated"
	ReasonStorageClassCreationFailed     = "StorageClassCreationFailed"
	ReasonStorageClassCreationSucceeded  = "StorageClassCreationSucceeded"
	ReasonStorageClassCreationInProgress = "StorageClassCreationInProgress"

	// VALIDATION CONDITION TYPES
	ConditionTypeDiskValidated    = "DiskValidated"
	ReasonDiskValidationFailed    = "DiskValidationFailed"
	ReasonDiskValidationSucceeded = "DiskValidationSucceeded"

	// OVERALL STATUS CONDITIONS
	ConditionTypeReady           = "Ready"
	ReasonProvisioningFailed     = "ProvisioningFailed"
	ReasonProvisioningSucceeded  = "ProvisioningSucceeded"
	ReasonProvisioningInProgress = "ProvisioningInProgress"

	ReasonValidationFailed = "ValidationFailed"
	ReasonDeviceNotFound   = "DeviceNotFound"
	ReasonDeviceInUse      = "DeviceInUse"

	// IBM Spectrum Scale resource information
	LocalDiskGroup   = "scale.spectrum.ibm.com"
	LocalDiskVersion = "v1beta1"
	LocalDiskKind    = "LocalDisk"
	LocalDiskList    = "LocalDiskList"

	FileSystemGroup   = "scale.spectrum.ibm.com"
	FileSystemVersion = "v1beta1"
	FileSystemKind    = "Filesystem"

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

	// Fetch the request
	fsc := &fusionv1alpha1.FileSystemClaim{}
	err := r.Get(ctx, req.NamespacedName, fsc)
	if err != nil {
		logger.Error(err, "Failed to get FileSystemClaim:"+req.NamespacedName.Name)
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

	// Add initial overall status condition
	if fsc.Status.Conditions == nil {
		// This means the FSC is new and has no conditions
		fsc.Status.Conditions = utils.UpdateCondition(
			fsc.Status.Conditions,
			ConditionTypeReady,
			metav1.ConditionFalse,
			ReasonProvisioningInProgress,
			"Provisioning in progress",
			fsc.Generation,
		)
	}

	// Validate disks
	if !r.isConditionTrue(fsc, ConditionTypeDiskValidated) {
		// Validate devices exist in LocalVolumeDiscoveryResult
		err := r.validateDisks(ctx, fsc)
		if err != nil {
			logger.Error(err, "Disk validation failed")
			// Update Ready condition
			fsc.Status.Conditions = utils.UpdateCondition(
				fsc.Status.Conditions,
				ConditionTypeReady,
				metav1.ConditionFalse,
				ReasonValidationFailed,
				err.Error(),
				fsc.Generation,
			)
			// Update DiskValidated condition
			fsc.Status.Conditions = utils.UpdateCondition(
				fsc.Status.Conditions,
				ConditionTypeDiskValidated,
				metav1.ConditionFalse,
				ReasonDiskValidationFailed,
				err.Error(),
				fsc.Generation,
			)
			if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
				logger.Error(statusErr, "Failed to update status after Disk validation failure")
				return ctrl.Result{}, statusErr
			}
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}

		// Update DiskValidated condition
		fsc.Status.Conditions = utils.UpdateCondition(
			fsc.Status.Conditions,
			ConditionTypeDiskValidated,
			metav1.ConditionTrue,
			ReasonDiskValidationSucceeded,
			"Disk/s validation succeeded",
			fsc.Generation,
		)
		if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
			logger.Error(statusErr, "Failed to update status after Device validation success")
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Create LocalDisk/s
	if !r.isConditionTrue(fsc, ConditionTypeLocalDiskCreated) {

		// Check if we're already in progress
		isInProgress := false
		for _, condition := range fsc.Status.Conditions {
			if condition.Type == ConditionTypeLocalDiskCreated &&
				condition.Status == metav1.ConditionFalse &&
				condition.Reason == ReasonLocalDiskCreationInProgress {
				isInProgress = true
				break
			}
		}

		// if not in progress, create LocalDisk/s
		if !isInProgress {
			err := r.ensureLocalDisk(ctx, fsc)
			if err != nil {
				logger.Error(err, "Failed to ensure LocalDisk")
				// Update LocalDiskCreated condition
				fsc.Status.Conditions = utils.UpdateCondition(
					fsc.Status.Conditions,
					ConditionTypeLocalDiskCreated,
					metav1.ConditionFalse,
					ReasonLocalDiskCreationFailed,
					err.Error(),
					fsc.Generation,
				)
				// Update Ready condition
				fsc.Status.Conditions = utils.UpdateCondition(
					fsc.Status.Conditions,
					ConditionTypeReady,
					metav1.ConditionFalse,
					ReasonProvisioningFailed,
					"LocalDisk creation failed",
					fsc.Generation,
				)

				if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
					logger.Error(statusErr, "Failed to update status after LocalDisk failure")
					return ctrl.Result{}, statusErr
				}
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}

			// Set InProgress condition after successful creation
			fsc.Status.Conditions = utils.UpdateCondition(
				fsc.Status.Conditions,
				ConditionTypeLocalDiskCreated,
				metav1.ConditionFalse,
				ReasonLocalDiskCreationInProgress,
				"LocalDisks created, waiting for them to become ready",
				fsc.Generation,
			)
			if statusErr := r.Status().Update(ctx, fsc); statusErr != nil {
				return ctrl.Result{}, statusErr
			}
		}

		return ctrl.Result{}, nil //  Don't requeue, wait for watch event to sync status
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
func (r *FileSystemClaimReconciler) handleStatusUpdateRequest(ctx context.Context, resource client.Object, kind string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling status update", "name", resource.GetName())

	resourceObj := &unstructured.Unstructured{}

	switch kind {
	case "LocalDisk":
		resourceObj.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   LocalDiskGroup,
			Version: LocalDiskVersion,
			Kind:    LocalDiskKind,
		})
	case "Filesystem":
		resourceObj.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   FileSystemGroup,
			Version: FileSystemVersion,
			Kind:    FileSystemKind,
		})
	}

	err := r.Get(ctx, types.NamespacedName{
		Name:      resource.GetName(),
		Namespace: resource.GetNamespace(),
	}, resourceObj)
	if err != nil {
		logger.Error(err, "Failed to get resource")
		return ctrl.Result{}, err
	}

	// Check if resource is local disk or file system
	if resourceObj.GetKind() == LocalDiskKind {
		return r.handleLocalDiskStatusUpdateRequest(ctx, resourceObj)
	} else if resourceObj.GetKind() == FileSystemKind {
		return r.handleFileSystemStatusUpdateRequest(ctx, resourceObj)
	}

	return ctrl.Result{}, nil
}

// handleLocalDiskStatusUpdateRequest handles status update events from localdisks
func (r *FileSystemClaimReconciler) handleLocalDiskStatusUpdateRequest(ctx context.Context, localDiskObj *unstructured.Unstructured) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling local disk status update", "name", localDiskObj.GetName())

	// Step 1: Find the owning FileSystemClaim
	fsc, err := r.findFileSystemClaimOwner(ctx, localDiskObj)
	if err != nil {
		logger.Error(err, "Failed to find FileSystemClaim owner")
		return ctrl.Result{}, err
	}
	if fsc == nil {
		logger.Info("No FileSystemClaim owner found for LocalDisk", "name", localDiskObj.GetName())
		return ctrl.Result{}, nil
	}

	logger.Info("Found owning FileSystemClaim", "fsc", fsc.Name, "namespace", fsc.Namespace)

	// Step 2: List all LocalDisks owned by this FSC
	localDiskList := &unstructured.UnstructuredList{}
	localDiskList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   LocalDiskGroup,
		Version: LocalDiskVersion,
		Kind:    LocalDiskList,
	})

	// List LocalDisks in the same namespace which should be ibm-spectrum-scale
	err = r.List(ctx, localDiskList, client.InNamespace(fsc.Namespace))
	if err != nil {
		logger.Error(err, "Failed to list LocalDisks")
		return ctrl.Result{}, err
	}

	// Filter LocalDisks owned by this FSC
	var ownedLocalDisks []unstructured.Unstructured
	for _, ld := range localDiskList.Items {
		if isOwnedByFileSystemClaim(&ld) {
			ownerRefs := ld.GetOwnerReferences()
			for _, ownerRef := range ownerRefs {
				if ownerRef.Kind == "FileSystemClaim" &&
					ownerRef.Name == fsc.Name &&
					ownerRef.Controller != nil && *ownerRef.Controller {
					ownedLocalDisks = append(ownedLocalDisks, ld)
					break
				}
			}
		}
	}

	if len(ownedLocalDisks) == 0 {
		logger.Info("No LocalDisks found owned by FileSystemClaim", "fsc", fsc.Name)
		return ctrl.Result{}, nil
	}

	logger.Info("Found LocalDisks owned by FSC", "count", len(ownedLocalDisks), "fsc", fsc.Name)

	// Step 3: Check each LocalDisk's status conditions
	// Track if all LocalDisks are ready
	allReady := true
	var failureMessage string
	var failedLocalDiskName string

	for _, ld := range ownedLocalDisks {
		ldName := ld.GetName()

		// Extract status.conditions from the LocalDisk (standard metav1.Condition format)
		conditions, found, err := unstructured.NestedSlice(ld.Object, "status", "conditions")
		if err != nil || !found {
			allReady = false
			failedLocalDiskName = ldName
			failureMessage = "Status conditions not found"
			logger.Info("LocalDisk has no status conditions", "name", ldName)
			break
		}

		// Helper function to find a specific condition by type
		findCondition := func(condType string) (string, string, bool) {
			for _, cond := range conditions {
				condition, ok := cond.(map[string]interface{})
				if !ok {
					continue
				}

				if ct, _, _ := unstructured.NestedString(condition, "type"); ct == condType {
					status, _, _ := unstructured.NestedString(condition, "status")
					message, _, _ := unstructured.NestedString(condition, "message")
					return status, message, true
				}
			}
			return "", "", false
		}

		// Priority 1: Check Ready condition (most critical)
		readyStatus, readyMessage, readyFound := findCondition("Ready")
		if !readyFound {
			allReady = false
			failedLocalDiskName = ldName
			failureMessage = "Ready condition not found"
			logger.Info("LocalDisk missing Ready condition", "name", ldName)
			break
		}
		if readyStatus != "True" {
			allReady = false
			failedLocalDiskName = ldName
			failureMessage = readyMessage
			logger.Info("LocalDisk is not ready", "name", ldName, "message", failureMessage)
			break
		}

		// Priority 2: Check Used condition (must be False)
		usedStatus, usedMessage, usedFound := findCondition("Used")
		if !usedFound {
			allReady = false
			failedLocalDiskName = ldName
			failureMessage = "Used condition not found"
			logger.Info("LocalDisk missing Used condition", "name", ldName)
			break
		}
		if usedStatus == "True" {
			allReady = false
			failedLocalDiskName = ldName
			failureMessage = usedMessage
			logger.Info("LocalDisk is already in use", "name", ldName, "message", failureMessage)
			break
		}

		// Priority 3: Check Available condition
		availableStatus, availableMessage, availableFound := findCondition("Available")
		if !availableFound {
			allReady = false
			failedLocalDiskName = ldName
			failureMessage = "Available condition not found"
			logger.Info("LocalDisk missing Available condition", "name", ldName)
			break
		}
		if availableStatus != "True" {
			allReady = false
			failedLocalDiskName = ldName
			failureMessage = availableMessage
			logger.Info("LocalDisk is not available", "name", ldName, "message", failureMessage)
			break
		}

		logger.Info("LocalDisk is ready and available", "name", ldName)
	}

	// Step 4: Update FSC status based on results
	if allReady {
		logger.Info("All LocalDisks are ready and available", "fsc", fsc.Name, "count", len(ownedLocalDisks))
		fsc.Status.Conditions = utils.UpdateCondition(
			fsc.Status.Conditions,
			ConditionTypeLocalDiskCreated,
			metav1.ConditionTrue,
			ReasonLocalDiskCreationSucceeded,
			fmt.Sprintf("All %d LocalDisks are ready and available", len(ownedLocalDisks)),
			fsc.Generation,
		)
	} else {
		logger.Info("LocalDisk not ready", "fsc", fsc.Name, "failedDisk", failedLocalDiskName)
		fsc.Status.Conditions = utils.UpdateCondition(
			fsc.Status.Conditions,
			ConditionTypeLocalDiskCreated,
			metav1.ConditionFalse,
			ReasonLocalDiskCreationFailed,
			fmt.Sprintf("LocalDisk %s: %s", failedLocalDiskName, failureMessage),
			fsc.Generation,
		)
	}

	// Step 5: Persist status update
	err = r.Status().Update(ctx, fsc)
	if err != nil {
		logger.Error(err, "Failed to update FileSystemClaim status")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully updated FileSystemClaim status", "fsc", fsc.Name)

	// Requeue to trigger main reconciliation loop
	return ctrl.Result{Requeue: true}, nil
}

// handleFileSystemStatusUpdateRequest handles status update events from filesystem
func (r *FileSystemClaimReconciler) handleFileSystemStatusUpdateRequest(ctx context.Context, resourceObj *unstructured.Unstructured) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling file system status update", "name", resourceObj.GetName())
	return ctrl.Result{}, nil
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

// ensureLocalDisk creates LocalDisk/s
func (r *FileSystemClaimReconciler) ensureLocalDisk(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) error {
	logger := log.FromContext(ctx)

	for index, devicePath := range fsc.Spec.Disks {
		localDiskName := fmt.Sprintf("%s-ld-%d", fsc.Name, index)

		// Create LocalDisk object
		localDiskObj := &unstructured.Unstructured{}
		localDiskObj.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   LocalDiskGroup,
			Version: LocalDiskVersion,
			Kind:    LocalDiskKind,
		})

		// Check if LocalDisk already exists
		err := r.Get(ctx, types.NamespacedName{
			Name:      localDiskName,
			Namespace: fsc.Namespace,
		}, localDiskObj)

		if err != nil && errors.IsNotFound(err) {
			// Get a random storage node for this disk
			nodeName, err := r.getRandomStorageNode(ctx)
			if err != nil {
				return fmt.Errorf("failed to get random sharedstorage node for disk %s: %w", devicePath, err)
			}

			// Create LocalDisk
			logger.Info("Creating LocalDisk", "name", localDiskName, "device", devicePath, "node", nodeName)

			localDiskObj.SetName(localDiskName)
			localDiskObj.SetNamespace(fsc.Namespace)

			// Set owner reference to LocalDisk
			err = controllerutil.SetOwnerReference(fsc, localDiskObj, r.Scheme)
			if err != nil {
				return fmt.Errorf("failed to set owner reference: %w", err)
			}

			// Set LocalDisk spec
			spec := map[string]interface{}{
				"device": devicePath,
				"node":   nodeName,
			}
			localDiskObj.Object["spec"] = spec

			err = r.Create(ctx, localDiskObj)
			if err != nil {
				return fmt.Errorf("failed to create LocalDisk: %w", err)
			}

			logger.Info("LocalDisk created successfully", "name", localDiskName)

		} else if err != nil {
			return fmt.Errorf("failed to get LocalDisk: %w", err)
		} else {
			// LocalDisk already exists
			logger.Info("LocalDisk already exists", "name", localDiskName)
		}
	}

	return nil
}

// ensureFileSystem creates FileSystem if it doesn't exist and returns its ready status
func (r *FileSystemClaimReconciler) ensureFileSystem(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	return false, nil
}

// ensureStorageClass creates StorageClass if it doesn't exist and returns its ready status
func (r *FileSystemClaimReconciler) ensureStorageClass(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	return false, nil
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

// getRandomStorageNode returns a random node name that has both
// WorkerNodeRoleLabel and ScaleStorageRoleLabel=ScaleStorageRoleValue labels
func (r *FileSystemClaimReconciler) getRandomStorageNode(ctx context.Context) (string, error) {
	logger := log.FromContext(ctx)

	allNodes := &metav1.PartialObjectMetadataList{}
	allNodes.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("NodeList"))

	// List all nodes
	err := r.List(ctx, allNodes)
	if err != nil {
		return "", fmt.Errorf("failed to list nodes: %w", err)
	}

	// Filter nodes with both worker and storage labels
	var storageNodes []string
	for _, node := range allNodes.Items {
		labels := node.GetLabels()
		_, hasWorkerLabel := labels[WorkerNodeRoleLabel]
		hasStorageLabel := labels[ScaleStorageRoleLabel] == ScaleStorageRoleValue

		if hasWorkerLabel && hasStorageLabel {
			storageNodes = append(storageNodes, node.Name)
		}
	}

	if len(storageNodes) == 0 {
		return "", fmt.Errorf("no nodes found with both %s and %s=%s labels",
			WorkerNodeRoleLabel, ScaleStorageRoleLabel, ScaleStorageRoleValue)
	}

	// Return random node
	randomIndex := rand.Intn(len(storageNodes))
	selectedNode := storageNodes[randomIndex]

	logger.Info("Selected random storage node", "node", selectedNode, "totalStorageNodes", len(storageNodes))
	return selectedNode, nil
}

// TODO handleDeletion handles the deletion of FileSystemClaim and cleans up resources
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

// validateDisks checks if the specified disks are present in ALL LocalVolumeDiscoveryResult
// which ensures both the device is valid and shared across all nodes.
// When this function is called.
// return a human readable error message.
func (r *FileSystemClaimReconciler) validateDisks(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) error {
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
	for _, disk := range fsc.Spec.Disks {
		for nodeName, lvdr := range lvdrs {
			// Check if DiscoveredDevices exists and is not empty
			if len(lvdr.Status.DiscoveredDevices) == 0 {
				return fmt.Errorf("no discovered disks available for node %s. "+
					"Disk: %s may be in use in another filesystem or is not "+
					"shared across all nodes", nodeName, disk)
			}

			diskFound := false
			for _, discoveredDisk := range lvdr.Status.DiscoveredDevices {
				if discoveredDisk.Path == disk {
					diskFound = true
					break
				}
			}

			if !diskFound {
				return fmt.Errorf("device %s not found in LocalVolumeDiscoveryResult for node %s", disk, nodeName)
			}
		}

		logger.Info("Device validation successful", "disk", disk, "availableOnAllNodesWithWorkerAndstorageLabel", len(lvdrs))
	}

	return nil
}

// BELOW THIS POINT ARE THE HANDLERS FOR WATCHED RESOURCES

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

// SetupWithManager sets up the controller with the Manager
func (r *FileSystemClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&fusionv1alpha1.FileSystemClaim{}, builder.OnlyMetadata, builder.WithPredicates(predicate.NewPredicateFuncs(isInTargetNamespace))).
		Watches(
			&unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": LocalDiskGroup + "/" + LocalDiskVersion,
					"kind":       LocalDiskKind,
				},
			},
			handler.EnqueueRequestsFromMapFunc(r.fileSystemClaimHandler),
			didLocalDiskStatusChange(),
			builder.OnlyMetadata,
		).
		Watches(
			&unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": FileSystemGroup + "/" + FileSystemVersion,
					"kind":       FileSystemKind,
				},
			},
			handler.EnqueueRequestsFromMapFunc(r.fileSystemClaimHandler),
			didFileSystemStatusChange(),
			builder.OnlyMetadata,
		).
		Named("filesystemclaim").
		Complete(r)
}
