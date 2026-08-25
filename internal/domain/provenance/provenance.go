package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
)

// SourcePart 描述组合制品中的一个可独立校验来源分片。
type SourcePart struct {
	Reference string     `json:"reference"`
	Revision  string     `json:"revision"`
	SizeBytes int64      `json:"sizeBytes"`
	BBox      [4]float64 `json:"bbox,omitempty"`
	SHA256    string     `json:"sha256,omitempty"`
}

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
	Provider            string       `json:"provider"`
	Dataset             string       `json:"dataset"`
	DatasetVersion      string       `json:"datasetVersion,omitempty"`
	SourceRevision      string       `json:"sourceRevision,omitempty"`
	SourceURI           string       `json:"sourceUri"`
	Citation            string       `json:"citation,omitempty"`
	License             string       `json:"license,omitempty"`
	DataKind            DataKind     `json:"dataKind"`
	ObservedAt          time.Time    `json:"observedAt,omitempty,omitzero"`
	PublishedAt         time.Time    `json:"publishedAt,omitempty,omitzero"`
	RevisionFirstSeenAt time.Time    `json:"revisionFirstSeenAt,omitempty,omitzero"`
	FetchedAt           time.Time    `json:"fetchedAt"`
	ValidFrom           time.Time    `json:"validFrom,omitempty"`
	ValidTo             time.Time    `json:"validTo,omitempty"`
	SpatialResolution   string       `json:"spatialResolution,omitempty"`
	TemporalResolution  string       `json:"temporalResolution,omitempty"`
	CRS                 string       `json:"crs,omitempty"`
	BBox                [4]float64   `json:"bbox,omitempty"`
	SHA256              string       `json:"sha256,omitempty"`
	TransformVersion    string       `json:"transformVersion,omitempty"`
	ProviderRequestID   string       `json:"providerRequestId,omitempty"`
	Model               string       `json:"model,omitempty"`
	Stale               bool         `json:"stale"`
	QualityFlags        []string     `json:"qualityFlags,omitempty"`
	Limitations         []string     `json:"limitations,omitempty"`
	SourceParts         []SourcePart `json:"sourceParts,omitempty"`
}

// Validate 校验最小可追溯字段和时间窗口。
func (p Provenance) Validate() error {
	if p.Provider == "" || p.Dataset == "" || p.SourceURI == "" || p.FetchedAt.IsZero() {
		return fmt.Errorf("%w: 数据来源字段不完整", domain.ErrInvalidInput)
	}
	if !p.DataKind.Valid() {
		return fmt.Errorf("%w: 未知数据分类 %q", domain.ErrInvalidInput, p.DataKind)
	}
	if name := nonUTCProvenanceTime(p); name != "" {
		return fmt.Errorf("%w: 数据来源 %s 必须使用 UTC", domain.ErrInvalidInput, name)
	}
	if !p.ValidFrom.IsZero() && !p.ValidTo.IsZero() && p.ValidTo.Before(p.ValidFrom) {
		return fmt.Errorf("%w: 数据有效期结束时间早于开始时间", domain.ErrInvalidInput)
	}
	if err := validateSourceParts(p.SourceParts); err != nil {
		return err
	}
	if len(p.SourceParts) > 0 && p.SourceRevision != CompositeSourceRevision(p.SourceParts) {
		return fmt.Errorf("%w: 组合制品修订与分片清单不一致", domain.ErrInvalidInput)
	}
	return nil
}

func validateSourceParts(parts []SourcePart) error {
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part.Reference) == "" || strings.TrimSpace(part.Revision) == "" || part.SizeBytes <= 0 {
			return fmt.Errorf("%w: 组合制品分片来源不完整", domain.ErrInvalidInput)
		}
		if part.SHA256 != "" && !validSHA256(part.SHA256) {
			return fmt.Errorf("%w: 组合制品分片摘要无效", domain.ErrInvalidInput)
		}
		if _, exists := seen[part.Reference]; exists {
			return fmt.Errorf("%w: 组合制品包含重复分片", domain.ErrInvalidInput)
		}
		seen[part.Reference] = struct{}{}
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// CompositeSourceRevision 为来源分片生成与顺序无关的可审计组合修订。
func CompositeSourceRevision(parts []SourcePart) string {
	values := append([]SourcePart(nil), parts...)
	sort.Slice(values, func(left, right int) bool { return values[left].Reference < values[right].Reference })
	digest := sha256.New()
	for _, part := range values {
		_, _ = digest.Write([]byte(part.Reference))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(part.Revision))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(strconv.FormatInt(part.SizeBytes, 10)))
		_, _ = digest.Write([]byte{'\n'})
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func nonUTCProvenanceTime(value Provenance) string {
	times := []struct {
		name  string
		value time.Time
	}{
		{name: "observedAt", value: value.ObservedAt}, {name: "publishedAt", value: value.PublishedAt},
		{name: "revisionFirstSeenAt", value: value.RevisionFirstSeenAt},
		{name: "fetchedAt", value: value.FetchedAt}, {name: "validFrom", value: value.ValidFrom},
		{name: "validTo", value: value.ValidTo},
	}
	for _, item := range times {
		if item.value.IsZero() {
			continue
		}
		_, offset := item.value.Zone()
		if offset != 0 {
			return item.name
		}
	}
	return ""
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
