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
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
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

	maxKubernetesNameLength = 253

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
	ConditionTypeDeviceValidated    = "DeviceValidated"
	ReasonDeviceValidationFailed    = "DeviceValidationFailed"
	ReasonDeviceValidationSucceeded = "DeviceValidationSucceeded"

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
	FileSystemList    = "FilesystemList"

	// Node validation labels
	ScaleStorageRoleLabel = "scale.spectrum.ibm.com/role"
	ScaleStorageRoleValue = "storage"
	WorkerNodeRoleLabel   = "node-role.kubernetes.io/worker"
)

// FileSystemClaimReconciler reconciles a FileSystemClaim object
type FileSystemClaimReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Configurable requeue delays for testing and optimization
	RequeueDelay time.Duration
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

	// Fetch the request
	fsc := &fusionv1alpha1.FileSystemClaim{}

	if err := r.Get(ctx, req.NamespacedName, fsc); errors.IsNotFound(err) {
		logger.Info("FileSystemClaim not found, maybe deleted", "name", req.Name)
		return ctrl.Result{}, nil
	} else if err != nil {
		logger.Error(err, "Failed to get FileSystemClaim", "name", req.Name)
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling FileSystemClaim", "name", fsc.Name, "namespace", fsc.Namespace)

	// Finalizers first
	if changed, err := r.handleFinalizers(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		// We wrote something (finalizer add/remove). Requeue to read fresh.
		return ctrl.Result{RequeueAfter: r.RequeueDelay}, nil
	}

	// Handle deletion
	if changed, err := r.handleDeletion(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{RequeueAfter: r.RequeueDelay}, nil
	}

	// 1) Ensure LocalDisks exist (create/update if needed)
	if changed, err := r.ensureLocalDisks(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		// We created/updated children; let cache/watches settle.
		return ctrl.Result{RequeueAfter: r.RequeueDelay}, nil
	}

	// 2) Sync FSC conditions from owned LocalDisks
	if changed, err := r.syncLocalDiskConditions(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		// We updated FSC status; that's a write -> requeue once.
		return ctrl.Result{RequeueAfter: r.RequeueDelay}, nil
	}

	// 3) Ensure Filesystems (only if LD preconditions are satisfied)
	if changed, err := r.ensureFileSystem(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{RequeueAfter: r.RequeueDelay}, nil
	}

	// 4) Sync FSC conditions from owned Filesystems
	if changed, err := r.syncFilesystemConditions(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{RequeueAfter: r.RequeueDelay}, nil
	}

	// 5) Ensure StorageClass (only after Filesystem ready)
	if changed, err := r.ensureStorageClass(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{RequeueAfter: r.RequeueDelay}, nil
	}

	// 6) Aggregate/Ready
	if changed, err := r.syncFSCReady(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{RequeueAfter: r.RequeueDelay}, nil
	}

	logger.Info("FileSystemClaim reconciliation completed successfully")
	return ctrl.Result{}, nil
}

// Handlers for FileSystemClaim reconciliation -- START

// handleFinalizers handles the finalizers for the FileSystemClaim
func (r *FileSystemClaimReconciler) handleFinalizers(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	logger := log.FromContext(ctx)

	// If the FileSystemClaim is being deleted, no need to add/remove finalizers
	if fsc.DeletionTimestamp != nil {
		return false, nil
	}

	if !controllerutil.ContainsFinalizer(fsc, FileSystemClaimFinalizer) {
		if err := r.patchFSCSpec(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
			controllerutil.AddFinalizer(cur, FileSystemClaimFinalizer)
		}); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return false, err
		}
		logger.Info("Added finalizer", "name", fsc.Name)
		return true, nil
	}
	return false, nil
}

// TODO handleDeletion handles the deletion of FileSystemClaim and cleans up resources
func (r *FileSystemClaimReconciler) handleDeletion(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	logger := log.FromContext(ctx)

	if fsc.DeletionTimestamp == nil {
		return false, nil
	}

	if controllerutil.ContainsFinalizer(fsc, FileSystemClaimFinalizer) {
		logger.Info("Cleanup logic placeholder - would delete LocalDisk, FileSystem, and StorageClass")

		if err := r.patchFSCSpec(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
			controllerutil.RemoveFinalizer(cur, FileSystemClaimFinalizer)
		}); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return false, err
		}
		logger.Info("Removed finalizer", "name", fsc.Name)
		return true, nil
	}
	return false, nil
}

// ensureLocalDisk creates LocalDisk/s
func (r *FileSystemClaimReconciler) ensureLocalDisks(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	logger := log.FromContext(ctx)

	// If LocalDisks are already created, no need to create them again
	if r.isConditionTrue(fsc, ConditionTypeLocalDiskCreated) {
		return false, nil
	}

	// If localDisk creation is in progress, no need to create LocalDisks again
	if r.hasConditionWithReason(fsc.Status.Conditions, ConditionTypeLocalDiskCreated, ReasonLocalDiskCreationInProgress) {
		return false, nil
	}

	// Phase 1: validate once
	if !r.isConditionTrue(fsc, ConditionTypeDeviceValidated) {
		if err := r.validateDevices(ctx, fsc); err != nil {
			logger.Error(err, "Device validation failed")
			if e := r.handleValidationError(ctx, fsc, err); e != nil {
				logger.Error(e, "Failed to update status after disk validation failure")
				return false, e
			}
			return true, nil
		}

		if _, e := r.updateConditionIfChanged(ctx, fsc, ConditionTypeDeviceValidated, metav1.ConditionTrue, ReasonDeviceValidationSucceeded, "Device/s validation succeeded"); e != nil {
			logger.Error(e, "Failed to update status after device validation success")
			return false, e
		}
		return true, nil
	}

	// Get node name first - the same node will be used for all LocalDisks
	nodeName, selErr := r.getRandomStorageNode(ctx)
	if selErr != nil {
		logger.Error(selErr, "failed to pick a storage node")
		if e := r.handleResourceCreationError(ctx, fsc, "LocalDisk", selErr); e != nil {
			return false, e
		}
		return true, nil
	}

	// variable to track if we need to requeue the reconciliation
	var requeue bool

	// Phase 2: ensure LocalDisks
	for _, devicePath := range fsc.Spec.Devices {
		// Get WWN for the device
		// this will fail if the device is not found in any of the LocalVolumeDiscoveryResult
		wwn, err := r.getDeviceWWN(ctx, devicePath, nodeName)
		if err != nil {
			logger.Error(err, "failed to get WWN for device", "device", devicePath, "node", nodeName)
			if e := r.handleResourceCreationError(ctx, fsc, "LocalDisk", err); e != nil {
				return false, e
			}
			return true, nil
		}

		// Generate LocalDisk name using device name + WWN
		localDiskName, err := generateLocalDiskName(devicePath, wwn)
		if err != nil {
			logger.Error(err, "failed to generate LocalDisk name", "device", devicePath, "wwn", wwn)
			if e := r.handleResourceCreationError(ctx, fsc, "LocalDisk", err); e != nil {
				return false, e
			}
			return true, nil
		}

		ld := &unstructured.Unstructured{}
		ld.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   LocalDiskGroup,
			Version: LocalDiskVersion,
			Kind:    LocalDiskKind,
		})
		ld.SetName(localDiskName) // Set the name before using it

		err = r.Get(ctx, types.NamespacedName{
			Namespace: fsc.Namespace,
			Name:      localDiskName,
		}, ld)

		switch {
		case errors.IsNotFound(err):
			// Create LocalDisk with new naming
			spec := map[string]interface{}{"device": devicePath, "node": nodeName}
			if err := r.createResourceWithOwnership(ctx, fsc, ld, spec); err != nil {
				logger.Error(err, "failed to create LocalDisk", "name", localDiskName)
				if e := r.handleResourceCreationError(ctx, fsc, "LocalDisk", err); e != nil {
					return false, e
				}
				return true, nil
			}

			logger.Info("Creating LocalDisk", "name", localDiskName, "device", devicePath, "node", nodeName)
			if _, e := r.updateConditionIfChanged(ctx, fsc, ConditionTypeLocalDiskCreated, metav1.ConditionFalse, ReasonLocalDiskCreationInProgress, "LocalDisks created, waiting for them to become ready"); e != nil {
				return false, e
			}

			requeue = true
			continue

		case err != nil:
			return false, fmt.Errorf("failed to get LocalDisk %s: %w", localDiskName, err)

		default:
			// Check for drift and patch if needed
			// There is a admission webhook that prevents the update of spec.device, spec.node and spec.thinDiskType after the LocalDisk is created.
			// example error:
			// ... cannot be edited because a related NSD is already created in Storage Scale
			logger.Info("localDisk already exists, skipping drift detection and patching", "name", localDiskName)
			return false, nil
		}
	}

	if requeue {
		return true, nil
	}

	return false, nil
}

// syncLocalDiskConditions inspects all LocalDisks owned by this FSC and updates
// ConditionTypeLocalDiskCreated with a precise reason/message. Returns changed=true if we wrote status.
func (r *FileSystemClaimReconciler) syncLocalDiskConditions(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	logger := log.FromContext(ctx)

	// 1) List owned LocalDisks
	owned, err := r.listOwnedResources(ctx, fsc, schema.GroupVersionKind{
		Group:   LocalDiskGroup,
		Version: LocalDiskVersion,
		Kind:    LocalDiskKind,
	}, LocalDiskList)
	if err != nil {
		return false, err
	}

	// 2) If none yet but validated, mark in-progress (idempotent)
	if len(owned) == 0 {
		if r.isConditionTrue(fsc, ConditionTypeDeviceValidated) {
			changed, err := r.updateConditionIfChanged(ctx, fsc, ConditionTypeLocalDiskCreated, metav1.ConditionFalse, ReasonLocalDiskCreationInProgress, "Waiting for LocalDisk objects to appear")
			if err != nil {
				return false, err
			}
			if !changed {
				logger.Info("LocalDiskCreated unchanged (no LDs yet); skipping patch")
			}
			return changed, nil
		}
		return false, nil
	}

	// 3) Collect names of Filesystems owned by this FSC (used to validate Used=True cases)
	ownedFS := map[string]struct{}{}
	ownedFS[fsc.Name] = struct{}{} // include the deterministic name even if informer lagged

	// Also check for any existing filesystems owned by this FSC
	ownedFilesystems, err := r.listOwnedResources(ctx, fsc, schema.GroupVersionKind{
		Group:   FileSystemGroup,
		Version: FileSystemVersion,
		Kind:    FileSystemKind,
	}, FileSystemList)
	if err == nil {
		for _, fs := range ownedFilesystems {
			ownedFS[fs.GetName()] = struct{}{}
		}
	}

	// 4) Check health of all LocalDisks
	allGood, failingName, failingMsg, hardFailure := r.checkAllResourcesHealthy(owned, []string{"Ready", "Used"}, ownedFS)

	// 5) Desired FSC condition
	var desiredStatus metav1.ConditionStatus
	var desiredReason, desiredMsg string

	if allGood {
		desiredStatus = metav1.ConditionTrue
		desiredReason = ReasonLocalDiskCreationSucceeded
		desiredMsg = fmt.Sprintf("All %d LocalDisks are Ready; if used, they are used by this Filesystem.", len(owned))
	} else {
		desiredStatus = metav1.ConditionFalse
		if hardFailure {
			desiredReason = ReasonLocalDiskCreationFailed
		} else {
			desiredReason = ReasonLocalDiskCreationInProgress
		}
		desiredMsg = fmt.Sprintf("LocalDisk %s: %s", failingName, failingMsg)
	}

	// 6) Update condition if changed
	changed, err := r.updateConditionIfChanged(ctx, fsc, ConditionTypeLocalDiskCreated, desiredStatus, desiredReason, desiredMsg)
	if err != nil {
		return false, err
	}

	if !changed {
		logger.Info("LocalDiskCreated condition unchanged; skipping patch")
	}

	logger.Info("synced LocalDisk conditions", "fsc", fsc.Name, "owned", len(owned), "allGood", allGood)
	return changed, nil
}

// ensureFileSystem creates FileSystem if it doesn't exist and returns its ready status
func (r *FileSystemClaimReconciler) ensureFileSystem(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	logger := log.FromContext(ctx)
	logger.Info("ensureFileSystem", "name", fsc.Name)

	// If localdisks are not created, we can't create a filesystem
	if !r.isConditionTrue(fsc, ConditionTypeLocalDiskCreated) {
		return false, nil
	}

	ownedLDs, err := r.listOwnedResources(ctx, fsc, schema.GroupVersionKind{
		Group:   LocalDiskGroup,
		Version: LocalDiskVersion,
		Kind:    LocalDiskKind,
	}, LocalDiskList)
	if err != nil {
		return false, fmt.Errorf("list LocalDisks: %w", err)
	}

	var ldNames []string
	for _, ld := range ownedLDs {
		ldNames = append(ldNames, ld.GetName())
	}
	if len(ldNames) == 0 {
		logger.Info("ensureFileSystem: no owned LocalDisks found despite LocalDiskCreated=True; skipping")
		return false, nil
	}

	desiredSpec := buildFilesystemSpec(ldNames)

	// List existing owned Filesystems
	owned, err := r.listOwnedResources(ctx, fsc, schema.GroupVersionKind{
		Group:   FileSystemGroup,
		Version: FileSystemVersion,
		Kind:    FileSystemKind,
	}, FileSystemList)
	if err != nil {
		return false, fmt.Errorf("list Filesystems: %w", err)
	}

	switch len(owned) {
	case 0:
		// No existing Filesystems found, create a new one
		fsName := fsc.Name

		fs := &unstructured.Unstructured{}
		fs.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   FileSystemGroup,
			Version: FileSystemVersion,
			Kind:    FileSystemKind,
		})
		fs.SetName(fsName)

		logger.Info("Creating Filesystem", "name", fsName, "disks", ldNames)
		if err := r.createResourceWithOwnership(ctx, fsc, fs, desiredSpec); err != nil {
			if e := r.handleResourceCreationError(ctx, fsc, "Filesystem", err); e != nil {
				return false, e
			}
			return true, nil
		}

		if _, e := r.updateConditionIfChanged(ctx, fsc, ConditionTypeFileSystemCreated, metav1.ConditionFalse, ReasonFileSystemCreationInProgress, "Filesystem created; waiting to become Ready"); e != nil {
			return false, e
		}
		return true, nil

	case 1:
		// One existing Filesystem found, check for drift and patch if needed
		fs := &owned[0]
		changed, err := r.detectAndPatchDrift(ctx, fs, func(obj client.Object) bool {
			u := obj.(*unstructured.Unstructured)
			currentSpec, _, _ := unstructured.NestedMap(u.Object, "spec")
			if !reflect.DeepEqual(currentSpec, desiredSpec) {
				u.Object["spec"] = desiredSpec
				return true
			}
			return false
		})
		if err != nil {
			return false, fmt.Errorf("patch Filesystem: %w", err)
		}
		return changed, nil

	default:
		// More than one existing Filesystem found, error out
		msg := fmt.Sprintf("found %d Filesystems owned by FSC; expected 1", len(owned))
		err := fmt.Errorf("%s", msg)
		if e := r.handleResourceCreationError(ctx, fsc, "Filesystem", err); e != nil {
			return false, e
		}
		return true, nil
	}
}

// syncFilesystemConditions updates ConditionTypeFileSystemCreated by inspecting owned Filesystem objects.
// Keep this conservative: "InProgress" unless we can positively assert success or failure.
func (r *FileSystemClaimReconciler) syncFilesystemConditions(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	logger := log.FromContext(ctx)

	// Do not surface FilesystemCreated at all until LocalDiskCreated is True.
	if !r.isConditionTrue(fsc, ConditionTypeLocalDiskCreated) {
		return false, nil
	}

	owned, err := r.listOwnedResources(ctx, fsc, schema.GroupVersionKind{
		Group:   FileSystemGroup,
		Version: FileSystemVersion,
		Kind:    FileSystemKind,
	}, FileSystemList)
	if err != nil {
		return false, fmt.Errorf("list Filesystems: %w", err)
	}

	var desiredStatus metav1.ConditionStatus
	var desiredReason, desiredMsg string

	if len(owned) == 0 {
		desiredStatus = metav1.ConditionFalse
		desiredReason = ReasonFileSystemCreationInProgress
		desiredMsg = "Waiting for Filesystem to be created"
	} else {
		allGood, failingName, failingMsg, _ := r.checkAllResourcesHealthy(owned, []string{"Success", "Healthy"}, nil)

		if allGood {
			desiredStatus = metav1.ConditionTrue
			desiredReason = ReasonFileSystemCreationSucceeded
			desiredMsg = "Filesystem is Success=True and Healthy=True"
		} else {
			desiredStatus = metav1.ConditionFalse
			desiredReason = ReasonFileSystemCreationInProgress
			desiredMsg = fmt.Sprintf("Filesystem %s not healthy: %s", failingName, failingMsg)
		}
	}

	// Update condition if changed
	changed, err := r.updateConditionIfChanged(ctx, fsc, ConditionTypeFileSystemCreated, desiredStatus, desiredReason, desiredMsg)
	if err != nil {
		return false, err
	}

	if !changed {
		logger.Info("FilesystemCreated condition unchanged; skipping patch")
	}

	logger.Info("synced Filesystem conditions", "fsc", fsc.Name, "owned", len(owned), "status", string(desiredStatus))
	return changed, nil
}

// ensureStorageClass creates StorageClass if it doesn't exist and returns its ready status
func (r *FileSystemClaimReconciler) ensureStorageClass(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	logger := log.FromContext(ctx)

	// Gate on Filesystem being ready
	if !r.isConditionTrue(fsc, ConditionTypeFileSystemCreated) {
		return false, nil
	}

	scName := fsc.Name // Use FSC name directly
	fsName := fsc.Name // the Filesystem name we created

	allowExpand := true
	reclaim := corev1.PersistentVolumeReclaimDelete
	bindMode := storagev1.VolumeBindingImmediate

	desired := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: scName,
			Annotations: map[string]string{
				"storageclass.kubevirt.io/is-default-virt-class": "true",
			},
			Labels: map[string]string{
				"fusion.storage.openshift.io/owned-by-name":      fsc.Name,
				"fusion.storage.openshift.io/owned-by-namespace": fsc.Namespace,
			},
		},
		Provisioner:          "spectrumscale.csi.ibm.com",
		AllowVolumeExpansion: &allowExpand,
		ReclaimPolicy:        &reclaim,
		VolumeBindingMode:    &bindMode,
		Parameters: map[string]string{
			"volBackendFs": fsName, // << template requires this key
		},
	}

	current := &storagev1.StorageClass{}
	err := r.Get(ctx, types.NamespacedName{Name: scName}, current)
	switch {
	case errors.IsNotFound(err):
		logger.Info("Creating StorageClass", "name", scName, "filesystem", fsName)
		if err := r.Create(ctx, desired); err != nil {
			if e := r.handleResourceCreationError(ctx, fsc, "StorageClass", err); e != nil {
				return false, e
			}
			return true, nil
		}

		// mark SC created (idempotent guard)
		changed, err := r.updateConditionIfChanged(ctx, fsc, ConditionTypeStorageClassCreated, metav1.ConditionTrue, ReasonStorageClassCreationSucceeded, "StorageClass created")
		if err != nil {
			return false, err
		}
		return changed, nil

	case err != nil:
		return false, fmt.Errorf("get StorageClass %q: %w", scName, err)

	default:
		// StorageClass exists - check for drift and patch if needed
		changed, err := r.detectAndPatchDrift(ctx, current, func(obj client.Object) bool {
			sc := obj.(*storagev1.StorageClass)

			// Compare the entire current StorageClass with desired
			// We only care about the fields we manage
			currentRelevant := &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: sc.Annotations,
					Labels:      sc.Labels,
				},
				Provisioner:          sc.Provisioner,
				AllowVolumeExpansion: sc.AllowVolumeExpansion,
				ReclaimPolicy:        sc.ReclaimPolicy,
				VolumeBindingMode:    sc.VolumeBindingMode,
				Parameters:           sc.Parameters,
			}

			desiredRelevant := &storagev1.StorageClass{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: desired.Annotations,
					Labels:      desired.Labels,
				},
				Provisioner:          desired.Provisioner,
				AllowVolumeExpansion: desired.AllowVolumeExpansion,
				ReclaimPolicy:        desired.ReclaimPolicy,
				VolumeBindingMode:    desired.VolumeBindingMode,
				Parameters:           desired.Parameters,
			}

			if !reflect.DeepEqual(currentRelevant, desiredRelevant) {
				sc.Annotations = desired.Annotations
				sc.Labels = desired.Labels
				sc.Provisioner = desired.Provisioner
				sc.AllowVolumeExpansion = desired.AllowVolumeExpansion
				sc.ReclaimPolicy = desired.ReclaimPolicy
				sc.VolumeBindingMode = desired.VolumeBindingMode
				sc.Parameters = desired.Parameters
				return true
			}
			return false
		})
		if err != nil {
			return false, fmt.Errorf("patch StorageClass %q: %w", scName, err)
		}

		// Ensure condition is True (idempotent)
		conditionChanged, err := r.updateConditionIfChanged(ctx, fsc, ConditionTypeStorageClassCreated, metav1.ConditionTrue, ReasonStorageClassCreationSucceeded, "StorageClass present")
		if err != nil {
			return false, err
		}
		return changed || conditionChanged, nil
	}
}

// syncFSCReady aggregates the overall Ready condition from the sub-conditions.
func (r *FileSystemClaimReconciler) syncFSCReady(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	readyNow := r.isConditionTrue(fsc, ConditionTypeDeviceValidated) &&
		r.isConditionTrue(fsc, ConditionTypeLocalDiskCreated) &&
		r.isConditionTrue(fsc, ConditionTypeFileSystemCreated) &&
		r.isConditionTrue(fsc, ConditionTypeStorageClassCreated)

	var status metav1.ConditionStatus
	var reason, msg string
	if readyNow {
		status, reason, msg = metav1.ConditionTrue, ReasonProvisioningSucceeded, "All resources created and ready"
	} else {
		status, reason, msg = metav1.ConditionFalse, ReasonProvisioningInProgress, "Provisioning in progress"
	}

	return r.updateConditionIfChanged(ctx, fsc, ConditionTypeReady, status, reason, msg)
}

// Handlers for FileSystemClaim reconciliation -- END

// Helper functions -- START

// hasConditionWithReason checks if a condition exists with the given type and reason
func (r *FileSystemClaimReconciler) hasConditionWithReason(conds []metav1.Condition, condType, reason string) bool {
	cond := apimeta.FindStatusCondition(conds, condType)
	return cond != nil && cond.Reason == reason
}

// convert unstructured.Slice to metav1.Condition
func asMetaConditions(sl []interface{}) []metav1.Condition {
	out := make([]metav1.Condition, 0, len(sl))
	for _, it := range sl {
		m, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		var c metav1.Condition
		if v, _, _ := unstructured.NestedString(m, "type"); v != "" {
			c.Type = v
		}
		if v, _, _ := unstructured.NestedString(m, "status"); v != "" {
			c.Status = metav1.ConditionStatus(v)
		}
		if v, _, _ := unstructured.NestedString(m, "reason"); v != "" {
			c.Reason = v
		}
		if v, _, _ := unstructured.NestedString(m, "message"); v != "" {
			c.Message = v
		}
		if v, _, _ := unstructured.NestedString(m, "lastTransitionTime"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				c.LastTransitionTime = metav1.NewTime(t)
			}
		}
		out = append(out, c)
	}
	return out
}

// patchFSCStatus safely patches status with retry-on-conflict.
func (r *FileSystemClaimReconciler) patchFSCStatus(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim, mutate func(*fusionv1alpha1.FileSystemClaim)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// refetch latest to get fresh resourceVersion
		cur := &fusionv1alpha1.FileSystemClaim{}
		if err := r.Get(ctx, types.NamespacedName{Name: fsc.Name, Namespace: fsc.Namespace}, cur); err != nil {
			return err
		}
		orig := cur.DeepCopy()
		mutate(cur)
		return r.Status().Patch(ctx, cur, client.MergeFrom(orig))
	})
}

// patchFSC safely patches metadata and spec updates with retry-on-conflict.
func (r *FileSystemClaimReconciler) patchFSCSpec(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim, mutate func(*fusionv1alpha1.FileSystemClaim)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &fusionv1alpha1.FileSystemClaim{}
		if err := r.Get(ctx, types.NamespacedName{Name: fsc.Name, Namespace: fsc.Namespace}, cur); err != nil {
			return err
		}
		orig := cur.DeepCopy()
		mutate(cur)
		return r.Patch(ctx, cur, client.MergeFrom(orig))
	})
}

// isOwnedByThisFSC returns true if obj has an OwnerReference to the given FSC name
// (Kind/APIVersion match; Controller bit not required).
func isOwnedByThisFSC(obj client.Object, fscName string) bool {
	for _, or := range obj.GetOwnerReferences() {
		if or.Kind == "FileSystemClaim" &&
			or.APIVersion == "fusion.storage.openshift.io/v1alpha1" &&
			or.Name == fscName {
			return true
		}
	}
	return false
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
	for i := range allNodes.Items {
		node := &allNodes.Items[i]
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
	idx, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(len(storageNodes))))
	if err != nil {
		return "", fmt.Errorf("failed to select random storage node: %w", err)
	}
	selectedNode := storageNodes[idx.Int64()]

	logger.Info("Selected random storage node", "node", selectedNode, "totalStorageNodes", len(storageNodes))
	return selectedNode, nil
}

// validateDevices checks if the specified devices are present in ALL LocalVolumeDiscoveryResult
// which ensures both the device is valid and shared across all nodes.
// When this function is called.
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
	for i := range allNodes.Items {
		node := &allNodes.Items[i]
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
	for _, device := range fsc.Spec.Devices {
		for nodeName, lvdr := range lvdrs {
			// Check if DiscoveredDevices exists and is not empty
			if len(lvdr.Status.DiscoveredDevices) == 0 {
				return fmt.Errorf("no discovered devices available for node %s. "+
					"Device: %s may be in use in another filesystem or is not "+
					"shared across all nodes", nodeName, device)
			}

			deviceFound := false
			for _, discoveredDevice := range lvdr.Status.DiscoveredDevices {
				if discoveredDevice.Path == device {
					deviceFound = true
					break
				}
			}

			if !deviceFound {
				return fmt.Errorf("device %s not found in LocalVolumeDiscoveryResult for node %s", device, nodeName)
			}
		}

		logger.Info("Device validation successful", "device", device, "availableOnAllNodesWithWorkerAndstorageLabel", len(lvdrs))
	}

	return nil
}

// getDeviceWWN looks up the WWN for a device path from LocalVolumeDiscoveryResult
func (r *FileSystemClaimReconciler) getDeviceWWN(
	ctx context.Context,
	devicePath string,
	nodeName string,
) (string, error) {
	logger := log.FromContext(ctx)

	// Get the operator namespace
	operatorNamespace, err := utils.GetDeploymentNamespace()
	if err != nil {
		return "", fmt.Errorf("failed to get operator deployment namespace: %w", err)
	}

	// Construct LVDR name
	lvdrName := fmt.Sprintf("discovery-result-%s", nodeName)

	// Get the LVDR for the node
	lvdr := &fusionv1alpha1.LocalVolumeDiscoveryResult{}
	err = r.Get(ctx, types.NamespacedName{
		Name:      lvdrName,
		Namespace: operatorNamespace,
	}, lvdr)

	if err != nil {
		if errors.IsNotFound(err) {
			return "", fmt.Errorf("LocalVolumeDiscoveryResult %s not found for node %s", lvdrName, nodeName)
		}
		return "", fmt.Errorf("failed to get LocalVolumeDiscoveryResult for node %s: %w", nodeName, err)
	}

	// Search for the device in DiscoveredDevices
	for _, device := range lvdr.Status.DiscoveredDevices {
		if device.Path == devicePath {
			if device.WWN == "" {
				return "", fmt.Errorf("device %s found but WWN is empty", devicePath)
			}
			logger.Info("Found WWN for device", "device", devicePath, "wwn", device.WWN, "node", nodeName)
			return device.WWN, nil
		}
	}

	return "", fmt.Errorf("device %s not found in LocalVolumeDiscoveryResult for node %s", devicePath, nodeName)
}

// generateLocalDiskName generates a LocalDisk name from device path and WWN
func generateLocalDiskName(devicePath, wwn string) (string, error) {
	// Extract device name from path (e.g., /dev/nvme1n1 -> nvme1n1)
	deviceName := filepath.Base(devicePath)
	if deviceName == "" || deviceName == "." {
		return "", fmt.Errorf("invalid device path: %s", devicePath)
	}

	// Clean WWN - remove common prefixes to make it Kubernetes-compatible
	cleanWWN := wwn
	if strings.HasPrefix(wwn, "uuid.") {
		cleanWWN = strings.TrimPrefix(wwn, "uuid.")
	} else if strings.HasPrefix(wwn, "0x") {
		cleanWWN = strings.TrimPrefix(wwn, "0x")
	}

	// Combine device name with cleaned WWN
	name := fmt.Sprintf("%s-%s", deviceName, cleanWWN)

	// Validate Kubernetes resource name constraints
	if len(name) > maxKubernetesNameLength {
		return "", fmt.Errorf("generated name too long: %s (max 253 chars)", name)
	}

	// Basic validation for Kubernetes resource names
	if !isValidKubernetesName(name) {
		return "", fmt.Errorf("generated name is not a valid Kubernetes resource name: %s", name)
	}

	return name, nil
}

// isValidKubernetesName checks if a string is a valid Kubernetes resource name
func isValidKubernetesName(name string) bool {
	if name == "" || len(name) > maxKubernetesNameLength {
		return false
	}

	// Must start and end with alphanumeric character
	first, _ := utf8.DecodeRuneInString(name)
	last, _ := utf8.DecodeLastRuneInString(name)
	if !isAlphanumeric(first) || !isAlphanumeric(last) {
		return false
	}

	// Can contain alphanumeric characters, hyphens, and dots
	for _, char := range name {
		if !isAlphanumeric(char) && char != '-' && char != '.' {
			return false
		}
	}

	return true
}

// isAlphanumeric checks if a character is alphanumeric
func isAlphanumeric(char rune) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9')
}

// listOwnedResources lists all resources of a given GVK owned by the FSC
func (r *FileSystemClaimReconciler) listOwnedResources(
	ctx context.Context,
	fsc *fusionv1alpha1.FileSystemClaim,
	gvk schema.GroupVersionKind,
	listKind string,
) ([]unstructured.Unstructured, error) {
	resourceList := &unstructured.UnstructuredList{}
	resourceList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    listKind,
	})

	if err := r.List(ctx, resourceList, client.InNamespace(fsc.Namespace)); err != nil {
		return nil, fmt.Errorf("list %s: %w", listKind, err)
	}

	var owned []unstructured.Unstructured
	for _, item := range resourceList.Items {
		if isOwnedByThisFSC(&item, fsc.Name) {
			owned = append(owned, item)
		}
	}

	return owned, nil
}

// updateConditionIfChanged updates a condition only if status/reason/message changed
func (r *FileSystemClaimReconciler) updateConditionIfChanged(
	ctx context.Context,
	fsc *fusionv1alpha1.FileSystemClaim,
	conditionType string,
	status metav1.ConditionStatus,
	reason string,
	message string,
) (bool, error) {
	// Check if condition already has the desired state
	prev := apimeta.FindStatusCondition(fsc.Status.Conditions, conditionType)
	if prev != nil && prev.Status == status && prev.Reason == reason && prev.Message == message {
		return false, nil
	}

	// Update the condition
	if err := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
		cur.Status.Conditions = utils.UpdateCondition(
			cur.Status.Conditions,
			conditionType,
			status,
			reason,
			message,
			cur.Generation,
		)
	}); err != nil {
		return false, err
	}

	return true, nil
}

// handleResourceCreationError updates both resource-specific and Ready conditions on creation errors
func (r *FileSystemClaimReconciler) handleResourceCreationError(
	ctx context.Context,
	fsc *fusionv1alpha1.FileSystemClaim,
	resourceType string,
	err error,
) error {
	var conditionType string
	var reason string

	switch resourceType {
	case "LocalDisk":
		conditionType = ConditionTypeLocalDiskCreated
		reason = ReasonLocalDiskCreationFailed
	case "Filesystem":
		conditionType = ConditionTypeFileSystemCreated
		reason = ReasonFileSystemCreationFailed
	case "StorageClass":
		conditionType = ConditionTypeStorageClassCreated
		reason = ReasonStorageClassCreationFailed
	default:
		return fmt.Errorf("unknown resource type: %s", resourceType)
	}

	// Update Ready condition first, then resource-specific condition
	if e := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
		cur.Status.Conditions = utils.UpdateCondition(
			cur.Status.Conditions,
			conditionType,
			metav1.ConditionFalse,
			reason,
			err.Error(),
			cur.Generation,
		)
		cur.Status.Conditions = utils.UpdateCondition(
			cur.Status.Conditions,
			ConditionTypeReady,
			metav1.ConditionFalse,
			ReasonProvisioningFailed,
			fmt.Sprintf("%s creation failed", resourceType),
			cur.Generation,
		)
	}); e != nil {
		return e
	}

	return nil
}

// extractResourceConditions extracts and converts status.conditions from unstructured objects
func extractResourceConditions(obj *unstructured.Unstructured) ([]metav1.Condition, error) {
	sl, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found {
		return nil, fmt.Errorf("status.conditions missing")
	}
	return asMetaConditions(sl), nil
}

// checkResourceCondition checks if a condition exists and matches expected status
func checkResourceCondition(
	conds []metav1.Condition,
	conditionType string,
	expectedStatus metav1.ConditionStatus,
) (matched bool, message string) {
	condition := apimeta.FindStatusCondition(conds, conditionType)
	if condition == nil {
		return false, fmt.Sprintf("%s condition not found", conditionType)
	}
	if condition.Status != expectedStatus {
		return false, condition.Message
	}
	return true, ""
}

// checkAllResourcesHealthy validates that all resources meet health criteria
func (r *FileSystemClaimReconciler) checkAllResourcesHealthy(
	resources []unstructured.Unstructured,
	requiredConditions []string,
	ownedFilesystems map[string]struct{},
) (allHealthy bool, failingName, failingMsg string, hardFailure bool) {
	for _, resource := range resources {
		conds, err := extractResourceConditions(&resource)
		if err != nil {
			return false, resource.GetName(), err.Error(), false
		}

		// Check all required conditions
		for _, conditionType := range requiredConditions {
			switch conditionType {
			case "Ready":
				if isMatch, msg := checkResourceCondition(conds, "Ready", metav1.ConditionTrue); !isMatch {
					return false, resource.GetName(), msg, false
				}
			case "Used":
				// Special handling for Used condition
				usedCondition := apimeta.FindStatusCondition(conds, "Used")
				if usedCondition == nil {
					return false, resource.GetName(), "Used condition not found", false
				}
				if usedCondition.Status == metav1.ConditionTrue {
					// Check if used by our filesystem
					fsName, _, _ := unstructured.NestedString(resource.Object, "status", "filesystem")
					if _, ok := ownedFilesystems[fsName]; !ok || fsName == "" {
						if fsName == "" {
							return false, resource.GetName(), "LocalDisk is used but status.filesystem is empty or missing", true
						}
						return false, resource.GetName(), fmt.Sprintf("LocalDisk is used by different filesystem %q", fsName), true
					}
				}
			case "Success":
				if isMatch, msg := checkResourceCondition(conds, "Success", metav1.ConditionTrue); !isMatch {
					return false, resource.GetName(), msg, false
				}
			case "Healthy":
				if isMatch, msg := checkResourceCondition(conds, "Healthy", metav1.ConditionTrue); !isMatch {
					return false, resource.GetName(), msg, false
				}
			}
		}
	}

	return true, "", "", false
}

// createResourceWithOwnership creates a resource with owner reference in one call
func (r *FileSystemClaimReconciler) createResourceWithOwnership(
	ctx context.Context,
	fsc *fusionv1alpha1.FileSystemClaim,
	obj *unstructured.Unstructured,
	spec map[string]interface{},
) error {
	obj.SetNamespace(fsc.Namespace)
	if err := controllerutil.SetOwnerReference(fsc, obj, r.Scheme); err != nil {
		return fmt.Errorf("set ownerRef: %w", err)
	}
	if obj.Object == nil {
		obj.Object = map[string]interface{}{}
	}
	obj.Object["spec"] = spec
	return r.Create(ctx, obj)
}

// detectAndPatchDrift performs generic drift detection and patching
func (r *FileSystemClaimReconciler) detectAndPatchDrift(
	ctx context.Context,
	current client.Object,
	updateFunc func(client.Object) bool,
) (bool, error) {
	orig := current.DeepCopyObject().(client.Object)
	changed := updateFunc(current)

	if !changed {
		return false, nil
	}

	if err := r.Patch(ctx, current, client.MergeFrom(orig)); err != nil {
		return false, fmt.Errorf("patch resource: %w", err)
	}

	return true, nil
}

// handleValidationError updates both Ready and DeviceValidated conditions for validation errors
func (r *FileSystemClaimReconciler) handleValidationError(
	ctx context.Context,
	fsc *fusionv1alpha1.FileSystemClaim,
	err error,
) error {
	return r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
		cur.Status.Conditions = utils.UpdateCondition(
			cur.Status.Conditions, ConditionTypeReady,
			metav1.ConditionFalse, ReasonValidationFailed, err.Error(), cur.Generation,
		)
		cur.Status.Conditions = utils.UpdateCondition(
			cur.Status.Conditions, ConditionTypeDeviceValidated,
			metav1.ConditionFalse, ReasonDeviceValidationFailed, err.Error(), cur.Generation,
		)
	})
}

// buildFilesystemSpec constructs the standard Filesystem spec structure
func buildFilesystemSpec(ldNames []string) map[string]interface{} {
	toIface := func(ss []string) []interface{} {
		out := make([]interface{}, len(ss))
		for i, s := range ss {
			out[i] = s
		}
		return out
	}

	return map[string]interface{}{
		"local": map[string]interface{}{
			"blockSize": "4M",
			"pools": []interface{}{
				map[string]interface{}{
					"name":  "system",
					"disks": toIface(ldNames),
				},
			},
			"replication": "1-way",
			"type":        "shared",
		},
		"seLinuxOptions": map[string]interface{}{
			"level": "s0",
			"role":  "object_r",
			"type":  "container_file_t",
			"user":  "system_u",
		},
	}
}

// Helper functions -- END

// Handlers for watched resources -- START

func enqueueFSCByOwner() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
		gvk := fusionv1alpha1.GroupVersion.WithKind("FileSystemClaim")
		owners := obj.GetOwnerReferences()
		for _, o := range owners {
			if o.APIVersion == gvk.GroupVersion().String() && o.Kind == gvk.Kind {
				// LocalDisk and Filesystem are namespaced; owner lives in the same namespace.
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Namespace: obj.GetNamespace(),
						Name:      o.Name,
					},
				}}
			}
		}
		return nil
	})
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
			ownerRef.APIVersion == "fusion.storage.openshift.io/v1alpha1" {
			return true
		}
	}
	return false
}

// didResourceStatusChange returns true if the LocalDisk or FileSystem status has changed
func didResourceStatusChange() builder.WatchesOption {
	return builder.WithPredicates(predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			if !isInTargetNamespace(e.ObjectNew) || !isOwnedByFileSystemClaim(e.ObjectNew) {
				return false
			}

			oldObj, okOld := e.ObjectOld.(*unstructured.Unstructured)
			newObj, okNew := e.ObjectNew.(*unstructured.Unstructured)
			if !okOld || !okNew {
				return false
			}

			oldStatus, oldHas := oldObj.Object["status"]
			newStatus, newHas := newObj.Object["status"]

			if !oldHas && !newHas {
				return false
			}
			// if oldHas != newHas {
			// 	return true
			// }
			if !newHas {
				return false
			}
			return !reflect.DeepEqual(oldStatus, newStatus)
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return false
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return false
		},
	})
}

// Handlers for watched resources -- END

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
			enqueueFSCByOwner(),
			didResourceStatusChange(),
		).
		Watches(
			&unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": FileSystemGroup + "/" + FileSystemVersion,
					"kind":       FileSystemKind,
				},
			},
			enqueueFSCByOwner(),
			didResourceStatusChange(),
		).
		Named("filesystemclaim").
		Complete(r)
}
