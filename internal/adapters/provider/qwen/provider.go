// Package qwen 实现受约束的通义千问兼容接口客户端。
package qwen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	// DefaultBaseURL 是阿里云百炼 OpenAI 兼容聊天端点。
	DefaultBaseURL  = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	ProviderName    = "阿里云百炼 Qwen"
	DatasetName     = "Qwen 解释性报告"
	DefaultModel    = "qwen-plus"
	defaultTokens   = 1200
	maxTokens       = 4096
	defaultAttempts = 2
	maxAttempts     = 3
	maxBodyBytes    = 2 << 20
)

// Config 配置 Qwen 兼容端点和严格输出边界。
type Config struct {
	BaseURL             string
	APIKey              string
	Model               string
	MaxCompletionTokens int
	OutputAttempts      int
}

// Provider 只生成解释性文字，不提供或修改风险、路线、损失和搜救数值。
type Provider struct {
	client              *httpclient.Client
	endpoint            string
	apiKey              string
	model               string
	maxCompletionTokens int
	outputAttempts      int
	now                 func() time.Time
}

var _ ports.NarrativeGenerator = (*Provider)(nil)

// New 创建 Qwen 适配器；API 密钥只保存在服务端内存中。
func New(client *httpclient.Client, config Config) (*Provider, error) {
	endpoint, err := validateEndpoint(config.BaseURL)
	if err != nil {
		return nil, err
	}
	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: Qwen API 密钥不能为空", domain.ErrInvalidInput)
	}
	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = DefaultModel
	}
	tokens := config.MaxCompletionTokens
	if tokens < 0 {
		return nil, fmt.Errorf("%w: Qwen 输出 token 上限不能为负数", domain.ErrInvalidInput)
	}
	if tokens <= 0 {
		tokens = defaultTokens
	}
	if tokens > maxTokens {
		return nil, fmt.Errorf("%w: Qwen 输出 token 上限不能超过 %d", domain.ErrInvalidInput, maxTokens)
	}
	attempts := config.OutputAttempts
	if attempts < 0 {
		return nil, fmt.Errorf("%w: Qwen 输出重试次数不能为负数", domain.ErrInvalidInput)
	}
	if attempts <= 0 {
		attempts = defaultAttempts
	}
	if attempts > maxAttempts {
		return nil, fmt.Errorf("%w: Qwen 输出重试次数不能超过 %d", domain.ErrInvalidInput, maxAttempts)
	}
	if client == nil {
		client = httpclient.New(httpclient.Options{})
	}
	return &Provider{
		client: client, endpoint: endpoint, apiKey: apiKey, model: model,
		maxCompletionTokens: tokens, outputAttempts: attempts,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Generate 生成经过严格 JSON 校验的解释性说明。
func (p *Provider) Generate(ctx context.Context, input report.NarrativeInput) (report.Narrative, error) {
	if p == nil || p.client == nil {
		return report.Narrative{}, fmt.Errorf("%w: Qwen 客户端未配置", domain.ErrInvalidInput)
	}
	if p.outputAttempts <= 0 {
		return report.Narrative{}, fmt.Errorf("%w: Qwen 输出重试次数无效", domain.ErrInvalidInput)
	}
	if err := input.Validate(); err != nil {
		return report.Narrative{}, err
	}
	var last error
	for attempt := 1; attempt <= p.outputAttempts; attempt++ {
		narrative, err := p.generateOnce(ctx, input)
		if err == nil {
			return narrative, nil
		}
		last = err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			break
		}
	}
	return report.Narrative{}, fmt.Errorf("Qwen 结构化输出失败: %w", last)
}

func (p *Provider) generateOnce(ctx context.Context, input report.NarrativeInput) (report.Narrative, error) {
	body, err := json.Marshal(buildRequest(p.model, p.maxCompletionTokens, input))
	if err != nil {
		return report.Narrative{}, fmt.Errorf("编码 Qwen 请求: %w", err)
	}
	response, err := p.client.Do(ctx, httpclient.Request{
		Method: http.MethodPost, URL: p.endpoint, Body: body,
		Headers: http.Header{
			"Accept": {"application/json"}, "Authorization": {"Bearer " + p.apiKey},
			"Content-Type": {"application/json"},
		}, MaxBodyBytes: maxBodyBytes,
	})
	if err != nil {
		return report.Narrative{}, fmt.Errorf("请求 Qwen: %w", err)
	}
	content, model, finishReason, err := decodeResponse(response.Body)
	if err != nil {
		return report.Narrative{}, err
	}
	if finishReason == "length" || finishReason == "content_filter" {
		return report.Narrative{}, fmt.Errorf("%w: Qwen 完成原因 %q", domain.ErrProviderUnavailable, finishReason)
	}
	payload, err := decodeNarrative(content)
	if err != nil {
		return report.Narrative{}, fmt.Errorf("%w: Qwen 输出结构无效: %w", domain.ErrProviderUnavailable, err)
	}
	if model == "" {
		model = p.model
	}
	return p.narrative(payload, model, response), nil
}

func (p *Provider) narrative(payload narrativePayload, model string,
	response httpclient.Response,
) report.Narrative {
	digest := sha256.Sum256(response.Body)
	fetched := response.FetchedAt.UTC()
	return report.Narrative{
		Summary: payload.Summary, KeyFindings: normalizeItems(payload.KeyFindings),
		Actions: normalizeItems(payload.Actions), Caveats: normalizeItems(payload.Caveats),
		GeneratedAt: p.now().UTC(), Model: model, Available: true,
		Source: provenance.Provenance{
			Provider: ProviderName, Dataset: DatasetName, DatasetVersion: "compatible-v1",
			SourceRevision: hex.EncodeToString(digest[:]), SourceURI: httpclient.RedactURL(p.endpoint),
			Citation: "阿里云百炼 Qwen OpenAI 兼容接口", License: "遵循供应商服务条款",
			DataKind: provenance.DataKindObservation, FetchedAt: fetched,
			TemporalResolution: "按需生成", ProviderRequestID: response.RequestID,
			QualityFlags: []string{"structured_json", "explanation_only", "deterministic_values_external"},
			Limitations:  []string{"该说明不具备修改确定性风险、路线、损失或搜救结论的权限"},
		},
	}
}

func validateEndpoint(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = DefaultBaseURL
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: Qwen 地址必须是无用户信息的 HTTPS 地址", domain.ErrInvalidInput)
	}
	return parsed.String(), nil
}

func normalizeItems(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, strings.TrimSpace(value))
	}
	return result
}
