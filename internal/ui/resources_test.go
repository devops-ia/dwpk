package ui

import "testing"

// CPU never gets a limit, regardless of what's typed: a CPU limit throttles
// a container via the CFS bandwidth controller even when the node has idle
// CPU to spare, so the value the user types is a request only. Memory keeps
// its limit, since it needs a hard ceiling.
func TestRequirementsNeverSetsACPULimit(t *testing.T) {
	t.Parallel()

	values := ResourceValues{CPU: "2", MemoryLimit: "4Gi"}
	requirements, err := values.Requirements()
	if err != nil {
		t.Fatalf("Requirements() error = %v", err)
	}

	if _, ok := requirements.Limits["cpu"]; ok {
		t.Errorf("Limits[cpu] = %v, want absent - CPU must never carry a limit", requirements.Limits["cpu"])
	}
	got, ok := requirements.Requests["cpu"]
	if !ok || got.String() != "2" {
		t.Errorf("Requests[cpu] = %v (present=%v), want 2", got, ok)
	}
	if _, ok := requirements.Requests["memory"]; ok {
		t.Errorf("Requests[memory] = %v, want absent - memory is a limit only, Kubernetes fills the request in", requirements.Requests["memory"])
	}
	if got := requirements.Limits["memory"]; got.String() != "4Gi" {
		t.Errorf("Limits[memory] = %v, want 4Gi", got)
	}
}
