/*
Copyright 2022.

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

package console

import (
	"context"
	"fmt"
	"slices"

	"github.com/openshift-storage-scale/openshift-fusion-access-operator/internal/utils"

	operatorv1 "github.com/openshift/api/operator/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	consolev1 "github.com/openshift/api/console/v1"
)

const (
	// PluginName is the name of the plugin and used at several places
	// this has to be the same as in the package.json in the plugin
	PluginName = "fusion-access-console"
	// ServiceName is the name of the console plugin Service and must match the name of the Service in /bundle/manifests!
	ServiceName = "fusion-access-operator-console-plugin"
	// ServicePort is the port of the console plugin Service and must match the port of the Service in /bundle/manifests!
	ServicePort = 9443
)

// +kubebuilder:rbac:groups=console.openshift.io,resources=consoleplugins,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=operator.openshift.io,resources=consoles,verbs=get;list;watch;update

// CreateOrUpdatePlugin creates or updates the resources needed for the remediation console plugin.
// HEADS UP: consider cleanup of old resources in case of name changes or removals!
func CreateOrUpdatePlugin(ctx context.Context, cl client.Client) error {
	// Create ConsolePlugin resource
	// Deployment and Service are deployed by OLM
	ns, err := utils.GetDeploymentNamespace()
	if err != nil {
		return err
	}
	if err := createOrUpdateConsolePlugin(ctx, ns, cl); err != nil {
		return err
	}

	return nil
}

func createOrUpdateConsolePlugin(ctx context.Context, namespace string, cl client.Client) error {
	cp := newConsolePlugin(namespace)
	oldCP := &consolev1.ConsolePlugin{}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(cp), oldCP); apierrors.IsNotFound(err) {
		if err := cl.Create(ctx, cp); err != nil {
			return fmt.Errorf("could not create console plugin: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("could not check for existing console plugin: %w", err)
	} else {
		oldCP.OwnerReferences = cp.OwnerReferences
		oldCP.Spec = cp.Spec
		if err := cl.Update(ctx, oldCP); err != nil {
			return fmt.Errorf("could not update console plugin: %w", err)
		}
	}
	return nil
}

func newConsolePlugin(namespace string) *consolev1.ConsolePlugin {
	return &consolev1.ConsolePlugin{
		ObjectMeta: metav1.ObjectMeta{
			Name: PluginName,
			// TODO set owner ref for deletion when operator is uninstalled
			// but which resource to use, needs to be cluster scoped
		},
		Spec: consolev1.ConsolePluginSpec{
			DisplayName: "Fusion Access for SAN plugin",
			Backend: consolev1.ConsolePluginBackend{
				Type: consolev1.Service,
				Service: &consolev1.ConsolePluginService{
					Name:      ServiceName,
					Namespace: namespace,
					Port:      ServicePort,
					BasePath:  "/",
				},
			},
		},
	}
}

func EnablePlugin(ctx context.Context, cl client.Client) error {
	consoleKey := client.ObjectKey{Namespace: "", Name: "cluster"}
	consoleObj := &operatorv1.Console{}
	if err := cl.Get(ctx, consoleKey, consoleObj); err != nil {
		return fmt.Errorf("could not find resource - APIVersion: %s, Kind: %s, Name: %s: %w",
			consoleObj.APIVersion, consoleObj.Kind, consoleObj.Name, err)
	}

	if !slices.Contains(consoleObj.Spec.Plugins, PluginName) {
		consoleObj.Spec.Plugins = append(consoleObj.Spec.Plugins, PluginName)
		err := cl.Update(ctx, consoleObj)
		if err != nil {
			return fmt.Errorf("could not update resource - APIVersion: %s, Kind: %s, Name: %s: %w",
				consoleObj.APIVersion, consoleObj.Kind, consoleObj.Name, err)
		}
	}
	return nil
}
