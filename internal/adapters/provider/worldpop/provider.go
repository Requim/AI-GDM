// Package worldpop 接入 WorldPop v2 异步人口汇总接口。
package worldpop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	"github.com/Requim/AI-GDM/internal/domain"
)

const (
	defaultBaseURL      = "https://api.worldpop.org/v2"
	defaultResolution   = "100m"
	defaultPollInterval = 2 * time.Second
	defaultMaxPolls     = 30
	defaultDatasetID    = "urn:worldpop:global-annual-population:100m:v2"
	maxRequestBytes     = 1 << 20
	maxResponseBytes    = 64 << 10
	worldPopDatasetURL  = "https://hub.worldpop.org/project/categories?id=3"
)

var taskIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

// Options 配置 WorldPop 固定端点和有界轮询。
type Options struct {
	Client       *httpclient.Client
	BaseURL      string
	Resolution   string
	PollInterval time.Duration
	MaxPolls     int
	DatasetID    string
	Sleep        func(context.Context, time.Duration) error
}

// Provider 执行 WorldPop 人口任务并轮询同源固定路径。
type Provider struct {
	client       *httpclient.Client
	baseURL      string
	resolution   string
	pollInterval time.Duration
	maxPolls     int
	datasetID    string
	sleep        func(context.Context, time.Duration) error
}

// New 创建 WorldPop provider。
func New(options Options) (*Provider, error) {
	applyDefaults(&options)
	baseURL, err := normalizeBaseURL(options.BaseURL)
	if err != nil {
		return nil, err
	}
	if options.Client == nil || options.Resolution != "100m" || options.MaxPolls <= 0 ||
		options.MaxPolls > 120 || options.PollInterval <= 0 || options.PollInterval > 30*time.Second {
		return nil, fmt.Errorf("%w: WorldPop 配置无效", domain.ErrInvalidInput)
	}
	if !validDatasetID(options.DatasetID) {
		return nil, fmt.Errorf("%w: WorldPop 数据集身份无效", domain.ErrInvalidInput)
	}
	return &Provider{client: options.Client, baseURL: baseURL, resolution: options.Resolution,
		pollInterval: options.PollInterval, maxPolls: options.MaxPolls,
		datasetID: options.DatasetID, sleep: options.Sleep}, nil
}

// Population 提交人口汇总任务并只使用配置端点轮询任务标识。
func (p *Provider) Population(ctx context.Context,
	query exposurecollection.PopulationQuery,
) (exposurecollection.PopulationResult, error) {
	if err := validateQuery(query); err != nil {
		return exposurecollection.PopulationResult{}, err
	}
	body, err := json.Marshal(populationRequest{GeoJSON: query.Geometry, Year: query.Year,
		Resolution: p.resolution})
	if err != nil || len(body) > maxRequestBytes {
		return exposurecollection.PopulationResult{}, fmt.Errorf("%w: WorldPop 请求超过预算", domain.ErrInvalidInput)
	}
	response, err := p.client.Do(ctx, httpclient.Request{Method: http.MethodPost,
		URL: p.baseURL + "/population", Headers: jsonHeaders(), Body: body, MaxBodyBytes: maxResponseBytes,
		MaxAttempts: 1, RedirectPolicy: httpclient.RedirectDeny})
	if err != nil {
		return exposurecollection.PopulationResult{}, fmt.Errorf("提交 WorldPop 任务: %w", err)
	}
	task, err := decodeSubmission(response.Body)
	if err != nil {
		return exposurecollection.PopulationResult{}, err
	}
	return p.poll(ctx, task.TaskID, query, response.FetchedAt)
}

func (p *Provider) poll(ctx context.Context, taskID string, query exposurecollection.PopulationQuery,
	submittedAt time.Time,
) (exposurecollection.PopulationResult, error) {
	for attempt := 0; attempt < p.maxPolls; attempt++ {
		if attempt > 0 {
			if err := p.sleep(ctx, p.pollInterval); err != nil {
				return exposurecollection.PopulationResult{}, fmt.Errorf("等待 WorldPop 任务: %w", err)
			}
		}
		status, fetchedAt, err := p.readTask(ctx, taskID)
		if err != nil {
			return exposurecollection.PopulationResult{}, err
		}
		if status.Status == "failure" {
			return exposurecollection.PopulationResult{}, providerError("WorldPop 任务失败")
		}
		if status.Status == "success" {
			return buildResult(p.baseURL, p.datasetID, p.resolution, taskID,
				status, query, fetchedAt, submittedAt)
		}
	}
	return exposurecollection.PopulationResult{}, providerError("WorldPop 任务轮询超时")
}

func (p *Provider) readTask(ctx context.Context, taskID string) (taskStatus, time.Time, error) {
	response, err := p.client.Do(ctx, httpclient.Request{Method: http.MethodGet,
		URL: p.baseURL + "/tasks/" + url.PathEscape(taskID), MaxBodyBytes: maxResponseBytes,
		RedirectPolicy: httpclient.RedirectSameOriginHTTPS})
	if err != nil {
		return taskStatus{}, time.Time{}, fmt.Errorf("轮询 WorldPop 任务: %w", err)
	}
	value, err := decodeTask(response.Body, taskID)
	if err != nil {
		return taskStatus{}, time.Time{}, err
	}
	return value, response.FetchedAt.UTC().Truncate(time.Microsecond), nil
}

func buildResult(baseURL, datasetID, resolution, taskID string, status taskStatus,
	query exposurecollection.PopulationQuery,
	fetchedAt, submittedAt time.Time,
) (exposurecollection.PopulationResult, error) {
	fetchedAt = fetchedAt.UTC().Truncate(time.Microsecond)
	submittedAt = submittedAt.UTC().Truncate(time.Microsecond)
	result := status.Result
	if !validPopulationResult(result) || result.DataYear != query.Year {
		return exposurecollection.PopulationResult{}, providerError("WorldPop 成功结果不完整")
	}
	if fetchedAt.Before(submittedAt) {
		return exposurecollection.PopulationResult{}, providerError("WorldPop 任务时间倒置")
	}
	from := time.Date(query.Year, time.January, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(1, 0, 0)
	taskURL := strings.TrimSuffix(baseURL, "/") + "/tasks/" + taskID
	return exposurecollection.PopulationResult{TaskID: taskID, Total: *result.TotalPopulation,
		AreaKM2: result.AreaKM2, DataYear: result.DataYear, DataSource: result.DataSource,
		DatasetIdentity: datasetID,
		CollectedAt:     fetchedAt, ValidFrom: from, ValidTo: to,
		InputReferences: []string{taskURL, worldPopDatasetURL},
		Limitations:     datasetIdentityLimitations(datasetID, result.DataSource, resolution)}, nil
}

func datasetIdentityLimitations(datasetID, dataSource, resolution string) []string {
	if dataSource == datasetID {
		return nil
	}
	return []string{fmt.Sprintf("WorldPop v2 响应未返回可校验的数据集 URN；%s 仅为部署配置声明，响应证据为 data_source=%q、resolution=%s",
		datasetID, dataSource, resolution)}
}

type populationRequest struct {
	GeoJSON    json.RawMessage `json:"geojson"`
	Year       int             `json:"year"`
	Resolution string          `json:"resolution"`
}

type submission struct {
	TaskID   string `json:"task_id"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	CheckURL string `json:"check_url"`
}

type populationResult struct {
	TotalPopulation   *float64 `json:"total_population"`
	AreaKM2           float64  `json:"area_km2"`
	DataYear          int      `json:"data_year"`
	DataSource        string   `json:"data_source"`
	PopulationDensity *float64 `json:"population_density"`
	ProcessingTimeMS  *float64 `json:"processing_time_ms"`
}

type taskStatus struct {
	TaskID   string            `json:"task_id"`
	Status   string            `json:"status"`
	Progress *float64          `json:"progress"`
	Stage    *string           `json:"stage"`
	Result   *populationResult `json:"result"`
	Error    *string           `json:"error"`
}

var submissionFields = map[string]struct{}{
	"task_id": {}, "status": {}, "message": {}, "check_url": {},
}

var taskFields = map[string]struct{}{
	"task_id": {}, "status": {}, "progress": {}, "stage": {}, "result": {}, "error": {},
}

var populationResultFields = map[string]struct{}{
	"total_population": {}, "area_km2": {}, "data_year": {}, "data_source": {},
	"population_density": {}, "processing_time_ms": {},
}

func decodeSubmission(payload []byte) (submission, error) {
	var value submission
	if _, err := strictJSONObject(payload, submissionFields); err != nil {
		return submission{}, providerError("WorldPop 提交响应无效")
	}
	if err := decodeExactJSON(payload, &value); err != nil || !taskIDPattern.MatchString(value.TaskID) ||
		!pendingStatus(strings.ToLower(value.Status)) {
		return submission{}, providerError("WorldPop 提交响应无效")
	}
	value.Status = strings.ToLower(value.Status)
	return value, nil
}

func decodeTask(payload []byte, taskID string) (taskStatus, error) {
	fields, err := strictJSONObject(payload, taskFields)
	if err != nil {
		return taskStatus{}, providerError("WorldPop 任务响应无效")
	}
	if result := bytes.TrimSpace(fields["result"]); len(result) > 0 && !bytes.Equal(result, []byte("null")) {
		if _, err = strictJSONObject(result, populationResultFields); err != nil {
			return taskStatus{}, providerError("WorldPop 任务结果响应无效")
		}
	}
	var value taskStatus
	if err = decodeExactJSON(payload, &value); err != nil || value.TaskID != taskID {
		return taskStatus{}, providerError("WorldPop 任务响应无效")
	}
	value.Status = strings.ToLower(value.Status)
	if !pendingStatus(value.Status) && value.Status != "success" && value.Status != "failure" {
		return taskStatus{}, providerError("WorldPop 任务状态未知")
	}
	if !validTaskState(value) {
		return taskStatus{}, providerError("WorldPop 任务状态与结果矛盾")
	}
	return value, nil
}

func strictJSONObject(payload []byte, allowed map[string]struct{}) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, fmt.Errorf("JSON 对象起始无效")
	}
	values := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		token, tokenErr := decoder.Token()
		key, ok := token.(string)
		if tokenErr != nil || !ok || key != strings.ToLower(key) {
			return nil, fmt.Errorf("JSON 字段名无效")
		}
		if _, allowedKey := allowed[key]; !allowedKey {
			return nil, fmt.Errorf("JSON 字段 %s 未允许", key)
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("JSON 字段 %s 重复", key)
		}
		var raw json.RawMessage
		if err = decoder.Decode(&raw); err != nil {
			return nil, err
		}
		values[key] = raw
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, fmt.Errorf("JSON 对象结束无效")
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("JSON 对象存在尾随内容")
	}
	return values, nil
}

func decodeExactJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("JSON 存在尾随内容")
	}
	return nil
}

func validTaskState(value taskStatus) bool {
	if value.Progress != nil && (!finite(*value.Progress) || *value.Progress < 0 || *value.Progress > 100) {
		return false
	}
	if value.Stage != nil && !validTaskStage(*value.Stage) {
		return false
	}
	switch {
	case pendingStatus(value.Status):
		return value.Result == nil && value.Error == nil
	case value.Status == "success":
		return value.Stage != nil && validPopulationResult(value.Result) && value.Error == nil
	case value.Status == "failure":
		return value.Stage == nil && value.Result == nil && value.Error != nil && validProviderError(*value.Error)
	default:
		return false
	}
}

func validPopulationResult(value *populationResult) bool {
	return value != nil && value.TotalPopulation != nil && finiteNonNegative(*value.TotalPopulation) &&
		finitePositive(value.AreaKM2) &&
		value.DataYear >= 2015 && value.DataYear <= 2030 && validDataSource(value.DataSource) &&
		value.PopulationDensity != nil && finiteNonNegative(*value.PopulationDensity) &&
		value.ProcessingTimeMS != nil && finiteNonNegative(*value.ProcessingTimeMS)
}

func validTaskStage(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 256 && !unsafeDatasetText(value)
}

func validProviderError(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 512 && !unsafeDatasetText(value)
}

func validateQuery(value exposurecollection.PopulationQuery) error {
	if value.Year < 2015 || value.Year > 2030 || !finitePositive(value.ExpectedAreaSquareMeter) ||
		len(value.Geometry) == 0 || len(value.Geometry) > maxRequestBytes {
		return fmt.Errorf("%w: WorldPop 查询无效", domain.ErrInvalidInput)
	}
	return validateGeometry(value.Geometry)
}

func validateGeometry(payload json.RawMessage) error {
	var value struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(payload, &value); err != nil || len(value.Coordinates) == 0 ||
		(value.Type != "Polygon" && value.Type != "MultiPolygon") {
		return fmt.Errorf("%w: WorldPop GeoJSON 必须是 Polygon 或 MultiPolygon", domain.ErrInvalidInput)
	}
	return nil
}

func validDataSource(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 256 && !unsafeDatasetText(value)
}

func validDatasetID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 128 &&
		strings.HasPrefix(value, "urn:worldpop:") && !unsafeDatasetText(value)
}

func unsafeDatasetText(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return true
		}
	}
	return false
}

func pendingStatus(value string) bool {
	return value == "pending" || value == "received" || value == "started" ||
		value == "progress" || value == "retry"
}

func normalizeBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: WorldPop BaseURL 必须是无凭据 HTTPS 地址", domain.ErrInvalidInput)
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func applyDefaults(options *Options) {
	if options.BaseURL == "" {
		options.BaseURL = defaultBaseURL
	}
	if options.Resolution == "" {
		options.Resolution = defaultResolution
	}
	if options.PollInterval == 0 {
		options.PollInterval = defaultPollInterval
	}
	if options.MaxPolls == 0 {
		options.MaxPolls = defaultMaxPolls
	}
	if options.DatasetID == "" {
		options.DatasetID = defaultDatasetID
	}
	if options.Sleep == nil {
		options.Sleep = sleepContext
	}
}

func jsonHeaders() http.Header {
	value := make(http.Header)
	value.Set("Content-Type", "application/json")
	value.Set("Accept", "application/json")
	return value
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func providerError(message string) error {
	return fmt.Errorf("%w: %s", domain.ErrProviderUnavailable, message)
}

func finite(value float64) bool            { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func finitePositive(value float64) bool    { return finite(value) && value > 0 }
func finiteNonNegative(value float64) bool { return finite(value) && value >= 0 }
