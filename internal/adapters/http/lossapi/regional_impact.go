package lossapi

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

// RegionalImpactService 在采集道路和人口前计算行政区风险覆盖面积。
type RegionalImpactService interface {
	PreviewRegion(context.Context, string, string) (exposurecollection.RegionalImpact, error)
}

func (h *Handler) regionalImpact(w http.ResponseWriter, r *http.Request) {
	service, ok := h.projector.(RegionalImpactService)
	if !ok {
		h.writeError(w, r, fmt.Errorf("%w: 区域风险预览尚未配置", domain.ErrInsufficientData))
		return
	}
	regionCode, snapshotID := chi.URLParam(r, "regionCode"), r.URL.Query().Get("snapshotId")
	if !regionCodePattern.MatchString(regionCode) || regionCode == "CN" || !assessmentIDPattern.MatchString(snapshotID) {
		h.writeError(w, r, fmt.Errorf("%w: 区域风险预览参数无效", domain.ErrInvalidInput))
		return
	}
	value, err := service.PreviewRegion(r.Context(), snapshotID, regionCode)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if value.RegionCode != regionCode || value.SnapshotID != snapshotID || !validRegionalImpact(value) {
		h.writeError(w, r, fmt.Errorf("%w: 区域风险预览范围不匹配", domain.ErrInsufficientData))
		return
	}
	payload, err := json.Marshal(successResponse{Data: value, RequestID: requestID(r)})
	if err != nil || len(payload) > 2<<20 {
		h.writeError(w, r, fmt.Errorf("%w: 区域风险预览超过安全预算", domain.ErrInsufficientData))
		return
	}
	h.writeEncodedJSON(w, http.StatusOK, payload, requestID(r))
}

func validRegionalImpact(value exposurecollection.RegionalImpact) bool {
	if value.ValidFrom.IsZero() || !value.ValidTo.After(value.ValidFrom) ||
		math.IsNaN(value.AreaSquareMeters) || math.IsInf(value.AreaSquareMeters, 0) ||
		value.AreaSquareMeters < 0 || value.ZoneCount < 0 || value.ZoneCount > 100000 ||
		value.BoundaryID == "" || value.BoundaryDigest == "" || len(value.Geometry) > 1<<20 {
		return false
	}
	if value.ZoneCount == 0 {
		return value.AreaSquareMeters == 0 && string(value.Geometry) == "null"
	}
	var geometry spatial.Geometry
	return value.AreaSquareMeters > 0 && json.Unmarshal(value.Geometry, &geometry) == nil &&
		geometry.ValidateArea() == nil
}
