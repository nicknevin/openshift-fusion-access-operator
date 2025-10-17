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
	"reflect"
	"time"

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
	err := r.Get(ctx, req.NamespacedName, fsc)
	if err != nil {
		logger.Error(err, "Failed to get FileSystemClaim:"+req.NamespacedName.Name)
		return ctrl.Result{}, err
	}
	logger.Info("Reconciling FileSystemClaim", "name", fsc.Name, "namespace", fsc.Namespace)

	// Finalizers first
	if changed, err := r.handleFinalizers(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		// We wrote something (finalizer add/remove). Requeue to read fresh.
		return ctrl.Result{Requeue: true}, nil
	}

	// Handle deletion
	if changed, err := r.handleDeletion(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{Requeue: true}, nil
	}

	// 1) Ensure LocalDisks exist (create/update if needed)
	if changed, err := r.ensureLocalDisks(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		// We created/updated children; let cache/watches settle.
		return ctrl.Result{Requeue: true}, nil
	}

	// 2) Sync FSC conditions from owned LocalDisks
	if changed, err := r.syncLocalDiskConditions(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		// We updated FSC status; that’s a write -> requeue once.
		return ctrl.Result{Requeue: true}, nil
	}

	// 3) Ensure Filesystems (only if LD preconditions are satisfied)
	if changed, err := r.ensureFileSystem(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{Requeue: true}, nil
	}

	// 4) Sync FSC conditions from owned Filesystems
	if changed, err := r.syncFilesystemConditions(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{Requeue: true}, nil
	}

	// 5) Ensure StorageClass (only after Filesystem ready)
	if changed, err := r.ensureStorageClass(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{Requeue: true}, nil
	}

	// 6) Aggregate/Ready
	if changed, err := r.syncFSCReady(ctx, fsc); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{Requeue: true}, nil
	}

	logger.Info("FileSystemClaim reconciliation completed successfully")
	return ctrl.Result{}, nil

}

// Handlers for FileSystemClaim reconciliation -- START

// handleFinalizers handles the finalizers for the FileSystemClaim
func (r *FileSystemClaimReconciler) handleFinalizers(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(fsc, FileSystemClaimFinalizer) {
		if err := r.patchFSC(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
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

		if err := r.patchFSC(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
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

	// Phase 1: validate once
	if !r.isConditionTrue(fsc, ConditionTypeDeviceValidated) {
		if err := r.validateDevices(ctx, fsc); err != nil {
			logger.Error(err, "Device validation failed")
			if e := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
				cur.Status.Conditions = utils.UpdateCondition(
					cur.Status.Conditions,
					ConditionTypeReady,
					metav1.ConditionFalse,
					ReasonValidationFailed,
					err.Error(),
					cur.Generation,
				)
				cur.Status.Conditions = utils.UpdateCondition(
					cur.Status.Conditions,
					ConditionTypeDeviceValidated,
					metav1.ConditionFalse,
					ReasonDeviceValidationFailed,
					err.Error(),
					cur.Generation,
				)
			}); e != nil {
				logger.Error(e, "Failed to update status after disk validation failure")
				return false, e
			}
			return true, nil
		}

		if e := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
			cur.Status.Conditions = utils.UpdateCondition(
				cur.Status.Conditions,
				ConditionTypeDeviceValidated,
				metav1.ConditionTrue,
				ReasonDeviceValidationSucceeded,
				"Device/s validation succeeded",
				cur.Generation,
			)
		}); e != nil {
			logger.Error(e, "Failed to update status after device validation success")
			return false, e
		}
		return true, nil
	}

	// Phase 2: ensure LocalDisks
	for index, devicePath := range fsc.Spec.Devices {
		localDiskName := fmt.Sprintf("%s-ld-%d", fsc.Name, index)

		ld := &unstructured.Unstructured{}
		ld.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   LocalDiskGroup,
			Version: LocalDiskVersion,
			Kind:    LocalDiskKind,
		})

		err := r.Get(ctx, types.NamespacedName{
			Namespace: fsc.Namespace,
			Name:      localDiskName,
		}, ld)

		switch {
		case errors.IsNotFound(err):
			nodeName, selErr := r.getRandomStorageNode(ctx)
			if selErr != nil {
				logger.Error(selErr, "failed to pick a storage node", "device", devicePath)
				if e := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
					cur.Status.Conditions = utils.UpdateCondition(
						cur.Status.Conditions,
						ConditionTypeLocalDiskCreated,
						metav1.ConditionFalse,
						ReasonLocalDiskCreationFailed,
						fmt.Sprintf("failed to pick node for %s: %v", devicePath, selErr),
						cur.Generation,
					)
					cur.Status.Conditions = utils.UpdateCondition(
						cur.Status.Conditions,
						ConditionTypeReady,
						metav1.ConditionFalse,
						ReasonProvisioningFailed,
						"LocalDisk creation failed",
						cur.Generation,
					)
				}); e != nil {
					return false, e
				}
				return true, nil
			}

			ld.SetNamespace(fsc.Namespace)
			ld.SetName(localDiskName)
			if err := controllerutil.SetOwnerReference(fsc, ld, r.Scheme); err != nil {
				return false, fmt.Errorf("set ownerRef: %w", err)
			}
			if ld.Object == nil {
				ld.Object = map[string]interface{}{}
			}
			ld.Object["spec"] = map[string]interface{}{"device": devicePath, "node": nodeName}

			logger.Info("Creating LocalDisk", "name", localDiskName, "device", devicePath, "node", nodeName)
			if err := r.Create(ctx, ld); err != nil {
				logger.Error(err, "failed to create LocalDisk", "name", localDiskName)
				if e := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
					cur.Status.Conditions = utils.UpdateCondition(
						cur.Status.Conditions,
						ConditionTypeLocalDiskCreated,
						metav1.ConditionFalse,
						ReasonLocalDiskCreationFailed,
						err.Error(),
						cur.Generation,
					)
					cur.Status.Conditions = utils.UpdateCondition(
						cur.Status.Conditions,
						ConditionTypeReady,
						metav1.ConditionFalse,
						ReasonProvisioningFailed,
						"LocalDisk creation failed",
						cur.Generation,
					)
				}); e != nil {
					return false, e
				}
				return true, nil
			}

			if e := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
				cur.Status.Conditions = utils.UpdateCondition(
					cur.Status.Conditions,
					ConditionTypeLocalDiskCreated,
					metav1.ConditionFalse,
					ReasonLocalDiskCreationInProgress,
					"LocalDisks created, waiting for them to become ready",
					cur.Generation,
				)
			}); e != nil {
				return false, e
			}
			return true, nil

		case err != nil:
			return false, fmt.Errorf("Failed to get LocalDisk %s: %w", localDiskName, err)

		default:
			orig := ld.DeepCopy()
			device := getNestedString(ld.Object, "spec", "device")
			node := getNestedString(ld.Object, "spec", "node")

			needsUpdate := device != devicePath
			if needsUpdate {
				if ld.Object == nil {
					ld.Object = map[string]interface{}{}
				}
				ld.Object["spec"] = map[string]interface{}{
					"device": devicePath,
					"node":   node,
				}
				if err := r.Patch(ctx, ld, client.MergeFrom(orig)); err != nil {
					return false, fmt.Errorf("patch LocalDisk %s: %w", localDiskName, err)
				}
				return true, nil
			}
		}
	}
	return false, nil
}

// syncLocalDiskConditions inspects all LocalDisks owned by this FSC and updates
// ConditionTypeLocalDiskCreated with a precise reason/message. Returns changed=true if we wrote status.
func (r *FileSystemClaimReconciler) syncLocalDiskConditions(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	logger := log.FromContext(ctx)

	// 1) List owned LocalDisks
	ldList := &unstructured.UnstructuredList{}
	ldList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   LocalDiskGroup,
		Version: LocalDiskVersion,
		Kind:    LocalDiskList,
	})
	if err := r.List(ctx, ldList, client.InNamespace(fsc.Namespace)); err != nil {
		return false, fmt.Errorf("list LocalDisks: %w", err)
	}
	var owned []unstructured.Unstructured
	for _, it := range ldList.Items {
		if isOwnedByThisFSC(&it, fsc.Name) {
			owned = append(owned, it)
		}
	}

	// 2) If none yet but validated, mark in-progress (idempotent)
	if len(owned) == 0 {
		if r.isConditionTrue(fsc, ConditionTypeDeviceValidated) {
			desiredStatus := metav1.ConditionFalse
			desiredReason := ReasonLocalDiskCreationInProgress
			desiredMsg := "Waiting for LocalDisk objects to appear"

			prev := apimeta.FindStatusCondition(fsc.Status.Conditions, ConditionTypeLocalDiskCreated)
			if prev != nil && prev.Status == desiredStatus && prev.Reason == desiredReason && prev.Message == desiredMsg {
				logger.Info("LocalDiskCreated unchanged (no LDs yet); skipping patch")
				return false, nil
			}
			if err := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
				cur.Status.Conditions = utils.UpdateCondition(
					cur.Status.Conditions,
					ConditionTypeLocalDiskCreated,
					desiredStatus, desiredReason, desiredMsg,
					cur.Generation,
				)
			}); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, nil
	}

	// 3) Collect names of Filesystems owned by this FSC (used to validate Used=True cases)
	fsList := &unstructured.UnstructuredList{}
	fsList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   FileSystemGroup,
		Version: FileSystemVersion,
		Kind:    FileSystemList,
	})
	ownedFS := map[string]struct{}{}
	if err := r.List(ctx, fsList, client.InNamespace(fsc.Namespace)); err == nil {
		for _, it := range fsList.Items {
			if isOwnedByThisFSC(&it, fsc.Name) {
				ownedFS[it.GetName()] = struct{}{}
			}
		}
	}
	// include the deterministic name even if informer lagged
	ownedFS[fmt.Sprintf("%s-fs", fsc.Name)] = struct{}{}

	// 4) Inspect each LocalDisk
	allGood := true
	hardFailure := false
	var failingName, failingMsg string

	for _, ld := range owned {
		// conditions -> []metav1.Condition
		sl, found, _ := unstructured.NestedSlice(ld.Object, "status", "conditions")
		if !found {
			allGood = false
			failingName, failingMsg = ld.GetName(), "status.conditions missing"
			break
		}
		conds := asMetaConditions(sl)

		// Ready must be True
		if !apimeta.IsStatusConditionTrue(conds, "Ready") {
			allGood = false
			if c := apimeta.FindStatusCondition(conds, "Ready"); c != nil {
				failingMsg = c.Message
			} else {
				failingMsg = "Ready condition not found"
			}
			failingName = ld.GetName()
			break
		}

		// Used logic:
		// - Used=False  -> OK (pre-FS stage)
		// - Used=True   -> OK only if status.filesystem belongs to this FSC
		cUsed := apimeta.FindStatusCondition(conds, "Used")
		if cUsed == nil {
			allGood = false
			failingName, failingMsg = ld.GetName(), "Used condition not found"
			break
		}
		if cUsed.Status == metav1.ConditionTrue {
			// read status.filesystem (string)
			fsName, _, _ := unstructured.NestedString(ld.Object, "status", "filesystem")
			if _, ok := ownedFS[fsName]; !ok || fsName == "" {
				allGood = false
				hardFailure = true
				if fsName == "" {
					failingMsg = "LocalDisk is used but status.filesystem is empty or missing"
				} else {
					failingMsg = fmt.Sprintf("LocalDisk is used by different filesystem %q", fsName)
				}
				failingName = ld.GetName()
				break
			}
			// Used by our FS → OK
		}

		// NOTE: We intentionally do NOT gate on Available; it may be True or False.
		// (Previously this caused false positives after the FS claimed the disk.)
	}

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

	// 6) Idempotency guard
	prev := apimeta.FindStatusCondition(fsc.Status.Conditions, ConditionTypeLocalDiskCreated)
	if prev != nil && prev.Status == desiredStatus && prev.Reason == desiredReason && prev.Message == desiredMsg {
		logger.Info("LocalDiskCreated condition unchanged; skipping patch")
		return false, nil
	}

	if err := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
		cur.Status.Conditions = utils.UpdateCondition(
			cur.Status.Conditions,
			ConditionTypeLocalDiskCreated,
			desiredStatus, desiredReason, desiredMsg,
			cur.Generation,
		)
	}); err != nil {
		return false, err
	}

	logger.Info("synced LocalDisk conditions", "fsc", fsc.Name, "owned", len(owned), "allGood", allGood)
	return true, nil
}

// ensureFileSystem creates FileSystem if it doesn't exist and returns its ready status
func (r *FileSystemClaimReconciler) ensureFileSystem(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	logger := log.FromContext(ctx)
	logger.Info("ensureFileSystem", "name", fsc.Name)

	if !r.isConditionTrue(fsc, ConditionTypeLocalDiskCreated) {
		return false, nil
	}

	ldList := &unstructured.UnstructuredList{}
	ldList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   LocalDiskGroup,
		Version: LocalDiskVersion,
		Kind:    LocalDiskList,
	})
	if err := r.List(ctx, ldList, client.InNamespace(fsc.Namespace)); err != nil {
		return false, fmt.Errorf("list LocalDisks: %w", err)
	}

	var ldNames []string
	for _, it := range ldList.Items {
		if isOwnedByThisFSC(&it, fsc.Name) {
			ldNames = append(ldNames, it.GetName())
		}
	}
	if len(ldNames) == 0 {
		logger.Info("ensureFileSystem: no owned LocalDisks found despite LocalDiskCreated=True; skipping")
		return false, nil
	}

	toIface := func(ss []string) []interface{} {
		out := make([]interface{}, len(ss))
		for i, s := range ss {
			out[i] = s
		}
		return out
	}
	desiredSpec := map[string]interface{}{
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

	fsList := &unstructured.UnstructuredList{}
	fsList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   FileSystemGroup,
		Version: FileSystemVersion,
		Kind:    "FilesystemList",
	})
	if err := r.List(ctx, fsList, client.InNamespace(fsc.Namespace)); err != nil {
		return false, fmt.Errorf("list Filesystems: %w", err)
	}

	var owned []*unstructured.Unstructured
	for i := range fsList.Items {
		if isOwnedByThisFSC(&fsList.Items[i], fsc.Name) {
			obj := fsList.Items[i]
			owned = append(owned, &obj)
		}
	}

	switch len(owned) {
	case 0:
		fsName := fmt.Sprintf("%s-fs", fsc.Name)

		fs := &unstructured.Unstructured{}
		fs.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   FileSystemGroup,
			Version: FileSystemVersion,
			Kind:    FileSystemKind,
		})
		fs.SetNamespace(fsc.Namespace)
		fs.SetName(fsName)

		if err := controllerutil.SetOwnerReference(fsc, fs, r.Scheme); err != nil {
			return false, fmt.Errorf("set ownerRef: %w", err)
		}
		if fs.Object == nil {
			fs.Object = map[string]interface{}{}
		}
		fs.Object["spec"] = desiredSpec

		logger.Info("Creating Filesystem", "name", fsName, "disks", ldNames)
		if err := r.Create(ctx, fs); err != nil {
			if e := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
				cur.Status.Conditions = utils.UpdateCondition(
					cur.Status.Conditions,
					ConditionTypeFileSystemCreated,
					metav1.ConditionFalse,
					ReasonFileSystemCreationFailed,
					err.Error(),
					cur.Generation,
				)
			}); e != nil {
				return false, e
			}
			return true, nil
		}

		if e := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
			cur.Status.Conditions = utils.UpdateCondition(
				cur.Status.Conditions,
				ConditionTypeFileSystemCreated,
				metav1.ConditionFalse,
				ReasonFileSystemCreationInProgress,
				"Filesystem created; waiting to become Ready",
				cur.Generation,
			)
		}); e != nil {
			return false, e
		}
		return true, nil

	case 1:
		fs := owned[0]
		orig := fs.DeepCopy()

		equalStringSet := func(a, b []interface{}) bool {
			if len(a) != len(b) {
				return false
			}
			m := map[string]int{}
			for _, v := range a {
				m[fmt.Sprint(v)]++
			}
			for _, v := range b {
				k := fmt.Sprint(v)
				if m[k] == 0 {
					return false
				}
				m[k]--
			}
			for _, n := range m {
				if n != 0 {
					return false
				}
			}
			return true
		}

		curBlock, _, _ := unstructured.NestedString(fs.Object, "spec", "local", "blockSize")
		curRep, _, _ := unstructured.NestedString(fs.Object, "spec", "local", "replication")
		curType, _, _ := unstructured.NestedString(fs.Object, "spec", "local", "type")
		curPools, _, _ := unstructured.NestedSlice(fs.Object, "spec", "local", "pools")
		var curDisks []interface{}
		var curPoolName string
		if len(curPools) > 0 {
			if pm, ok := curPools[0].(map[string]interface{}); ok {
				curDisks, _, _ = unstructured.NestedSlice(pm, "disks")
				curPoolName, _, _ = unstructured.NestedString(pm, "name")
			}
		}
		curSELvl, _, _ := unstructured.NestedString(fs.Object, "spec", "seLinuxOptions", "level")
		curSERole, _, _ := unstructured.NestedString(fs.Object, "spec", "seLinuxOptions", "role")
		curSEType, _, _ := unstructured.NestedString(fs.Object, "spec", "seLinuxOptions", "type")
		curSEUser, _, _ := unstructured.NestedString(fs.Object, "spec", "seLinuxOptions", "user")

		wantBlock := "4M"
		wantRep := "1-way"
		wantType := "shared"
		wantPoolName := "system"
		wantDisks := toIface(ldNames)
		wantSELvl, wantSERole, wantSEType, wantSEUser := "s0", "object_r", "container_file_t", "system_u"

		changed := false
		if curBlock != wantBlock {
			_ = unstructured.SetNestedField(fs.Object, wantBlock, "spec", "local", "blockSize")
			changed = true
		}
		if curRep != wantRep {
			_ = unstructured.SetNestedField(fs.Object, wantRep, "spec", "local", "replication")
			changed = true
		}
		if curType != wantType {
			_ = unstructured.SetNestedField(fs.Object, wantType, "spec", "local", "type")
			changed = true
		}
		if curPoolName != wantPoolName {
			pool0 := map[string]interface{}{"name": wantPoolName, "disks": wantDisks}
			_ = unstructured.SetNestedSlice(fs.Object, []interface{}{pool0}, "spec", "local", "pools")
			changed = true
		} else if !equalStringSet(curDisks, wantDisks) {
			if len(curPools) == 0 {
				curPools = []interface{}{map[string]interface{}{}}
			}
			pm, _ := curPools[0].(map[string]interface{})
			pm["name"], pm["disks"] = wantPoolName, wantDisks
			curPools[0] = pm
			_ = unstructured.SetNestedSlice(fs.Object, curPools, "spec", "local", "pools")
			changed = true
		}
		if curSELvl != wantSELvl {
			_ = unstructured.SetNestedField(fs.Object, wantSELvl, "spec", "seLinuxOptions", "level")
			changed = true
		}
		if curSERole != wantSERole {
			_ = unstructured.SetNestedField(fs.Object, wantSERole, "spec", "seLinuxOptions", "role")
			changed = true
		}
		if curSEType != wantSEType {
			_ = unstructured.SetNestedField(fs.Object, wantSEType, "spec", "seLinuxOptions", "type")
			changed = true
		}
		if curSEUser != wantSEUser {
			_ = unstructured.SetNestedField(fs.Object, wantSEUser, "spec", "seLinuxOptions", "user")
			changed = true
		}

		if changed {
			if err := r.Patch(ctx, fs, client.MergeFrom(orig)); err != nil {
				return false, fmt.Errorf("patch Filesystem: %w", err)
			}
			return true, nil
		}
		return false, nil

	default:
		msg := fmt.Sprintf("found %d Filesystems owned by FSC; expected 1", len(owned))
		if e := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
			cur.Status.Conditions = utils.UpdateCondition(
				cur.Status.Conditions,
				ConditionTypeFileSystemCreated,
				metav1.ConditionFalse,
				ReasonFileSystemCreationFailed,
				msg,
				cur.Generation,
			)
		}); e != nil {
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

	fsList := &unstructured.UnstructuredList{}
	fsList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   FileSystemGroup,
		Version: FileSystemVersion,
		Kind:    FileSystemList,
	})
	if err := r.List(ctx, fsList, client.InNamespace(fsc.Namespace)); err != nil {
		return false, fmt.Errorf("list Filesystems: %w", err)
	}

	var owned []unstructured.Unstructured
	for _, it := range fsList.Items {
		if isOwnedByThisFSC(&it, fsc.Name) {
			owned = append(owned, it)
		}
	}

	var desiredStatus metav1.ConditionStatus
	var desiredReason, desiredMsg string

	if len(owned) == 0 {
		desiredStatus = metav1.ConditionFalse
		desiredReason = ReasonFileSystemCreationInProgress
		desiredMsg = "Waiting for Filesystem to be created"
	} else {
		allGood := true
		var failingName, failingMsg string

		for _, fs := range owned {
			sl, found, _ := unstructured.NestedSlice(fs.Object, "status", "conditions")
			if !found {
				allGood = false
				failingName, failingMsg = fs.GetName(), "status.conditions missing"
				break
			}
			conds := asMetaConditions(sl)

			if !apimeta.IsStatusConditionTrue(conds, "Success") {
				allGood = false
				if c := apimeta.FindStatusCondition(conds, "Success"); c != nil {
					failingMsg = c.Message
				} else {
					failingMsg = "Success condition not found"
				}
				failingName = fs.GetName()
				break
			}
			if !apimeta.IsStatusConditionTrue(conds, "Healthy") {
				allGood = false
				if c := apimeta.FindStatusCondition(conds, "Healthy"); c != nil {
					failingMsg = c.Message
				} else {
					failingMsg = "Healthy condition not found"
				}
				failingName = fs.GetName()
				break
			}
		}

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

	// Idempotency guard: only patch if changed
	prev := apimeta.FindStatusCondition(fsc.Status.Conditions, ConditionTypeFileSystemCreated)
	if prev != nil && prev.Status == desiredStatus && prev.Reason == desiredReason && prev.Message == desiredMsg {
		logger.Info("FilesystemCreated condition unchanged; skipping patch")
		return false, nil
	}

	if err := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
		cur.Status.Conditions = utils.UpdateCondition(
			cur.Status.Conditions,
			ConditionTypeFileSystemCreated,
			desiredStatus,
			desiredReason,
			desiredMsg,
			cur.Generation,
		)
	}); err != nil {
		return false, err
	}

	logger.Info("synced Filesystem conditions", "fsc", fsc.Name, "owned", len(owned), "status", string(desiredStatus))
	return true, nil
}

// // ensureStorageClass creates StorageClass if it doesn't exist and returns its ready status
func (r *FileSystemClaimReconciler) ensureStorageClass(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	logger := log.FromContext(ctx)

	// Gate on Filesystem being ready
	if !r.isConditionTrue(fsc, ConditionTypeFileSystemCreated) {
		return false, nil
	}

	scName := fmt.Sprintf("%s-sc", fsc.Name) // e.g., "new-sc"
	fsName := fmt.Sprintf("%s-fs", fsc.Name) // the Filesystem name we created

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
			// mark SC create failed
			if e := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
				cur.Status.Conditions = utils.UpdateCondition(
					cur.Status.Conditions,
					ConditionTypeStorageClassCreated,
					metav1.ConditionFalse,
					ReasonStorageClassCreationFailed,
					err.Error(),
					cur.Generation,
				)
			}); e != nil {
				return false, e
			}
			return true, nil
		}

		// mark SC created (idempotent guard)
		prev := apimeta.FindStatusCondition(fsc.Status.Conditions, ConditionTypeStorageClassCreated)
		if prev == nil || prev.Status != metav1.ConditionTrue || prev.Reason != ReasonStorageClassCreationSucceeded {
			if err := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
				cur.Status.Conditions = utils.UpdateCondition(
					cur.Status.Conditions,
					ConditionTypeStorageClassCreated,
					metav1.ConditionTrue,
					ReasonStorageClassCreationSucceeded,
					"StorageClass created",
					cur.Generation,
				)
			}); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, nil

	case err != nil:
		return false, fmt.Errorf("get StorageClass %q: %w", scName, err)

	default:
		// Drift correction to match the template
		orig := current.DeepCopy()
		changed := false

		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		if current.Annotations["storageclass.kubevirt.io/is-default-virt-class"] != "true" {
			current.Annotations["storageclass.kubevirt.io/is-default-virt-class"] = "true"
			changed = true
		}

		if current.Labels == nil {
			current.Labels = map[string]string{}
		}
		if current.Labels["fusion.storage.openshift.io/owned-by-name"] != fsc.Name {
			current.Labels["fusion.storage.openshift.io/owned-by-name"] = fsc.Name
			changed = true
		}
		if current.Labels["fusion.storage.openshift.io/owned-by-namespace"] != fsc.Namespace {
			current.Labels["fusion.storage.openshift.io/owned-by-namespace"] = fsc.Namespace
			changed = true
		}

		if current.Provisioner != desired.Provisioner {
			current.Provisioner = desired.Provisioner
			changed = true
		}
		if current.AllowVolumeExpansion == nil || *current.AllowVolumeExpansion != *desired.AllowVolumeExpansion {
			current.AllowVolumeExpansion = desired.AllowVolumeExpansion
			changed = true
		}
		if current.ReclaimPolicy == nil || *current.ReclaimPolicy != *desired.ReclaimPolicy {
			current.ReclaimPolicy = desired.ReclaimPolicy
			changed = true
		}
		if current.VolumeBindingMode == nil || *current.VolumeBindingMode != *desired.VolumeBindingMode {
			current.VolumeBindingMode = desired.VolumeBindingMode
			changed = true
		}
		if !reflect.DeepEqual(current.Parameters, desired.Parameters) {
			current.Parameters = desired.Parameters
			changed = true
		}

		if changed {
			if err := r.Patch(ctx, current, client.MergeFrom(orig)); err != nil {
				return false, fmt.Errorf("patch StorageClass %q: %w", scName, err)
			}
		}

		// Ensure condition is True (idempotent)
		prev := apimeta.FindStatusCondition(fsc.Status.Conditions, ConditionTypeStorageClassCreated)
		if prev == nil || prev.Status != metav1.ConditionTrue || prev.Reason != ReasonStorageClassCreationSucceeded {
			if err := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
				cur.Status.Conditions = utils.UpdateCondition(
					cur.Status.Conditions,
					ConditionTypeStorageClassCreated,
					metav1.ConditionTrue,
					ReasonStorageClassCreationSucceeded,
					"StorageClass present",
					cur.Generation,
				)
			}); err != nil {
				return false, err
			}
			return true, nil
		}
		return changed, nil
	}
}

// syncFSCReady aggregates the overall Ready condition from the sub-conditions.
func (r *FileSystemClaimReconciler) syncFSCReady(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim) (bool, error) {
	readyNow :=
		r.isConditionTrue(fsc, ConditionTypeDeviceValidated) &&
			r.isConditionTrue(fsc, ConditionTypeLocalDiskCreated) &&
			r.isConditionTrue(fsc, ConditionTypeFileSystemCreated) &&
			r.isConditionTrue(fsc, ConditionTypeStorageClassCreated)

	var desired metav1.ConditionStatus
	var reason, msg string

	if readyNow {
		desired = metav1.ConditionTrue
		reason = ReasonProvisioningSucceeded
		msg = "All resources created and ready"
	} else {
		desired = metav1.ConditionFalse
		reason = ReasonProvisioningInProgress
		msg = "Provisioning in progress"
	}

	prev := metav1.ConditionStatus(metav1.ConditionUnknown)
	for _, c := range fsc.Status.Conditions {
		if c.Type == ConditionTypeReady {
			prev = c.Status
			break
		}
	}
	if prev == desired {
		return false, nil
	}

	if err := r.patchFSCStatus(ctx, fsc, func(cur *fusionv1alpha1.FileSystemClaim) {
		cur.Status.Conditions = utils.UpdateCondition(
			cur.Status.Conditions,
			ConditionTypeReady,
			desired,
			reason,
			msg,
			cur.Generation,
		)
	}); err != nil {
		return false, err
	}
	return true, nil
}

// Handlers for FileSystemClaim reconciliation -- END

// Helper functions -- START

// helper function to get a nested string from a map[string]interface{}
func getNestedString(obj map[string]interface{}, fields ...string) string {
	cur := obj
	for i := 0; i < len(fields)-1; i++ {
		m, _ := cur[fields[i]].(map[string]interface{})
		cur = m
		if cur == nil {
			return ""
		}
	}
	if v, ok := cur[fields[len(fields)-1]].(string); ok {
		return v
	}
	return ""
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
func (r *FileSystemClaimReconciler) patchFSC(ctx context.Context, fsc *fusionv1alpha1.FileSystemClaim, mutate func(*fusionv1alpha1.FileSystemClaim)) error {
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

// Helper functions -- END

// Handlers for watched resources -- START

func enqueueFSCByOwner() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
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
			enqueueFSCByOwner(),
			didFileSystemStatusChange(),
			builder.OnlyMetadata,
		).
		Named("filesystemclaim").
		Complete(r)
}
