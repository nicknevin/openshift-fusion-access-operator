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
	fusionv1alpha1 "github.com/openshift-storage-scale/openshift-fusion-access-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// createStorageNode creates a Node with worker and storage labels for testing
func createStorageNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				WorkerNodeRoleLabel:   "",
				ScaleStorageRoleLabel: ScaleStorageRoleValue,
			},
		},
	}
}

// createLVDR creates a LocalVolumeDiscoveryResult for testing
func createLVDR(nodeName, namespace string, devices []fusionv1alpha1.DiscoveredDevice) *fusionv1alpha1.LocalVolumeDiscoveryResult {
	return &fusionv1alpha1.LocalVolumeDiscoveryResult{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "discovery-result-" + nodeName,
			Namespace: namespace,
		},
		Spec: fusionv1alpha1.LocalVolumeDiscoveryResultSpec{
			NodeName: nodeName,
		},
		Status: fusionv1alpha1.LocalVolumeDiscoveryResultStatus{
			DiscoveredDevices: devices,
		},
	}
}

// createTestFSC creates a FileSystemClaim for testing with specified conditions
func createTestFSC(name, namespace string, devices []string, conditions []metav1.Condition) *fusionv1alpha1.FileSystemClaim {
	return &fusionv1alpha1.FileSystemClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: fusionv1alpha1.FileSystemClaimSpec{
			Devices: devices,
		},
		Status: fusionv1alpha1.FileSystemClaimStatus{
			Conditions: conditions,
		},
	}
}

// deviceValidatedCondition returns a DeviceValidated condition
func deviceValidatedCondition(status metav1.ConditionStatus) metav1.Condition {
	reason := ReasonDeviceValidationSucceeded
	if status == metav1.ConditionFalse {
		reason = ReasonDeviceValidationFailed
	}
	return metav1.Condition{
		Type:   ConditionTypeDeviceValidated,
		Status: status,
		Reason: reason,
	}
}

// localDiskCreatedCondition returns a LocalDiskCreated condition
func localDiskCreatedCondition(status metav1.ConditionStatus, reason string) metav1.Condition {
	return metav1.Condition{
		Type:   ConditionTypeLocalDiskCreated,
		Status: status,
		Reason: reason,
	}
}

// filesystemCreatedCondition returns a FileSystemCreated condition
func filesystemCreatedCondition(status metav1.ConditionStatus, reason string) metav1.Condition {
	return metav1.Condition{
		Type:   ConditionTypeFileSystemCreated,
		Status: status,
		Reason: reason,
	}
}

// storageClassCreatedCondition returns a StorageClassCreated condition
func storageClassCreatedCondition(status metav1.ConditionStatus, reason string) metav1.Condition {
	return metav1.Condition{
		Type:   ConditionTypeStorageClassCreated,
		Status: status,
		Reason: reason,
	}
}

// findCondition finds a condition by type (shared across test files)
func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}
