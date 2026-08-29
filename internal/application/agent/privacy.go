package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/report"
)

const maxSensitiveScanRunes = 256 << 10

const (
	evidenceRedactionVersion       = "ai-gdm-public-evidence-redaction-v2"
	evidenceItemReferenceVersion   = "ai-gdm-public-evidence-item-v1"
	evidenceRequestAuditVersion    = "ai-gdm-public-evidence-request-v1"
	publicEvidenceDatasetVersion   = "redacted-v2"
	publicEvidenceLicenseStatement = "来源许可需在公开站点人工核验"
)

var sensitiveContactPattern = regexp.MustCompile(`(?i)` +
	`(?:\+?86[[:space:]\-()（）]*)?1[3-9](?:[[:space:]\-()（）]*[0-9]){9}|` +
	`(?:\+?86[[:space:]\-()（）]*)?0[1-9][0-9]{1,2}[[:space:]\-()（）]+(?:[0-9][[:space:]\-()（）]*){6,7}[0-9]|` +
	`[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)

const sensitiveContactField = `(?:手机号|手机号码|联系电话|咨询电话|报警电话|电话|座机|联系方式|联系号码)`

var sensitiveContactFieldPattern = regexp.MustCompile(
	sensitiveContactField + `(?:[[:space:]:："'=]|$)|` +
		sensitiveContactField + `(?:为|是)[[:space:]:："'=]*(?:\+|[0-9(（])`,
)

var sensitiveTextMarkers = []string{
	"姓名", "联系人", "身份证", "家庭住址", "详细地址", "门牌号",
}

func searchQueryForAuthority(kind report.AuthorityKind) string {
	switch kind {
	case report.AuthorityHazardSnapshot:
		return "地质灾害 风险监测 官方通报"
	case report.AuthorityEvacuationRoute:
		return "地质灾害 疏散道路 交通管制 官方通报"
	case report.AuthorityLossAssessment:
		return "地质灾害 损失影响 官方通报"
	case report.AuthoritySurvivalAssessment:
		return "地质灾害 搜救进展 官方通报"
	default:
		return "地质灾害 官方通报"
	}
}

func evidenceContainsSensitiveData(value report.Evidence) bool {
	payload, err := json.Marshal(value)
	return err != nil || containsSensitiveText(string(payload))
}

func minimizedEvidence(value report.Evidence) (report.Evidence, error) {
	origin, host, err := evidenceOrigin(value.URL, value.Source.QualityFlags)
	if err != nil {
		return report.Evidence{}, err
	}
	reference, err := evidenceAuditReference(value)
	if err != nil {
		return report.Evidence{}, err
	}
	result := report.Evidence{
		Title: "公开灾害信息来源", URL: origin,
		Summary:  "标题与摘要已去标识化，请由值守人员访问公开站点核验原文。",
		SiteName: host, CrawledAt: value.CrawledAt,
		Source: minimizedEvidenceSource(value.Source, origin, host, reference),
	}
	if err := result.Validate(); err != nil {
		return report.Evidence{}, fmt.Errorf("最小化公开证据: %w", err)
	}
	return result, nil
}

func evidenceAuditReference(value report.Evidence) (string, error) {
	itemURL, err := normalizedEvidenceItemURL(value.URL)
	if err != nil {
		return "", err
	}
	itemRevision, err := normalizedItemRevision(value.Source.SourceRevision)
	if err != nil {
		return "", err
	}
	identity := struct {
		Version, URL, ItemRevision, ContentSHA256 string
	}{
		Version: evidenceItemReferenceVersion, URL: itemURL, ItemRevision: itemRevision,
		ContentSHA256: evidenceContentIdentity(value),
	}
	payload, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("编码公开证据审计身份: %w", err)
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

func evidenceContentIdentity(value report.Evidence) string {
	content := strings.Join([]string{
		strings.Join(strings.Fields(value.Title), " "), strings.Join(strings.Fields(value.Summary), " "),
	}, "\n")
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

func normalizedEvidenceItemURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("公开证据条目标识无效")
	}
	parsed.Scheme, parsed.Host, parsed.Fragment = "https", strings.ToLower(parsed.Host), ""
	parsed.RawQuery = parsed.Query().Encode()
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), nil
}

func normalizedItemRevision(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "sha256:") && validRawSHA256(strings.TrimPrefix(value, "sha256:")) {
		return value, nil
	}
	if validRawSHA256(value) {
		return "sha256:" + value, nil
	}
	return "", fmt.Errorf("公开证据缺少稳定条目修订")
}

func utcTimestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func evidenceOrigin(raw string, flags []string) (string, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", "", fmt.Errorf("公开证据地址无效")
	}
	base, err := trustedEvidenceBase(flags)
	if err != nil || !hostWithinTrustedBase(parsed.Hostname(), base) {
		return "", "", fmt.Errorf("公开证据可信基域无效")
	}
	origin := &url.URL{Scheme: "https", Host: base, Path: "/"}
	return origin.String(), base, nil
}

func trustedEvidenceBase(flags []string) (string, error) {
	base := ""
	for _, flag := range flags {
		if !strings.HasPrefix(flag, report.TrustedDomainQualityFlagPrefix) {
			continue
		}
		candidate := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(flag, report.TrustedDomainQualityFlagPrefix)))
		if candidate == "" || strings.ContainsAny(candidate, "/:@") || (base != "" && base != candidate) {
			return "", fmt.Errorf("可信基域标记无效")
		}
		base = candidate
	}
	if base == "" {
		return "", fmt.Errorf("缺少可信基域标记")
	}
	return base, nil
}

func hostWithinTrustedBase(host, base string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	base = strings.TrimSuffix(strings.ToLower(base), ".")
	return host == base || strings.HasSuffix(host, "."+base)
}

func minimizedEvidenceSource(value provenance.Provenance, sourceURI, trustedBase, reference string) provenance.Provenance {
	return provenance.Provenance{
		Provider: "public-search", Dataset: "public-disaster-information",
		DatasetVersion: publicEvidenceDatasetVersion, SourceRevision: reference,
		SourceURI: sourceURI, Citation: "公开搜索证据；原始条目已转换为不可逆审计引用",
		License: publicEvidenceLicenseStatement, DataKind: provenance.DataKindObservation,
		PublishedAt: value.PublishedAt, FetchedAt: value.FetchedAt,
		ValidFrom: value.ValidFrom, ValidTo: value.ValidTo, Stale: value.Stale,
		SHA256: evidenceResponseSHA256(value), TransformVersion: evidenceRedactionVersion,
		ProviderRequestID: evidenceRequestAuditReference(value),
		QualityFlags: []string{"trusted_domain", report.TrustedDomainQualityFlagPrefix + trustedBase,
			"per_item_audit_reference", "response_audit_separated"},
		Limitations: []string{"证据文本、地址路径和未配置子域已最小化；条目身份与批次响应审计分别使用不可逆摘要"},
	}
}

func evidenceResponseSHA256(value provenance.Provenance) string {
	if validRawSHA256(value.SHA256) {
		return value.SHA256
	}
	return ""
}

func evidenceRequestAuditReference(value provenance.Provenance) string {
	requestID := strings.TrimSpace(value.ProviderRequestID)
	if requestID == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(evidenceRequestAuditVersion + "\x00" + value.Provider + "\x00" + requestID))
	return fmt.Sprintf("sha256:%x", digest[:])
}

func validRawSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func containsSensitiveText(value string) bool {
	if utf8.RuneCountInString(value) > maxSensitiveScanRunes {
		return true
	}
	if sensitiveContactPattern.MatchString(value) || sensitiveContactFieldPattern.MatchString(value) {
		return true
	}
	for _, marker := range sensitiveTextMarkers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
