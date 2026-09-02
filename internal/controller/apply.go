/*
Copyright 2026.

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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// fieldOwner identifies this operator in server-side apply managed fields.
const fieldOwner = client.FieldOwner("dwpk-controller")

// applyOwned server-side applies one desired object as a child of owner, which
// both creates it and corrects drift without a read-modify-write cycle. The
// ownerReference is what makes deletion garbage-collect the children.
func applyOwned(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	owner, desired client.Object,
) error {
	kind := desired.GetObjectKind().GroupVersionKind().Kind
	if err := controllerutil.SetControllerReference(owner, desired, scheme); err != nil {
		return fmt.Errorf("set owner on %s %s: %w", kind, desired.GetName(), err)
	}
	// The typed builders stay typed because they are easier to read and test;
	// Client.Apply only speaks ApplyConfiguration, so convert at the boundary.
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(desired)
	if err != nil {
		return fmt.Errorf("convert %s %s: %w", kind, desired.GetName(), err)
	}
	ac := client.ApplyConfigurationFromUnstructured(&unstructured.Unstructured{Object: raw})
	if err := c.Apply(ctx, ac, fieldOwner, client.ForceOwnership); err != nil {
		return fmt.Errorf("apply %s %s: %w", kind, desired.GetName(), err)
	}
	return nil
}
