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
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	kubeclient "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fusionv1alpha "github.com/openshift-storage-scale/openshift-fusion-access-operator/api/v1alpha1"
	"github.com/openshift-storage-scale/openshift-fusion-access-operator/internal/controller/kernelmodule"
)

const (
	resourceName   = "test-resource"
	oscinitVersion = "v4.17.0"
)

var _ = Describe("FusionAccess Controller", func() {
	var (
		fakeClientBuilder *fake.ClientBuilder
		scheme            = createFakeScheme()
		namespace         = newNamespace("ibm-fusion-access-operator")
		version           = newOCPVersion(oscinitVersion)
		clusterConsole    = &operatorv1.Console{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}
		clusterPullSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pull-secret",
				Namespace: "openshift-config",
			},
			Data: map[string][]byte{".dockerconfigjson": []byte(`
					{
		  				"auths": {
							"quay.io/repo1": {
								"auth": "authkey",
								"email": ""
		    				},
							"quay.io/repo2": {
								"auth": "authkey",
								"email": ""
		    				}
						}
					}`)}}
		testTimeout = 5 * time.Second
	)

	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}

		BeforeEach(func() {
			By("creating the custom resource for the Kind FusionAccess")
			os.Setenv("DEPLOYMENT_NAMESPACE", "ibm-fusion-access-operator")
			fakeClientBuilder = fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(version, namespace, clusterConsole, clusterPullSecret).
				WithStatusSubresource(&fusionv1alpha.FusionAccess{})

		})

		AfterEach(func() {
			os.Unsetenv("DEPLOYMENT_NAMESPACE")
		})

		It("should successfully reconcile the resource", func() {
			resource := &fusionv1alpha.FusionAccess{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: fusionv1alpha.FusionAccessSpec{
					StorageScaleVersion:  "v5.2.3.1",
					LocalVolumeDiscovery: fusionv1alpha.StorageDeviceDiscovery{
						// Create: false,
					},
				},
			}
			k8sClient = fakeClientBuilder.WithRuntimeObjects(resource).Build()
			Expect(k8sClient).NotTo(BeNil())

			By("Reconciling the custom resource created")
			FusionAccessReconciler := &FusionAccessReconciler{
				Client:     k8sClient,
				Scheme:     k8sClient.Scheme(),
				fullClient: kubeclient.NewSimpleClientset(),
				CanPullImage: func(ctx context.Context, client kubernetes.Interface, ns, image, pullSecret string) (bool, error) {
					return true, nil
				},
			}

			_, err := FusionAccessReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).ToNot(HaveOccurred())
			updated := &fusionv1alpha.FusionAccess{}
			err = k8sClient.Get(ctx, typeNamespacedName, updated)
			Expect(err).ToNot(HaveOccurred())
		})
	})
})

var _ = Describe("FusionAccessReconciler Setup", func() {
	var (
		k8sMgr     manager.Manager
		reconciler *FusionAccessReconciler
		scheme     = createFakeScheme()
	)

	BeforeEach(func(ctx context.Context) {
		var err error

		scheme = createFakeScheme()
		_ = clientgoscheme.AddToScheme(scheme)
		_ = fusionv1alpha.AddToScheme(scheme)

		k8sMgr, err = manager.New(cfg, manager.Options{
			Scheme: scheme,
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("should initialize the reconciler without errors", func() {
		reconciler = &FusionAccessReconciler{}

		err := reconciler.SetupWithManager(k8sMgr)
		Expect(err).NotTo(HaveOccurred())

		Expect(reconciler.config).ToNot(BeNil())
		Expect(reconciler.dynamicClient).ToNot(BeNil())
		Expect(reconciler.fullClient).ToNot(BeNil())

	})
})

var _ = Describe("checkPullSecret", func() {
	const (
		expectedName      = "fusion-pullsecret"
		expectedNamespace = "default"
	)

	It("returns true for a valid pull secret", func() {
		secret := &corev1.Secret{
			Type: corev1.SecretTypeOpaque,
			ObjectMeta: metav1.ObjectMeta{
				Name:      expectedName,
				Namespace: expectedNamespace,
			},
		}

		Expect(checkPullSecret(secret, expectedNamespace)).To(BeTrue())
	})

	It("returns false if secret type is incorrect", func() {
		secret := &corev1.Secret{
			Type: corev1.SecretTypeDockercfg,
			ObjectMeta: metav1.ObjectMeta{
				Name:      expectedName,
				Namespace: expectedNamespace,
			},
		}

		Expect(checkPullSecret(secret, expectedNamespace)).To(BeFalse())
	})

	It("returns false if secret name is incorrect", func() {
		secret := &corev1.Secret{
			Type: corev1.SecretTypeOpaque,
			ObjectMeta: metav1.ObjectMeta{
				Name:      "wrong-name",
				Namespace: expectedNamespace,
			},
		}

		Expect(checkPullSecret(secret, expectedNamespace)).To(BeFalse())
	})

	It("returns false if secret namespace is incorrect", func() {
		secret := &corev1.Secret{
			Type: corev1.SecretTypeOpaque,
			ObjectMeta: metav1.ObjectMeta{
				Name:      expectedName,
				Namespace: "other-namespace",
			},
		}

		Expect(checkPullSecret(secret, expectedNamespace)).To(BeFalse())
	})
})

func newNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func newOCPVersion(version string) *configv1.ClusterVersion {
	return &configv1.ClusterVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "version"},
		Status: configv1.ClusterVersionStatus{
			History: []configv1.UpdateHistory{
				{State: configv1.CompletedUpdate,
					Version: version},
			},
		},
	}
}

var _ = Describe("FusionAccessReconciler Setup", func() {
})

// Replace these with your actual mocking setup
var _ = Describe("getIbmManifest", func() {
	Context("when ExternalManifestURL is valid", func() {
		It("should return the external URL if allowed", func() {
			fusionObj := fusionv1alpha.FusionAccessSpec{
				ExternalManifestURL: "https://raw.githubusercontent.com/openshift-storage-scale/openshift-fusion-access-manifests/refs/heads/main/manifests/5.2.3.1.dev2/install.yaml",
				StorageScaleVersion: "",
			}

			url, err := getIbmManifest(fusionObj)
			Expect(err).ToNot(HaveOccurred())
			Expect(url).To(Equal("https://raw.githubusercontent.com/openshift-storage-scale/openshift-fusion-access-manifests/refs/heads/main/manifests/5.2.3.1.dev2/install.yaml"))
		})

		It("should return an error if external URL is disallowed", func() {
			fusionObj := fusionv1alpha.FusionAccessSpec{
				ExternalManifestURL: "http://bad-url.com",
				StorageScaleVersion: "",
			}

			_, err := getIbmManifest(fusionObj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("disallowed URL"))
		})
	})

	Context("when IbmCnsaVersion is set and external URL is empty", func() {
		It("should return the install path", func() {
			fusionObj := fusionv1alpha.FusionAccessSpec{
				ExternalManifestURL: "",
				StorageScaleVersion: "v5.2.3.1",
			}

			path, err := getIbmManifest(fusionObj)
			Expect(err).ToNot(HaveOccurred())
			Expect(path).To(Equal("../../files/v5.2.3.1/install.yaml"))
		})
	})

	Context("when neither ExternalManifestURL nor StorageScaleVersion is set", func() {
		It("should return an error", func() {
			fusionObj := fusionv1alpha.FusionAccessSpec{}

			_, err := getIbmManifest(fusionObj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no Storage Scale manifest version"))
		})
	})
})

var _ = Describe("getCurrentRegistrySecretName", func() {
	var (
		origGetKMMImageConfig                func(context.Context, client.Client, string) (kernelmodule.KMMImageConfig, error)
		origGetServiceAccountDockercfgSecret func(context.Context, client.Client, string, string) (string, error)
		fakeClient                           client.Client
		ctx                                  = context.Background()
		ns                                   = "test-ns"
	)

	BeforeEach(func() {
		origGetKMMImageConfig = kernelmodule.GetKMMImageConfig
		origGetServiceAccountDockercfgSecret = kernelmodule.GetServiceAccountDockercfgSecretName
		fakeClient = fake.NewClientBuilder().Build()
	})

	AfterEach(func() {
		kernelmodule.GetKMMImageConfig = origGetKMMImageConfig
		kernelmodule.GetServiceAccountDockercfgSecretName = origGetServiceAccountDockercfgSecret
	})

	It("returns RegistrySecretName from KMMImageConfig if set", func() {
		kernelmodule.GetKMMImageConfig = func(_ context.Context, _ client.Client, _ string) (kernelmodule.KMMImageConfig, error) {
			return kernelmodule.KMMImageConfig{RegistrySecretName: "my-registry-secret"}, nil
		}
		kernelmodule.GetServiceAccountDockercfgSecretName = func(_ context.Context, _ client.Client, _, _ string) (string, error) {
			return "should-not-be-called", nil
		}
		name, err := getCurrentRegistrySecretName(ctx, fakeClient, ns)
		Expect(err).ToNot(HaveOccurred())
		Expect(name).To(Equal("my-registry-secret"))
	})

	It("falls back to GetServiceAccountDockercfgSecretName if RegistrySecretName is empty", func() {
		kernelmodule.GetKMMImageConfig = func(_ context.Context, _ client.Client, _ string) (kernelmodule.KMMImageConfig, error) {
			return kernelmodule.KMMImageConfig{RegistrySecretName: ""}, nil
		}
		kernelmodule.GetServiceAccountDockercfgSecretName = func(_ context.Context, _ client.Client, namespace, sa string) (string, error) {
			Expect(namespace).To(Equal(ns))
			Expect(sa).To(Equal("builder"))
			return "builder-dockercfg-secret", nil
		}
		name, err := getCurrentRegistrySecretName(ctx, fakeClient, ns)
		Expect(err).ToNot(HaveOccurred())
		Expect(name).To(Equal("builder-dockercfg-secret"))
	})

	It("returns error if GetKMMImageConfig fails", func() {
		kernelmodule.GetKMMImageConfig = func(_ context.Context, _ client.Client, _ string) (kernelmodule.KMMImageConfig, error) {
			return kernelmodule.KMMImageConfig{}, fmt.Errorf("configmap not found")
		}
		name, err := getCurrentRegistrySecretName(ctx, fakeClient, ns)
		Expect(err).To(HaveOccurred())
		Expect(name).To(BeEmpty())
		Expect(err.Error()).To(ContainSubstring("failed to get KMMImageConfigmap"))
	})

	It("returns error if GetServiceAccountDockercfgSecretName fails", func() {
		kernelmodule.GetKMMImageConfig = func(_ context.Context, _ client.Client, _ string) (kernelmodule.KMMImageConfig, error) {
			return kernelmodule.KMMImageConfig{RegistrySecretName: ""}, nil
		}
		kernelmodule.GetServiceAccountDockercfgSecretName = func(_ context.Context, _ client.Client, _, _ string) (string, error) {
			return "", fmt.Errorf("dockercfg secret not found")
		}
		name, err := getCurrentRegistrySecretName(ctx, fakeClient, ns)
		Expect(err).To(HaveOccurred())
		Expect(name).To(BeEmpty())
		Expect(err.Error()).To(ContainSubstring("dockercfg secret not found"))
	})
})
