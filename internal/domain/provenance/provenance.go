package provenance

import (
	"errors"
	"fmt"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
)

// DataKind 标识数据是观测、模型结果、基线还是历史记录。
type DataKind string

const (
	DataKindObservation DataKind = "observation"
	DataKindNowcast     DataKind = "nowcast"
	DataKindForecast    DataKind = "forecast"
	DataKindBaseline    DataKind = "baseline"
	DataKindHistorical  DataKind = "historical"
)

// Provenance 保存外部数据的来源、时效、许可和处理信息。
type Provenance struct {
	Provider           string     `json:"provider"`
	Dataset            string     `json:"dataset"`
	DatasetVersion     string     `json:"datasetVersion,omitempty"`
	SourceURI          string     `json:"sourceUri"`
	Citation           string     `json:"citation,omitempty"`
	License            string     `json:"license,omitempty"`
	DataKind           DataKind   `json:"dataKind"`
	ObservedAt         time.Time  `json:"observedAt,omitempty"`
	PublishedAt        time.Time  `json:"publishedAt,omitempty"`
	FetchedAt          time.Time  `json:"fetchedAt"`
	ValidFrom          time.Time  `json:"validFrom,omitempty"`
	ValidTo            time.Time  `json:"validTo,omitempty"`
	SpatialResolution  string     `json:"spatialResolution,omitempty"`
	TemporalResolution string     `json:"temporalResolution,omitempty"`
	CRS                string     `json:"crs,omitempty"`
	BBox               [4]float64 `json:"bbox,omitempty"`
	SHA256             string     `json:"sha256,omitempty"`
	TransformVersion   string     `json:"transformVersion,omitempty"`
	ProviderRequestID  string     `json:"providerRequestId,omitempty"`
	Model              string     `json:"model,omitempty"`
	Stale              bool       `json:"stale"`
	QualityFlags       []string   `json:"qualityFlags,omitempty"`
	Limitations        []string   `json:"limitations,omitempty"`
}

// Validate 校验最小可追溯字段和时间窗口。
func (p Provenance) Validate() error {
	if p.Provider == "" || p.Dataset == "" || p.SourceURI == "" || p.FetchedAt.IsZero() {
		return fmt.Errorf("%w: 数据来源字段不完整", domain.ErrInvalidInput)
	}
	if !p.DataKind.Valid() {
		return fmt.Errorf("%w: 未知数据分类 %q", domain.ErrInvalidInput, p.DataKind)
	}
	if !p.ValidFrom.IsZero() && !p.ValidTo.IsZero() && p.ValidTo.Before(p.ValidFrom) {
		return fmt.Errorf("%w: 数据有效期结束时间早于开始时间", domain.ErrInvalidInput)
	}
	return nil
}

// IsStale 按查询时刻重新判断数据是否超过有效期。
func (p Provenance) IsStale(now time.Time) bool {
	if !p.ValidTo.IsZero() {
		return now.After(p.ValidTo)
	}
	return p.Stale
}

// Valid 判断数据分类是否受支持。
func (k DataKind) Valid() bool {
	switch k {
	case DataKindObservation, DataKindNowcast, DataKindForecast, DataKindBaseline, DataKindHistorical:
		return true
	default:
		return false
	}
}

// WrapInvalid 保留领域错误语义并补充字段上下文。
func WrapInvalid(field string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, domain.ErrInvalidInput) {
		return fmt.Errorf("%s: %w", field, err)
	}
	return err
}

// Artifact 描述已校验并可供后续处理的数据文件。
type Artifact struct {
	Reference  string     `json:"reference"`
	MediaType  string     `json:"mediaType"`
	LocalPath  string     `json:"localPath"`
	SizeBytes  int64      `json:"sizeBytes"`
	Provenance Provenance `json:"provenance"`
}

// Validate 校验待下载或已落盘制品的最小字段。
func (a Artifact) Validate() error {
	if a.Reference == "" || a.MediaType == "" {
		return fmt.Errorf("%w: 制品引用或媒体类型为空", domain.ErrInvalidInput)
	}
	if a.SizeBytes < 0 {
		return fmt.Errorf("%w: 制品大小不能为负数", domain.ErrInvalidInput)
	}
	return a.Provenance.Validate()
}
