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

package webhook

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

// quotaUsage totals what a namespace's workspaces are asking for.
//
// The two halves are counted differently on purpose. CPU, memory, GPU and the
// workspace count come from running workspaces only: a stopped workspace holds
// none of them. Storage comes from every workspace, because a stopped one still
// has its PVC and the cluster is still paying for it.
type quotaUsage struct {
	running int32
	cpu     resource.Quantity
	memory  resource.Quantity
	gpu     resource.Quantity
	storage resource.Quantity
}

func newQuotaUsage() *quotaUsage { return &quotaUsage{} }

// add folds one workspace into the totals.
//
// Requests, not limits. A limit is what a container may burst to; a request is
// what the scheduler actually reserves, and reserving is what a quota is for.
// It is also what the namespace ResourceQuota measures, so the number the user
// is shown here and the number that stops their pod agree.
func (u *quotaUsage) add(ws *dwpkv1alpha1.Workspace, gpuResource corev1.ResourceName) {
	if ws.Spec.Storage != nil {
		u.storage.Add(*ws.Spec.Storage)
	}
	if !ws.Spec.Running {
		return
	}

	u.running++
	requests := ws.Spec.Resources.Requests
	if cpu, ok := requests[corev1.ResourceCPU]; ok {
		u.cpu.Add(cpu)
	}
	if memory, ok := requests[corev1.ResourceMemory]; ok {
		u.memory.Add(memory)
	}
	// A GPU is an extended resource: Kubernetes requires request == limit, so
	// either side is the same number. Limits are read because that is the side
	// the documentation tells people to set.
	if gpu, ok := ws.Spec.Resources.Limits[gpuResource]; ok {
		u.gpu.Add(gpu)
	}
}

// within reports the first allowance this usage breaks.
//
// First, not all of them: the numbers are related - halving the CPU may fix the
// memory too - and a list of five failures reads as five problems when it is
// one shape that does not fit.
func (u *quotaUsage) within(us *dwpkv1alpha1.UserSpace, ws *dwpkv1alpha1.Workspace) error {
	quota := us.Spec.Quota

	refuse := func(format string, args ...any) error {
		return apierrors.NewForbidden(workspaceGR, ws.Name, fmt.Errorf(format, args...))
	}

	if u.running > quota.Workspaces {
		return refuse("UserSpace %q allows %d running workspace(s) and this would make %d; "+
			"stop one first, or leave this one stopped",
			us.Name, quota.Workspaces, u.running)
	}
	if u.cpu.Cmp(quota.CPU) > 0 {
		return refuse("UserSpace %q allows %s CPU and this would request %s in total",
			us.Name, quota.CPU.String(), u.cpu.String())
	}
	if u.memory.Cmp(quota.Memory) > 0 {
		return refuse("UserSpace %q allows %s memory and this would request %s in total",
			us.Name, quota.Memory.String(), u.memory.String())
	}
	gpuLimit := *resource.NewQuantity(int64(quota.GPU), resource.DecimalSI)
	if u.gpu.Cmp(gpuLimit) > 0 {
		return refuse("UserSpace %q allows %d GPU(s) and this would request %s in total",
			us.Name, quota.GPU, u.gpu.String())
	}
	if u.storage.Cmp(quota.Storage) > 0 {
		return refuse("UserSpace %q allows %s storage and this would claim %s in total; "+
			"stopped workspaces keep their volumes and still count",
			us.Name, quota.Storage.String(), u.storage.String())
	}
	return nil
}

// gpuResource is the extended resource a GPU is requested as, from the platform
// settings. A read failure falls back to the default rather than failing the
// admission: refusing every workspace because one optional settings object
// could not be read is the wrong way round.
func (v *WorkspaceValidator) gpuResource(ctx context.Context) corev1.ResourceName {
	config := &dwpkv1alpha1.PlatformConfig{}
	key := client.ObjectKey{Name: dwpkv1alpha1.PlatformConfigName}
	if err := v.client.Get(ctx, key, config); err != nil {
		return corev1.ResourceName(dwpkv1alpha1.DefaultGPUResourceName)
	}
	return corev1.ResourceName(config.GPUResource())
}

// validateResources refuses a request larger than its own limit.
//
// Kubernetes refuses it too, but at the StatefulSet - so the Workspace admits
// cleanly, the controller fails to apply the workload, and the person reads
// "must be less than or equal to cpu limit" on a Degraded condition instead of
// on the form they just submitted. SPEC §7.4 is explicit that a rejection
// carrying the reason beats an object that sits in Failed.
func validateResources(ws *dwpkv1alpha1.Workspace) error {
	var errs field.ErrorList
	requests := ws.Spec.Resources.Requests
	limits := ws.Spec.Resources.Limits

	for name, request := range requests {
		limit, ok := limits[name]
		if !ok {
			// No limit is no ceiling to exceed. The LimitRange may add one, and
			// it will not add one below the request.
			continue
		}
		if request.Cmp(limit) > 0 {
			errs = append(errs, field.Invalid(
				field.NewPath("spec", "resources", "requests").Key(string(name)),
				request.String(),
				fmt.Sprintf("is more than the limit of %s; a request is what is reserved and cannot exceed the ceiling",
					limit.String()),
			))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(workspaceGK, ws.Name, errs)
}
