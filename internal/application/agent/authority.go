package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/report"
)

// AuthorityEnvelopeVersion 标识权威分析摘要的规范封包版本。
const AuthorityEnvelopeVersion = "ai-gdm-authority-v1"

var (
	// ErrInvalidAuthority 表示 resolver 返回的权威对象损坏或绑定不一致。
	ErrInvalidAuthority = report.ErrInvalidAuthority
	// ErrUnsafeStoredAnalysis 表示存储分析包含固定 schema 之外的字段。
	ErrUnsafeStoredAnalysis = report.ErrUnsafeStoredAnalysis
)

// AuthoritativeAnalysisResolver 按白名单引用读取服务端确定性分析。
type AuthoritativeAnalysisResolver interface {
	// Resolve 必须验证引用和父级快照、规则、公式或模型绑定，禁止客户端数值回退。
	Resolve(context.Context, report.AnalysisReference) (report.Authority, error)
}

// authorityEnvelope 不包含 ResolvedAt，使同一版本权威对象重复解析时摘要保持稳定。
type authorityEnvelope struct {
	EnvelopeVersion string               `json:"envelopeVersion"`
	Kind            report.AuthorityKind `json:"kind"`
	ID              string               `json:"id"`
	Version         string               `json:"version"`
	SchemaVersion   string               `json:"schemaVersion"`
	AnalysisJSON    json.RawMessage      `json:"analysis"`
	ImmutableFields []string             `json:"immutableFields"`
}

func authoritySHA256(value report.Authority) (string, error) {
	canonical, err := value.Canonical()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(authorityEnvelope{
		EnvelopeVersion: AuthorityEnvelopeVersion,
		Kind:            canonical.Kind, ID: canonical.ID, Version: canonical.Version,
		SchemaVersion: canonical.SchemaVersion, AnalysisJSON: canonical.AnalysisJSON,
		ImmutableFields: canonical.ImmutableFields,
	})
	if err != nil {
		return "", fmt.Errorf("编码权威分析规范封包: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Service) resolveAuthority(ctx context.Context,
	reference report.AnalysisReference,
) (report.Authority, error) {
	value, err := s.resolver.Resolve(ctx, reference)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidInput) && !errors.Is(err, ErrUnsafeStoredAnalysis) {
			return report.Authority{}, fmt.Errorf("%w: resolver 返回无效权威对象: %w", ErrInvalidAuthority, err)
		}
		return report.Authority{}, fmt.Errorf("解析权威分析 %s/%s: %w", reference.Kind, reference.ID, err)
	}
	value, err = value.Canonical()
	if err != nil {
		return report.Authority{}, fmt.Errorf("规范化权威分析 %s/%s: %w", reference.Kind, reference.ID, err)
	}
	if value.Kind != reference.Kind || value.ID != reference.ID {
		return report.Authority{}, fmt.Errorf("%w: resolver 返回的 kind/id 与请求引用不一致", ErrInvalidAuthority)
	}
	if containsSensitiveText(string(value.AnalysisJSON)) {
		return report.Authority{}, fmt.Errorf("%w: 权威分析包含疑似个人信息", ErrUnsafeStoredAnalysis)
	}
	return value, nil
}
