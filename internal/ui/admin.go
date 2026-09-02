package ui

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	dwpkv1alpha1 "github.com/devops-ia/dwpk/api/v1alpha1"
)

func (s *Server) handleAdminQuota(w http.ResponseWriter, r *http.Request) {
	session, ok := requestSessionFrom(r.Context())
	if !ok {
		s.redirectToLogin(w, r)
		return
	}
	api, err := s.clientFactory.ForToken(session.Token)
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	userSpaces, err := api.ListUserSpaces(r.Context())
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	workspacesList, err := api.ListWorkspaces(r.Context(), "")
	if err != nil {
		s.writeErrorPage(w, r, err)
		return
	}
	s.renderAuthedPage(w, r, http.StatusOK, session, "Admin quota", AdminQuotaPage(QuotaData{Session: session, Rows: quotaRows(userSpaces, workspacesList, s.gpuResource(r)), Notice: doneNotice(r)}))
}

func quotaRows(
	userSpaces []dwpkv1alpha1.UserSpace,
	workspacesList []dwpkv1alpha1.Workspace,
	gpuResource corev1.ResourceName,
) []QuotaRow {
	workspacesByNamespace := make(map[string][]dwpkv1alpha1.Workspace)
	for _, ws := range workspacesList {
		workspacesByNamespace[ws.Namespace] = append(workspacesByNamespace[ws.Namespace], ws)
	}
	rows := make([]QuotaRow, 0, len(userSpaces))
	for _, userSpace := range userSpaces {
		cpuUsed := resource.MustParse("0")
		memoryUsed := resource.MustParse("0")
		storageUsed := resource.MustParse("0")
		gpuUsed := resource.MustParse("0")
		count := 0
		// Only storage counts a stopped workspace. Stopping deletes the pod, so
		// CPU, memory, GPU and the count itself all fall to zero for it - but
		// the home PVC survives, which is what makes stopping non-destructive,
		// and that volume still occupies the quota.
		//
		// Reporting storage as running-only would show headroom the Kubernetes
		// ResourceQuota does not agree exists, and the create would then fail
		// citing a limit the screen said was not reached.
		for _, ws := range workspacesByNamespace[userSpace.Status.Namespace] {
			if ws.Spec.Storage != nil {
				storageUsed.Add(ws.Spec.Storage.DeepCopy())
			}
			if !ws.Spec.Running {
				continue
			}
			count++
			requests := ws.Spec.Resources.Requests
			cpuUsed.Add(requests.Cpu().DeepCopy())
			memoryUsed.Add(requests.Memory().DeepCopy())
			if gpu, ok := ws.Spec.Resources.Limits[gpuResource]; ok {
				gpuUsed.Add(gpu.DeepCopy())
			}
		}
		rows = append(rows, QuotaRow{
			Name:           userSpace.Name,
			Owner:          userSpace.Spec.Owner,
			Namespace:      userSpace.Status.Namespace,
			WorkspaceCount: count,
			WorkspaceLimit: userSpace.Spec.Quota.Workspaces,
			CPUUsed:        cpuUsed.String(),
			CPULimit:       userSpace.Spec.Quota.CPU.String(),
			MemoryUsed:     memoryUsed.String(),
			MemoryLimit:    userSpace.Spec.Quota.Memory.String(),
			StorageUsed:    storageUsed.String(),
			StorageLimit:   userSpace.Spec.Quota.Storage.String(),
			GPUUsed:        gpuUsed.String(),
			GPULimit:       fmt.Sprintf("%d", userSpace.Spec.Quota.GPU),
		})
	}
	slices.SortFunc(rows, func(a, b QuotaRow) int { return strings.Compare(a.Name, b.Name) })
	return rows
}
