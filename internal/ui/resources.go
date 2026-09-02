package ui

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// ResourceValues is what the resource form renders and reads back. Strings
// rather than quantities: an input holds whatever was typed, including the
// half-finished and the wrong, and turning it into a quantity is a parse step
// with an error message rather than a field type.
type ResourceValues struct {
	// CPU is a request only, never a limit - see Requirements for why.
	CPU         string
	MemoryLimit string
	Storage     string
	GPU         string
	// GPUResource is the extended resource name, prefilled from the platform
	// settings and editable for other hardware.
	GPUResource string
}

// defaultMemoryLimit is the default workspace's memory limit.
const defaultMemoryLimit = "4Gi"

// defaultResourceValues is a small workspace: enough to run a shell and a
// language server, with room to burst into a build.
func defaultResourceValues() ResourceValues {
	return ResourceValues{
		CPU:         "2",
		MemoryLimit: defaultMemoryLimit,
		Storage:     "10Gi",
		GPU:         "0",
		GPUResource: defaultGPUResourceName,
	}
}

const defaultGPUResourceName = "nvidia.com/gpu"

// resourceValuesFrom reads the form back, keeping what was typed so a rejected
// submission redraws with the person's own values rather than the defaults.
func resourceValuesFrom(r *http.Request) ResourceValues {
	get := func(name string) string { return strings.TrimSpace(r.Form.Get(name)) }
	values := ResourceValues{
		CPU:         get("cpu_request"),
		MemoryLimit: get("memory_limit"),
		Storage:     get("storage"),
		GPU:         get("gpu"),
		GPUResource: get("gpu_resource"),
	}
	if values.GPUResource == "" {
		values.GPUResource = defaultGPUResourceName
	}
	return values
}

// Requirements turns the typed values into a ResourceRequirements.
//
// CPU and memory are treated deliberately differently, not identically as
// they once were. CPU gets a request and never a limit: a CPU limit is
// enforced by the CFS bandwidth controller, which throttles a container back
// even when the node has idle CPU to spare, so the only effect of setting one
// is to sometimes make the workspace slower for no reason. A request has no
// such downside - it is what the scheduler reserves, and the workspace is
// free to use more whenever the node has room. Memory keeps its limit: unlike
// CPU it is not compressible, so an unbounded container risks starving or
// OOM-killing its neighbours rather than just itself, and Kubernetes copies a
// limit into the request when none is set, so memory still gets "reserved
// equals ceiling" without an explicit request field to read.
//
// The GPU is written to both requests and limits from the single field, because
// Kubernetes refuses an extended resource whose two sides differ. Setting only
// the limit would work - the API server copies it across - but writing both
// makes the object say what it means when somebody reads it back.
func (v ResourceValues) Requirements() (corev1.ResourceRequirements, error) {
	requirements := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{},
		Limits:   corev1.ResourceList{},
	}

	for _, field := range []struct {
		raw   string
		name  corev1.ResourceName
		into  corev1.ResourceList
		label string
	}{
		{v.CPU, corev1.ResourceCPU, requirements.Requests, "CPU"},
		{v.MemoryLimit, corev1.ResourceMemory, requirements.Limits, "memory limit"},
	} {
		if field.raw == "" {
			continue
		}
		quantity, err := resource.ParseQuantity(field.raw)
		if err != nil {
			return corev1.ResourceRequirements{},
				fmt.Errorf("%s: %q is not a quantity", field.label, field.raw)
		}
		field.into[field.name] = quantity
	}

	if err := v.addGPU(requirements); err != nil {
		return corev1.ResourceRequirements{}, err
	}

	// Empty maps marshal as {} and would show up in the YAML preview as noise.
	if len(requirements.Requests) == 0 {
		requirements.Requests = nil
	}
	if len(requirements.Limits) == 0 {
		requirements.Limits = nil
	}
	return requirements, nil
}

func (v ResourceValues) addGPU(requirements corev1.ResourceRequirements) error {
	if v.GPU == "" || v.GPU == "0" {
		return nil
	}
	count, err := strconv.Atoi(v.GPU)
	if err != nil || count < 0 {
		return fmt.Errorf("GPUs: %q is not a whole number", v.GPU)
	}
	name := corev1.ResourceName(v.GPUResource)
	quantity := *resource.NewQuantity(int64(count), resource.DecimalSI)
	requirements.Requests[name] = quantity
	requirements.Limits[name] = quantity
	return nil
}

// StorageQuantity parses the home volume size.
func (v ResourceValues) StorageQuantity() (resource.Quantity, error) {
	quantity, err := resource.ParseQuantity(v.Storage)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("storage: %q is not a quantity", v.Storage)
	}
	return quantity, nil
}

// resourceValuesFromWorkspace prefills the edit form from what's already set.
// Requirements writes CPU into Requests and memory into Limits (see above),
// so reading them back means reading each from the side it was written to.
// Storage is carried through for display only: the edit form renders it
// locked, since it's immutable after creation.
func resourceValuesFromWorkspace(ws *dwpkv1alpha1.Workspace, gpuResource corev1.ResourceName) ResourceValues {
	limits := ws.Spec.Resources.Limits
	values := ResourceValues{
		CPU:         quantityText(ws.Spec.Resources.Requests, corev1.ResourceCPU),
		MemoryLimit: quantityText(limits, corev1.ResourceMemory),
		GPU:         quantityText(limits, gpuResource),
		GPUResource: string(gpuResource),
	}
	if ws.Spec.Storage != nil {
		values.Storage = ws.Spec.Storage.String()
	}
	return values
}

// quantityText reads one resource out of a list as display text, or "" if
// unset - unset and zero are different answers, so this never fabricates a
// "0".
func quantityText(list corev1.ResourceList, name corev1.ResourceName) string {
	value, ok := list[name]
	if !ok {
		return ""
	}
	return value.String()
}
