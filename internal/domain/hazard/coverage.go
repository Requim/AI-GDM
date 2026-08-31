package hazard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

// CoverageMode 表示风险处理使用的空间范围约束方式。
type CoverageMode string

const (
	CoverageAdministrativeBoundary CoverageMode = "administrative_boundary"
)

// Coverage 保存风险快照使用的版本化空间范围，不包含大体积几何。
type Coverage struct {
	Mode            CoverageMode `json:"mode"`
	RegionCode      string       `json:"regionCode"`
	BoundaryID      string       `json:"boundaryId"`
	BoundaryType    string       `json:"boundaryType"`
	BoundaryVersion string       `json:"boundaryVersion"`
	Source          string       `json:"source"`
	License         string       `json:"license"`
	Reference       string       `json:"reference"`
	SHA256          string       `json:"sha256"`
	GeometrySHA256  string       `json:"geometrySha256"`
	CollectedAt     time.Time    `json:"collectedAt"`
}

// ProcessingBoundary 是传入确定性空间处理器的固定边界输入。
type ProcessingBoundary struct {
	Coverage        Coverage
	Geometry        spatial.Geometry
	InputReferences []string
}

// Validate 校验覆盖范围身份、摘要和时间语义。
func (c Coverage) Validate() error {
	if c.Mode != CoverageAdministrativeBoundary || c.RegionCode == "" || c.BoundaryID == "" ||
		c.BoundaryType == "" || c.BoundaryVersion == "" || c.Source == "" || c.License == "" ||
		c.Reference == "" || c.CollectedAt.IsZero() {
		return fmt.Errorf("%w: 风险覆盖范围字段不完整", domain.ErrInvalidInput)
	}
	if !validCoverageDigest(c.SHA256) || !validCoverageDigest(c.GeometrySHA256) {
		return fmt.Errorf("%w: 风险覆盖范围摘要无效", domain.ErrInvalidInput)
	}
	if _, offset := c.CollectedAt.Zone(); offset != 0 {
		return fmt.Errorf("%w: 风险覆盖范围采集时间必须使用 UTC", domain.ErrInvalidInput)
	}
	for _, value := range []string{c.RegionCode, c.BoundaryID, c.BoundaryType, c.BoundaryVersion,
		c.Source, c.License, c.Reference} {
		if value != strings.TrimSpace(value) || len([]rune(value)) > 2048 {
			return fmt.Errorf("%w: 风险覆盖范围文本无效", domain.ErrInvalidInput)
		}
	}
	return nil
}

// Identity 返回可绑定快照身份的稳定边界标识。
func (c Coverage) Identity() string {
	return c.BoundaryID + "|" + c.BoundaryVersion + "|" + c.SHA256 + "|" + c.GeometrySHA256
}

// Validate 校验处理边界及其审计引用。
func (b ProcessingBoundary) Validate() error {
	if err := b.Coverage.Validate(); err != nil {
		return err
	}
	if err := b.Geometry.ValidateArea(); err != nil {
		return fmt.Errorf("校验风险覆盖范围几何: %w", err)
	}
	digest, err := BoundaryGeometryDigest(b.Geometry)
	if err != nil || digest != b.Coverage.GeometrySHA256 {
		return fmt.Errorf("%w: 风险覆盖范围几何摘要不匹配", domain.ErrInvalidInput)
	}
	if len(b.InputReferences) == 0 || len(b.InputReferences) > 16 {
		return fmt.Errorf("%w: 风险覆盖范围输入引用无效", domain.ErrInvalidInput)
	}
	for _, value := range b.InputReferences {
		if strings.TrimSpace(value) == "" || len([]rune(value)) > 4096 {
			return fmt.Errorf("%w: 风险覆盖范围输入引用无效", domain.ErrInvalidInput)
		}
	}
	return nil
}

// BoundaryGeometryDigest 返回标准 Geometry JSON 的稳定 SHA-256。
func BoundaryGeometryDigest(geometry spatial.Geometry) (string, error) {
	if err := geometry.ValidateArea(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(geometry)
	if err != nil {
		return "", fmt.Errorf("编码风险覆盖范围几何: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func validCoverageDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
