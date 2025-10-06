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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LocalDisk represents a local disk specification for the file system claim
type LocalDisk struct {
	// Device represents the persistent name of the device. For eg, /dev/sda
	Device string `json:"device"`
	// Node represents the node where this disk is located
	Node string `json:"node"`
}

// FileSystemClaimSpec defines the desired state of FileSystemClaim.
type FileSystemClaimSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// LocalDisks is a list of local disks to be used for the file system
	LocalDisks []LocalDisk `json:"localdisks,omitempty"`
}

// FileSystemClaimStatus defines the observed state of FileSystemClaim.
type FileSystemClaimStatus struct {
    // Overall conditions
    Conditions []metav1.Condition `json:"conditions,omitempty"`
    
    // Individual LocalDisk statuses
    LocalDisks map[string][]metav1.Condition `json:"localDisks,omitempty"`
    FileSystem []metav1.Condition `json:"fileSystem,omitempty"`

}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// FileSystemClaim is the Schema for the filesystemclaims API.
type FileSystemClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FileSystemClaimSpec   `json:"spec,omitempty"`
	Status FileSystemClaimStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FileSystemClaimList contains a list of FileSystemClaim.
type FileSystemClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FileSystemClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FileSystemClaim{}, &FileSystemClaimList{})
}
