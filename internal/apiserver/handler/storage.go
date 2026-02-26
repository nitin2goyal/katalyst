package handler

import (
	"context"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/koptimizer/koptimizer/internal/config"
)

type StorageHandler struct {
	client client.Client
	cfg    *config.Config
}

func NewStorageHandler(c client.Client, cfg *config.Config) *StorageHandler {
	return &StorageHandler{client: c, cfg: cfg}
}

func (h *StorageHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := h.client.List(ctx, pvcList); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	pvList := &corev1.PersistentVolumeList{}
	if err := h.client.List(ctx, pvList); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Count running pods with PVC mounts
	podList := &corev1.PodList{}
	if err := h.client.List(ctx, podList); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	mountedPVCs := make(map[string]bool)
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
			continue
		}
		for _, vol := range pod.Spec.Volumes {
			if vol.PersistentVolumeClaim != nil {
				key := fmt.Sprintf("%s/%s", pod.Namespace, vol.PersistentVolumeClaim.ClaimName)
				mountedPVCs[key] = true
			}
		}
	}

	var totalRequestedGB float64
	var totalProvisionedGB float64
	unused := 0

	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]
		if req, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			totalRequestedGB += float64(req.Value()) / (1024 * 1024 * 1024)
		}

		key := fmt.Sprintf("%s/%s", pvc.Namespace, pvc.Name)
		if pvc.Status.Phase == corev1.ClaimBound && !mountedPVCs[key] {
			unused++
		}
	}

	for i := range pvList.Items {
		pv := &pvList.Items[i]
		if cap, ok := pv.Spec.Capacity[corev1.ResourceStorage]; ok {
			totalProvisionedGB += float64(cap.Value()) / (1024 * 1024 * 1024)
		}
	}

	costPerGB := h.cfg.StorageMonitor.StorageCostPerGBUSD
	if costPerGB == 0 {
		costPerGB = 0.10
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pvcCount":                  len(pvcList.Items),
		"pvCount":                   len(pvList.Items),
		"totalRequestedGB":          totalRequestedGB,
		"totalProvisionedGB":        totalProvisionedGB,
		"unusedPVCs":                unused,
		"estimatedMonthlyCostUSD":   totalProvisionedGB * costPerGB,
	})
}

func (h *StorageHandler) GetPVCs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := h.client.List(ctx, pvcList); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	type pvcInfo struct {
		Name         string  `json:"name"`
		Namespace    string  `json:"namespace"`
		StorageClass string  `json:"storageClass"`
		RequestedGB  float64 `json:"requestedGB"`
		Phase        string  `json:"phase"`
		VolumeName   string  `json:"volumeName"`
	}

	var result []pvcInfo
	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]
		sc := ""
		if pvc.Spec.StorageClassName != nil {
			sc = *pvc.Spec.StorageClassName
		}
		var gb float64
		if req, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			gb = float64(req.Value()) / (1024 * 1024 * 1024)
		}
		result = append(result, pvcInfo{
			Name:         pvc.Name,
			Namespace:    pvc.Namespace,
			StorageClass: sc,
			RequestedGB:  gb,
			Phase:        string(pvc.Status.Phase),
			VolumeName:   pvc.Spec.VolumeName,
		})
	}

	if result == nil {
		result = []pvcInfo{}
	}
	page, pageSize := parsePagination(r)
	start, end, resp := paginateSlice(len(result), page, pageSize)
	resp.Data = result[start:end]
	writeJSON(w, http.StatusOK, resp)
}
