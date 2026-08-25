package spatialanalysis

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// NewAnalysis 复制并规范化输入，生成与执行时间无关的稳定分析标识。
func NewAnalysis(input AnalysisInput) (Analysis, error) {
	value := Analysis{
		SnapshotID: input.SnapshotID, Version: AnalysisVersion, Area: input.Area,
		Zones: input.Zones, CalculatedAt: input.CalculatedAt,
		InputReferences: input.InputReferences, DatasetReferences: input.DatasetReferences,
		Limitations: input.Limitations,
	}
	value = normalizeAnalysis(value)
	value.Status = deriveAnalysisStatus(value.Zones)
	if err := validateAnalysisContent(value); err != nil {
		return Analysis{}, err
	}
	identifier, err := analysisID(value)
	if err != nil {
		return Analysis{}, err
	}
	value.ID = identifier
	return value, value.Validate()
}

func deriveAnalysisStatus(zones []ZoneResult) AnalysisStatus {
	if len(zones) == 0 {
		return AnalysisAreaOnly
	}
	allAvailable, allUnavailable := true, true
	for _, zone := range zones {
		metricStatuses := []MetricStatus{zone.Population.Status, zone.Roads.Status, zone.POIs.Status}
		for _, status := range metricStatuses {
			allAvailable = allAvailable && status == MetricAvailable
			allUnavailable = allUnavailable && status == MetricUnavailable
		}
		allAvailable = allAvailable && zone.Administration.Status == AdminMatchAvailable
		allUnavailable = allUnavailable && zone.Administration.Status == AdminMatchUnavailable
	}
	if allAvailable {
		return AnalysisAvailable
	}
	if allUnavailable {
		return AnalysisAreaOnly
	}
	return AnalysisPartial
}

func analysisID(value Analysis) (string, error) {
	payload := analysisIdentity{
		SnapshotID: value.SnapshotID, Version: value.Version, Status: value.Status,
		Area: value.Area, Zones: value.Zones, InputReferences: value.InputReferences,
		DatasetReferences: value.DatasetReferences, Limitations: value.Limitations,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("序列化空间分析标识输入: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "spatial-" + hex.EncodeToString(digest[:]), nil
}

type analysisIdentity struct {
	SnapshotID        string          `json:"snapshotId"`
	Version           string          `json:"analysisVersion"`
	Status            AnalysisStatus  `json:"status"`
	Area              AreaCalculation `json:"area"`
	Zones             []ZoneResult    `json:"zones"`
	InputReferences   []string        `json:"inputReferences"`
	DatasetReferences []string        `json:"datasetReferences"`
	Limitations       []string        `json:"limitations"`
}
