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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFileSystemClaimHelpers(t *testing.T) {
	RegisterFailHandler(Fail)
	// This test is part of the main Controller Suite, no need for separate RunSpecs
}

var _ = Describe("FileSystemClaim Helper Functions", func() {

	Describe("generateLocalDiskName", func() {
		Context("with valid device paths and WWNs", func() {
			It("should generate correct names for nvme devices", func() {
				devicePath := "/dev/nvme1n1"
				wwn := "uuid.f18cb32d-1087-55a1-b9bc-4b4d12bcdbf4"

				result, err := generateLocalDiskName(devicePath, wwn)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("nvme1n1-f18cb32d-1087-55a1-b9bc-4b4d12bcdbf4"))
			})

			It("should handle WWN with 0x prefix", func() {
				devicePath := "/dev/nvme1n1"
				wwn := "0x5002538e00000001"

				result, err := generateLocalDiskName(devicePath, wwn)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("nvme1n1-5002538e00000001"))
			})

			It("should handle WWN with uuid. prefix", func() {
				devicePath := "/dev/nvme1n1"
				wwn := "uuid.test-wwn-123"

				result, err := generateLocalDiskName(devicePath, wwn)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("nvme1n1-test-wwn-123"))
			})

			It("should handle sda devices", func() {
				devicePath := "/dev/sda"
				wwn := "uuid.sda-wwn-456"

				result, err := generateLocalDiskName(devicePath, wwn)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("sda-sda-wwn-456"))
			})
		})

		Context("with invalid inputs", func() {
			It("should return error for empty device path", func() {
				_, err := generateLocalDiskName("", "uuid.test")
				Expect(err).To(HaveOccurred())
			})

			It("should return error for empty WWN", func() {
				_, err := generateLocalDiskName("/dev/nvme1n1", "")
				Expect(err).To(HaveOccurred())
			})
		})
	})

	Describe("isValidKubernetesName", func() {
		Context("with valid names", func() {
			It("should accept valid names", func() {
				Expect(isValidKubernetesName("valid-name")).To(BeTrue())
				Expect(isValidKubernetesName("validname")).To(BeTrue())
				Expect(isValidKubernetesName("valid-name-123")).To(BeTrue())
				Expect(isValidKubernetesName("valid.name")).To(BeTrue())
				Expect(isValidKubernetesName("VALID")).To(BeTrue())
				Expect(isValidKubernetesName("a")).To(BeTrue())
			})
		})

		Context("with invalid names", func() {
			It("should reject invalid names", func() {
				Expect(isValidKubernetesName("")).To(BeFalse())
				Expect(isValidKubernetesName("-invalid")).To(BeFalse())
				Expect(isValidKubernetesName("invalid-")).To(BeFalse())
				Expect(isValidKubernetesName("invalid_name")).To(BeFalse())
				Expect(isValidKubernetesName("invalid@name")).To(BeFalse())
			})
		})
	})

	Describe("buildFilesystemSpec", func() {
		Context("with valid disk names", func() {
			It("should build correct spec for single disk", func() {
				ldNames := []string{"nvme1n1-test-wwn-123"}

				spec := buildFilesystemSpec(ldNames)

				Expect(spec).To(HaveKey("local"))
				local := spec["local"].(map[string]interface{})

				Expect(local).To(HaveKey("blockSize"))
				Expect(local["blockSize"]).To(Equal("4M"))

				Expect(local).To(HaveKey("replication"))
				Expect(local["replication"]).To(Equal("1-way"))

				Expect(local).To(HaveKey("type"))
				Expect(local["type"]).To(Equal("shared"))

				Expect(local).To(HaveKey("pools"))
				pools := local["pools"].([]interface{})
				Expect(pools).To(HaveLen(1))

				pool := pools[0].(map[string]interface{})
				Expect(pool["name"]).To(Equal("system"))
				Expect(pool["disks"]).To(Equal([]interface{}{"nvme1n1-test-wwn-123"}))
			})

			It("should build correct spec for multiple disks", func() {
				ldNames := []string{"nvme1n1-wwn1", "nvme1n2-wwn2", "sda-wwn3"}

				spec := buildFilesystemSpec(ldNames)

				local := spec["local"].(map[string]interface{})
				pools := local["pools"].([]interface{})
				pool := pools[0].(map[string]interface{})

				expectedDisks := []interface{}{"nvme1n1-wwn1", "nvme1n2-wwn2", "sda-wwn3"}
				Expect(pool["disks"]).To(Equal(expectedDisks))
			})

			It("should include seLinuxOptions", func() {
				ldNames := []string{"nvme1n1-test-wwn-123"}

				spec := buildFilesystemSpec(ldNames)

				Expect(spec).To(HaveKey("seLinuxOptions"))
				seLinux := spec["seLinuxOptions"].(map[string]interface{})

				Expect(seLinux["level"]).To(Equal("s0"))
				Expect(seLinux["role"]).To(Equal("object_r"))
				Expect(seLinux["type"]).To(Equal("container_file_t"))
				Expect(seLinux["user"]).To(Equal("system_u"))
			})
		})
	})

	Describe("asMetaConditions", func() {
		Context("with valid condition data", func() {
			It("should convert unstructured conditions to metav1.Condition", func() {
				conditions := []interface{}{
					map[string]interface{}{
						"type":               "Ready",
						"status":             "True",
						"reason":             "TestReason",
						"message":            "Test message",
						"lastTransitionTime": "2023-01-01T00:00:00Z",
					},
					map[string]interface{}{
						"type":               "Available",
						"status":             "False",
						"reason":             "TestReason2",
						"message":            "Test message 2",
						"lastTransitionTime": "2023-01-02T00:00:00Z",
					},
				}

				result := asMetaConditions(conditions)

				Expect(result).To(HaveLen(2))

				Expect(result[0].Type).To(Equal("Ready"))
				Expect(result[0].Status).To(Equal(metav1.ConditionTrue))
				Expect(result[0].Reason).To(Equal("TestReason"))
				Expect(result[0].Message).To(Equal("Test message"))

				Expect(result[1].Type).To(Equal("Available"))
				Expect(result[1].Status).To(Equal(metav1.ConditionFalse))
				Expect(result[1].Reason).To(Equal("TestReason2"))
				Expect(result[1].Message).To(Equal("Test message 2"))
			})

			It("should handle empty conditions", func() {
				result := asMetaConditions([]interface{}{})
				Expect(result).To(HaveLen(0))
			})

			It("should skip invalid condition entries", func() {
				conditions := []interface{}{
					map[string]interface{}{
						"type":   "Valid",
						"status": "True",
					},
					"invalid-string",
					map[string]interface{}{
						"type":   "AnotherValid",
						"status": "False",
					},
				}

				result := asMetaConditions(conditions)
				Expect(result).To(HaveLen(2))
				Expect(result[0].Type).To(Equal("Valid"))
				Expect(result[1].Type).To(Equal("AnotherValid"))
			})
		})
	})

	Describe("isOwnedByThisFSC", func() {
		Context("with valid owner references", func() {
			It("should return true for matching FSC owner", func() {
				obj := &unstructured.Unstructured{}
				obj.SetOwnerReferences([]metav1.OwnerReference{
					{
						APIVersion: "fusion.storage.openshift.io/v1alpha1",
						Kind:       "FileSystemClaim",
						Name:       "test-fsc",
					},
				})

				result := isOwnedByThisFSC(obj, "test-fsc")
				Expect(result).To(BeTrue())
			})

			It("should return false for non-matching FSC owner", func() {
				obj := &unstructured.Unstructured{}
				obj.SetOwnerReferences([]metav1.OwnerReference{
					{
						APIVersion: "fusion.storage.openshift.io/v1alpha1",
						Kind:       "FileSystemClaim",
						Name:       "other-fsc",
					},
				})

				result := isOwnedByThisFSC(obj, "test-fsc")
				Expect(result).To(BeFalse())
			})

			It("should return false for non-FSC owner", func() {
				obj := &unstructured.Unstructured{}
				obj.SetOwnerReferences([]metav1.OwnerReference{
					{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "test-deployment",
					},
				})

				result := isOwnedByThisFSC(obj, "test-fsc")
				Expect(result).To(BeFalse())
			})

			It("should return false for no owner references", func() {
				obj := &unstructured.Unstructured{}
				obj.SetOwnerReferences([]metav1.OwnerReference{})

				result := isOwnedByThisFSC(obj, "test-fsc")
				Expect(result).To(BeFalse())
			})
		})
	})
})
