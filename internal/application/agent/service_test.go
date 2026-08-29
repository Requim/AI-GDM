package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/domain/report"
	survivaldomain "github.com/Requim/AI-GDM/internal/domain/survival"
	"github.com/Requim/AI-GDM/internal/ports"
)

func TestGenerateResolvesAuthorityBeforeProviders(t *testing.T) {
	resolver := &resolverStub{value: validLossAuthority()}
	search := &searchStub{values: []report.Evidence{validEvidence()}}
	generator := &generatorStub{value: validNarrative("解释不能覆盖 Authority。")}
	service := newService(t, resolver, search, generator)

	result, err := service.Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || search.calls != 1 || generator.calls != 1 {
		t.Fatalf("calls resolver=%d search=%d generator=%d", resolver.calls, search.calls, generator.calls)
	}
	if search.query != "地质灾害 损失影响 官方通报" || result.Authority.ID != validReference().ID ||
		result.AuthoritySHA256 == "" || result.AuthorityEnvelopeVersion != AuthorityEnvelopeVersion {
		t.Fatalf("Authority 或固定搜索词错误: query=%q result=%+v", search.query, result)
	}
	if string(generator.input.AnalysisJSON) != string(result.Authority.AnalysisJSON) ||
		!sameStringValues(generator.input.ImmutableFields, result.Authority.ImmutableFields) {
		t.Fatalf("大模型没有收到规范 Authority: %+v", generator.input)
	}
	if err = result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateRejectsResolverFailuresBeforeProviders(t *testing.T) {
	for _, test := range []struct {
		name      string
		input     Input
		resolver  *resolverStub
		wantError error
	}{
		{name: "不存在", input: validInput(), resolver: &resolverStub{err: domain.ErrNotFound}, wantError: domain.ErrNotFound},
		{name: "resolver 对象损坏", input: validInput(), resolver: &resolverStub{err: fmt.Errorf("读取持久对象: %w", domain.ErrInvalidInput)}, wantError: ErrInvalidAuthority},
		{name: "不支持类型", input: Input{AnalysisRef: report.AnalysisReference{Kind: "browser_json", ID: "x"}}, resolver: &resolverStub{}, wantError: domain.ErrInvalidInput},
		{name: "kind-id 不一致", input: validInput(), resolver: &resolverStub{value: authorityWithID("loss-2")}, wantError: ErrInvalidAuthority},
	} {
		t.Run(test.name, func(t *testing.T) {
			search, generator := &searchStub{}, &generatorStub{}
			service := newService(t, test.resolver, search, generator)
			_, err := service.Generate(context.Background(), test.input)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Generate() error = %v", err)
			}
			if search.calls != 0 || generator.calls != 0 {
				t.Fatalf("resolver 失败后仍调用供应商: search=%d generator=%d", search.calls, generator.calls)
			}
		})
	}
}

func TestGenerateRejectsAuthorityBindingMismatchBeforeProviders(t *testing.T) {
	for _, test := range bindingMismatchCases() {
		t.Run(test.name, func(t *testing.T) {
			search, generator := &searchStub{}, &generatorStub{}
			service := newService(t, &resolverStub{value: test.authority}, search, generator)
			_, err := service.Generate(context.Background(), Input{AnalysisRef: test.reference})
			if !errors.Is(err, ErrInvalidAuthority) {
				t.Fatalf("Generate() error = %v", err)
			}
			if search.calls != 0 || generator.calls != 0 {
				t.Fatalf("绑定失败后仍调用供应商: search=%d generator=%d", search.calls, generator.calls)
			}
		})
	}
}

func TestGenerateRejectsUnsafeStoredFieldsBeforeProviders(t *testing.T) {
	for _, field := range []string{"name", "phone", "detailedAddress", "internalCostCoefficient"} {
		t.Run(field, func(t *testing.T) {
			value := validLossAuthority()
			var object map[string]any
			if err := json.Unmarshal(value.AnalysisJSON, &object); err != nil {
				t.Fatal(err)
			}
			object[field] = "张三 13800138000 四川省某街道1号"
			value.AnalysisJSON = marshalAnalysis(object)
			search, generator := &searchStub{}, &generatorStub{}
			service := newService(t, &resolverStub{value: value}, search, generator)
			_, err := service.Generate(context.Background(), validInput())
			if !errors.Is(err, ErrUnsafeStoredAnalysis) {
				t.Fatalf("Generate() error = %v", err)
			}
			if search.calls != 0 || generator.calls != 0 {
				t.Fatalf("不安全 Authority 仍调用供应商: search=%d generator=%d", search.calls, generator.calls)
			}
		})
	}
}

func TestGenerateRejectsSensitiveAuthorityValuesBeforeProviders(t *testing.T) {
	analysis := validRouteAnalysis()
	analysis.RouteID = "联系电话13800138000"
	value := authority(report.AuthorityEvacuationRoute, "route-analysis-1", "route-v1", report.AuthoritySchemaRouteV1, analysis)
	search, generator := &searchStub{}, &generatorStub{}
	service := newService(t, &resolverStub{value: value}, search, generator)

	_, err := service.Generate(context.Background(), Input{AnalysisRef: reference(report.AuthorityEvacuationRoute, "route-analysis-1")})
	if !errors.Is(err, ErrUnsafeStoredAnalysis) {
		t.Fatalf("Generate() error = %v", err)
	}
	if search.calls != 0 || generator.calls != 0 {
		t.Fatalf("含个人信息的 Authority 仍调用供应商: search=%d generator=%d", search.calls, generator.calls)
	}
}

func TestAuthoritySHA256UsesCanonicalEnvelope(t *testing.T) {
	first := validLossAuthority()
	first.AnalysisJSON = []byte(` {"status":"available","snapshotId":"snapshot-1","impactAreaSquareMeters":100,"formulaVersion":"loss-v1","confidenceBand":"high","confidence":0.8,"conditionalLowCents":"1000","conditionalHighCents":"3000","conditionalCentralCents":"2000","assessmentId":"loss-1","affectedPopulation":10} `)
	second := validLossAuthority()
	second.ResolvedAt = second.ResolvedAt.Add(time.Hour)
	second.AnalysisJSON = []byte(`{"affectedPopulation":1e1,"assessmentId":"loss-1","conditionalCentralCents":"2000","conditionalHighCents":"3000","conditionalLowCents":"1000","confidence":8e-1,"confidenceBand":"high","formulaVersion":"loss-v1","impactAreaSquareMeters":1e2,"snapshotId":"snapshot-1","status":"available"}`)

	firstResult := generateWithoutProviders(t, first)
	secondResult := generateWithoutProviders(t, second)
	if firstResult.AuthoritySHA256 != secondResult.AuthoritySHA256 ||
		string(firstResult.Authority.AnalysisJSON) != string(secondResult.Authority.AnalysisJSON) {
		t.Fatalf("语义相同 Authority 摘要不稳定: %s != %s", firstResult.AuthoritySHA256, secondResult.AuthoritySHA256)
	}
	changed := secondResult.Authority
	changed.Version = "loss-v2"
	var analysis report.LossAuthorityAnalysis
	if err := json.Unmarshal(changed.AnalysisJSON, &analysis); err != nil {
		t.Fatal(err)
	}
	analysis.FormulaVersion = "loss-v2"
	changed.AnalysisJSON, changed.ImmutableFields = marshalAnalysis(analysis), nil
	changed, err := changed.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	changedDigest, err := authoritySHA256(changed)
	if err != nil || changedDigest == firstResult.AuthoritySHA256 {
		t.Fatalf("版本变化未改变摘要: digest=%s err=%v", changedDigest, err)
	}
}

func TestSurvivalAuthoritySHA256BindsReplayFields(t *testing.T) {
	first := validSurvivalAnalysis()
	firstAuthority := authority(report.AuthoritySurvivalAssessment, first.AssessmentID,
		first.ModelVersion, report.AuthoritySchemaSurvivalV1, first)
	firstCanonical, err := firstAuthority.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, err := authoritySHA256(firstAuthority)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*report.SurvivalAuthorityAnalysis){
		func(value *report.SurvivalAuthorityAnalysis) {
			value.Factors = []string{"相同分数对应另一组确定性因素"}
		},
		func(value *report.SurvivalAuthorityAnalysis) {
			value.Limitations = []string{"相同分数对应另一组确定性限制"}
		},
	} {
		second := first
		mutate(&second)
		assertSurvivalAuthorityChanged(t, firstCanonical, firstDigest, second)
	}
}

func assertSurvivalAuthorityChanged(t *testing.T, first report.Authority, firstDigest string,
	second report.SurvivalAuthorityAnalysis,
) {
	t.Helper()
	secondAuthority := authority(report.AuthoritySurvivalAssessment, second.AssessmentID,
		second.ModelVersion, report.AuthoritySchemaSurvivalV1, second)
	secondCanonical, err := secondAuthority.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := authoritySHA256(secondAuthority)
	if err != nil || secondDigest == firstDigest || bytes.Equal(first.AnalysisJSON, secondCanonical.AnalysisJSON) {
		t.Fatalf("回放因素或限制未绑定 Authority: first=%s second=%s err=%v", firstDigest, secondDigest, err)
	}
}

func TestGenerateDoesNotSendPIIToSearchOrLLM(t *testing.T) {
	unsafeEvidence := validEvidence()
	unsafeEvidence.Title = "姓名：张三"
	unsafeEvidence.Summary = "联系电话 13800138000，详细地址为某街道1号"
	search := &searchStub{values: []report.Evidence{unsafeEvidence, validEvidence()}}
	generator := &generatorStub{value: validNarrative("说明")}
	service := newService(t, &resolverStub{value: validLossAuthority()}, search, generator)
	result, err := service.Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if containsAny(search.query, "张三", "13800138000", "街道") || len(generator.input.Evidence) != 1 {
		t.Fatalf("个人信息进入供应商输入: query=%q evidence=%+v", search.query, generator.input.Evidence)
	}
	inputJSON := string(generator.input.AnalysisJSON)
	if containsAny(inputJSON, "name", "phone", "address", "internal") || len(result.Evidence) != 1 {
		t.Fatalf("Authority 或证据泄露内部字段: analysis=%s evidence=%+v", inputJSON, result.Evidence)
	}
}

func TestGenerateMinimizesUnstructuredPIIBeforeExternalUse(t *testing.T) {
	evidence := validEvidence()
	evidence.Title = "救援对象张三"
	evidence.Summary = "证件 E12345678，现居成都市某路27号"
	evidence.URL = "https://zhangsan-e12345678.mnr.gov.cn/rescue/person?address=road-27"
	evidence.Source.SourceURI = "https://www.mnr.gov.cn/search?resident=road-27"
	generator := &generatorStub{value: validNarrative("说明")}
	service := newService(t, &resolverStub{value: validLossAuthority()},
		&searchStub{values: []report.Evidence{evidence}}, generator)
	result, err := service.Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(struct {
		Result Result                `json:"result"`
		Input  report.NarrativeInput `json:"input"`
	}{Result: result, Input: generator.input})
	for _, sensitive := range []string{"张三", "zhangsan", "E12345678", "e12345678", "某路27号", "/rescue/person", "road-27"} {
		if bytes.Contains(payload, []byte(sensitive)) {
			t.Fatalf("非结构化个人信息进入外发或响应证据: %q", sensitive)
		}
	}
	if len(result.Evidence) != 1 || len(generator.input.Evidence) != 1 ||
		result.Evidence[0].URL != "https://mnr.gov.cn/" || result.Evidence[0].SiteName != "mnr.gov.cn" ||
		result.Evidence[0].Source.SourceURI != "https://mnr.gov.cn/" {
		t.Fatalf("证据未按公开主机最小化: result=%+v input=%+v", result.Evidence, generator.input.Evidence)
	}
}

func TestGenerateDropsEvidenceWithoutBoundTrustedBase(t *testing.T) {
	for _, flags := range [][]string{
		{"trusted_domain"},
		{report.TrustedDomainQualityFlagPrefix + "mem.gov.cn"},
		{report.TrustedDomainQualityFlagPrefix + "mnr.gov.cn", report.TrustedDomainQualityFlagPrefix + "mem.gov.cn"},
	} {
		evidence := validEvidence()
		evidence.Source.QualityFlags = flags
		generator := &generatorStub{value: validNarrative("说明")}
		service := newService(t, &resolverStub{value: validLossAuthority()},
			&searchStub{values: []report.Evidence{evidence}}, generator)
		result, err := service.Generate(context.Background(), validInput())
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Evidence) != 0 || len(generator.input.Evidence) != 0 ||
			!strings.Contains(strings.Join(result.Limitations, "|"), "无法安全最小化") {
			t.Fatalf("未绑定可信基域的证据未被丢弃: flags=%v result=%+v", flags, result)
		}
	}
}

func TestGenerateDropsNonPublicEvidenceBeforeExternalUse(t *testing.T) {
	for _, rawURL := range []string{
		"https://127.0.0.1/private", "https://127.1/private", "https://[::1]/private",
		"https://metadata.internal/private", "https://localhost/private",
	} {
		t.Run(rawURL, func(t *testing.T) {
			evidence := validEvidence()
			evidence.URL = rawURL
			generator := &generatorStub{value: validNarrative("说明")}
			service := newService(t, &resolverStub{value: validLossAuthority()},
				&searchStub{values: []report.Evidence{evidence}}, generator)
			result, err := service.Generate(context.Background(), validInput())
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Evidence) != 0 || len(generator.input.Evidence) != 0 ||
				!strings.Contains(strings.Join(result.Limitations, "|"), "校验失败") {
				t.Fatalf("非公开证据未在外发前丢弃: result=%+v input=%+v", result.Evidence, generator.input.Evidence)
			}
		})
	}
}

func TestContainsSensitiveTextCoversCommonContactFormats(t *testing.T) {
	for _, value := range []string{
		"咨询电话 138-0013-8000", "联系方式 138 0013 8000", "座机 010-12345678",
		"联系电话为01012345678", "报警电话是(010) 1234-5678", "(010) 1234-5678",
		"手机号", "电话", "contact@example.test", strings.Repeat("x", maxSensitiveScanRunes+1),
	} {
		if !containsSensitiveText(value) {
			t.Fatalf("未识别敏感联系方式: %q", value)
		}
	}
	for _, value := range []string{
		"公开灾害通报", "通信设施暂时中断", "编号 20260828001", "电话会议", "手机端页面",
		"电话为主要通信手段", "联系电话恢复服务",
	} {
		if containsSensitiveText(value) {
			t.Fatalf("非敏感文本被误判: %q", value)
		}
	}
}

func TestGenerateDegradesProviderTimeoutsWhenRequestContextIsAlive(t *testing.T) {
	search := &searchStub{err: context.DeadlineExceeded}
	generator := &generatorStub{err: context.DeadlineExceeded}
	service := newService(t, &resolverStub{value: validLossAuthority()}, search, generator)
	result, err := service.Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if result.EvidenceAvailable || result.NarrativeAvailable || len(result.Limitations) != 2 {
		t.Fatalf("供应商超时未降级: %+v", result)
	}
	assertNarrativeWire(t, result.Narrative, false)
}

func TestGenerateKeepsEvidenceArrayNonNilWhenSearchDegrades(t *testing.T) {
	badSite := validEvidence()
	badSite.SiteName = strings.Repeat("站", 257)
	badRequestID := validEvidence()
	badRequestID.Source.ProviderRequestID = strings.Repeat("r", 257)
	for _, search := range []*searchStub{{err: errors.New("search failed")}, {values: []report.Evidence{badSite, badRequestID}}} {
		service := newService(t, &resolverStub{value: validLossAuthority()}, search, &generatorStub{value: validNarrative("说明")})
		result, err := service.Generate(context.Background(), validInput())
		if err != nil {
			t.Fatal(err)
		}
		payload, marshalErr := json.Marshal(result)
		if marshalErr != nil || result.Evidence == nil || result.EvidenceAvailable ||
			!strings.Contains(string(payload), `"evidence":[]`) || result.Authority.ID != "loss-1" {
			t.Fatalf("降级结果不稳定: result=%+v payload=%s error=%v", result, payload, marshalErr)
		}
	}
}

func TestGenerateStopsOnCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resolver, search, generator := &resolverStub{value: validLossAuthority()}, &searchStub{}, &generatorStub{}
	service := newService(t, resolver, search, generator)
	_, err := service.Generate(ctx, validInput())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate() error = %v", err)
	}
	if resolver.calls != 0 || search.calls != 0 || generator.calls != 0 {
		t.Fatalf("调用方取消后仍访问依赖: resolver=%d search=%d generator=%d", resolver.calls, search.calls, generator.calls)
	}
}

func TestGenerateKeepsNarrativeWireStableAcrossDegradation(t *testing.T) {
	for _, test := range degradationCases() {
		t.Run(test.name, func(t *testing.T) {
			service := newService(t, &resolverStub{value: validLossAuthority()}, test.search, test.generator)
			result, err := service.Generate(context.Background(), validInput())
			if err != nil {
				t.Fatal(err)
			}
			if err = result.Validate(); err != nil {
				t.Fatal(err)
			}
			assertNarrativeWire(t, result.Narrative, test.sourceExpected)
		})
	}
}

func TestGenerateFiltersTemporalInvalidEvidenceBeforeNarrative(t *testing.T) {
	now := fixedClock{}.Now()
	for _, test := range temporalEvidenceCases(now) {
		t.Run(test.name, func(t *testing.T) {
			generator := &generatorStub{value: validNarrative("说明")}
			service := newService(t, &resolverStub{value: test.authority},
				&searchStub{values: []report.Evidence{test.evidence}}, generator)
			result, err := service.Generate(context.Background(), validInput())
			if err != nil {
				t.Fatal(err)
			}
			if len(generator.input.Evidence) != 0 || len(result.Evidence) != 0 || !result.NarrativeAvailable ||
				!strings.Contains(strings.Join(result.Limitations, "|"), "时间契约无效") {
				t.Fatalf("异常证据未在 LLM 前降级: input=%+v result=%+v", generator.input.Evidence, result)
			}
			if err = result.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGenerateDegradesNarrativeOlderThanUsedEvidence(t *testing.T) {
	now := fixedClock{}.Now()
	authority := validLossAuthority()
	authority.ResolvedAt = now.Add(-2 * time.Hour)
	evidence := validEvidence()
	evidence.CrawledAt, evidence.Source.FetchedAt = now.Add(-time.Hour), now.Add(-time.Hour)
	narrative := validNarrative("说明")
	narrative.GeneratedAt, narrative.Source.FetchedAt = now.Add(-90*time.Minute), now.Add(-90*time.Minute)
	service := newService(t, &resolverStub{value: authority}, &searchStub{values: []report.Evidence{evidence}},
		&generatorStub{value: narrative})
	result, err := service.Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if result.NarrativeAvailable || len(result.Evidence) != 1 ||
		!strings.Contains(strings.Join(result.Limitations, "|"), "未通过校验") {
		t.Fatalf("倒序 narrative 未降级: %+v", result)
	}
	if err = result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateAcceptsHistoricalCrawledAtBeforeAuthority(t *testing.T) {
	evidence := validEvidence()
	evidence.CrawledAt = fixedClock{}.Now().Add(-24 * time.Hour)
	generator := &generatorStub{value: validNarrative("说明")}
	service := newService(t, &resolverStub{value: validLossAuthority()},
		&searchStub{values: []report.Evidence{evidence}}, generator)
	result, err := service.Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(generator.input.Evidence) != 1 || len(result.Evidence) != 1 || !result.NarrativeAvailable {
		t.Fatalf("历史抓取时间被错误降级: input=%+v result=%+v", generator.input.Evidence, result)
	}
	if err = result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateDegradesOversizedNarrativeModel(t *testing.T) {
	for _, size := range []int{257, (1 << 20) - 1024} {
		t.Run(fmt.Sprintf("长度_%d", size), func(t *testing.T) {
			narrative := validNarrative("说明")
			narrative.Model = strings.Repeat("m", size)
			service := newService(t, &resolverStub{value: validLossAuthority()}, nil, &generatorStub{value: narrative})
			result, err := service.Generate(context.Background(), validInput())
			if err != nil {
				t.Fatal(err)
			}
			if result.NarrativeAvailable || result.Narrative.Available || len(result.Narrative.KeyFindings) != 0 ||
				len(result.Narrative.Actions) != 0 || len(result.Narrative.Caveats) != 0 {
				t.Fatalf("异常模型名未降级: %+v", result.Narrative)
			}
			if err = result.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGenerateSetsReportTimeAfterNarrative(t *testing.T) {
	base := fixedClock{}.Now()
	clock := &sequenceClock{values: []time.Time{base.Add(time.Minute), base.Add(2 * time.Minute)}}
	generator := &clockedGenerator{clock: clock}
	service, err := New(&resolverStub{value: validLossAuthority()}, nil, generator, clock)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Narrative.GeneratedAt.Before(result.GeneratedAt) || clock.index != 2 {
		t.Fatalf("时间顺序错误: narrative=%s report=%s calls=%d", result.Narrative.GeneratedAt, result.GeneratedAt, clock.index)
	}
	if err = result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestResultValidateRejectsFutureComponentTimes(t *testing.T) {
	base := generateWithoutProviders(t, validLossAuthority())
	for _, test := range resultFutureTimeCases(base) {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("Validate() error=%v", err)
			}
		})
	}
}

func TestResultValidateRejectsComponentsBeforeAuthority(t *testing.T) {
	base := generateWithoutProviders(t, validLossAuthority())
	for _, test := range resultPastTimeCases(base) {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("Validate() error=%v", err)
			}
		})
	}
}

func TestResultValidateRejectsNullTopLevelArrays(t *testing.T) {
	base := generateWithoutProviders(t, validLossAuthority())
	for _, mutate := range []func(*Result){
		func(value *Result) { value.Evidence = nil },
		func(value *Result) { value.Limitations = nil },
	} {
		value := base
		mutate(&value)
		if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("Validate() error=%v", err)
		}
	}
}

func TestGenerateEnforcesRequestEvidenceLimit(t *testing.T) {
	values := distinctEvidence(maxEvidenceLimit + 2)
	for _, limit := range []int{1, defaultEvidenceLimit, maxEvidenceLimit} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			generator := &generatorStub{value: validNarrative("说明")}
			service := newService(t, concurrentResolverStub{}, &searchStub{values: values}, generator)
			input := validInput()
			input.EvidenceLimit = limit
			result, err := service.Generate(context.Background(), input)
			if err != nil || len(result.Evidence) != limit || len(generator.input.Evidence) != limit {
				t.Fatalf("请求证据上限未生效: limit=%d result=%d input=%d error=%v",
					limit, len(result.Evidence), len(generator.input.Evidence), err)
			}
			expected := fmt.Sprintf("截取前 %d 条", limit)
			if !containsAny(strings.Join(result.Limitations, "\n"), expected) {
				t.Fatalf("证据截断限制未记录实际请求上限: %v", result.Limitations)
			}
		})
	}
}

func TestGenerateMinimizesEvidenceBeforeNarrative(t *testing.T) {
	values := make([]report.Evidence, maxEvidenceLimit)
	for index := range values {
		values[index] = budgetEvidence(index)
		host := fmt.Sprintf("source-%d.example.test", index)
		values[index].URL = "https://" + host + "/item"
		values[index].Source.QualityFlags[0] = report.TrustedDomainQualityFlagPrefix + host
	}
	generator := &generatorStub{value: validNarrative("说明")}
	service := newService(t, concurrentResolverStub{}, &searchStub{values: values}, generator)
	input := validInput()
	input.EvidenceLimit = maxEvidenceLimit
	result, err := service.Generate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != len(values) ||
		len(generator.input.Evidence) != len(result.Evidence) ||
		generator.input.Evidence[len(result.Evidence)-1].URL != result.Evidence[len(result.Evidence)-1].URL {
		t.Fatalf("最小化证据与 LLM 输入不一致: result=%d input=%d", len(result.Evidence), len(generator.input.Evidence))
	}
	payload, marshalErr := json.Marshal(result)
	if marshalErr != nil || len(payload) >= 1<<20 ||
		!strings.Contains(strings.Join(result.Limitations, "|"), "不发送 URL、主机") {
		t.Fatalf("最小化结果无效: bytes=%d limitations=%v err=%v", len(payload), result.Limitations, marshalErr)
	}
}

func TestGeneratePreservesDistinctEvidenceAfterMinimization(t *testing.T) {
	first, second := validEvidence(), validEvidence()
	first.URL = "https://www.mnr.gov.cn/news/first"
	second.URL = "https://www.mnr.gov.cn/news/second?query=value"
	first.Source.SourceRevision = "sha256:" + strings.Repeat("a", 64)
	second.Source.SourceRevision = "sha256:" + strings.Repeat("b", 64)
	first.Source.SHA256, second.Source.SHA256 = strings.Repeat("c", 64), strings.Repeat("d", 64)
	first.Source.ProviderRequestID, second.Source.ProviderRequestID = "search-1", "search-1"
	service := newService(t, &resolverStub{value: validLossAuthority()},
		&searchStub{values: []report.Evidence{first, second}}, nil)
	result, err := service.Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 2 || result.Evidence[0].URL != "https://mnr.gov.cn/" ||
		result.Evidence[1].URL != "https://mnr.gov.cn/" ||
		result.Evidence[0].Source.SourceRevision == result.Evidence[1].Source.SourceRevision ||
		result.Evidence[0].Source.SHA256 != first.Source.SHA256 ||
		result.Evidence[1].Source.SHA256 != second.Source.SHA256 ||
		result.Evidence[0].Source.ProviderRequestID == "search-1" ||
		result.Evidence[0].Source.ProviderRequestID != result.Evidence[1].Source.ProviderRequestID {
		t.Fatalf("同域不同证据未保留独立审计绑定: evidence=%+v", result.Evidence)
	}
	payload, marshalErr := json.Marshal(result.Evidence)
	if marshalErr != nil || bytes.Contains(payload, []byte("search-1")) {
		t.Fatalf("供应商请求 ID 原值进入公开响应: payload=%s err=%v", payload, marshalErr)
	}
}

func TestGenerateDeduplicatesSameEvidenceByAuditReference(t *testing.T) {
	evidence := validEvidence()
	evidence.Source.SourceRevision = "sha256:" + strings.Repeat("c", 64)
	service := newService(t, &resolverStub{value: validLossAuthority()},
		&searchStub{values: []report.Evidence{evidence, evidence}}, nil)
	result, err := service.Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 1 || !strings.HasPrefix(result.Evidence[0].Source.SourceRevision, "sha256:") ||
		!strings.Contains(strings.Join(result.Limitations, "|"), "1 条证据条目重复") {
		t.Fatalf("相同证据未按不可逆引用稳定去重: evidence=%+v limitations=%v", result.Evidence, result.Limitations)
	}
}

func TestEvidenceReferenceIsStableAcrossCrawlAndBatchRefresh(t *testing.T) {
	first := validEvidence()
	first.Source.PublishedAt = fixedClock{}.Now().Add(-time.Hour)
	second := first
	second.CrawledAt = fixedClock{}.Now().Add(-time.Minute)
	second.Source.SHA256, second.Source.ProviderRequestID = strings.Repeat("f", 64), "batch-2"
	second.Source.DatasetVersion, second.Source.License = "供应商刷新版本", "供应商刷新许可"
	firstReference, firstErr := evidenceAuditReference(first)
	secondReference, secondErr := evidenceAuditReference(second)
	if firstErr != nil || secondErr != nil || firstReference != secondReference {
		t.Fatalf("同一条目随抓取或批次审计漂移: first=%s second=%s errors=%v/%v",
			firstReference, secondReference, firstErr, secondErr)
	}
	second.Summary = "正文摘要已变化"
	changedReference, changedErr := evidenceAuditReference(second)
	if changedErr != nil || changedReference == firstReference {
		t.Fatalf("稳定正文身份变化未生成新引用: first=%s changed=%s error=%v",
			firstReference, changedReference, changedErr)
	}
	identicalReference, identicalErr := evidenceAuditReference(first)
	if identicalErr != nil || identicalReference != firstReference {
		t.Fatalf("完全相同条目未保持稳定引用: first=%s identical=%s error=%v",
			firstReference, identicalReference, identicalErr)
	}
}

func TestGenerateDoesNotExposeFreeTextProvenanceFields(t *testing.T) {
	evidence := validEvidence()
	evidence.Source.DatasetVersion = "张三"
	evidence.Source.ProviderRequestID = "E12345678"
	evidence.Source.License = "成都市武侯区人民南路四段27号"
	generator := &generatorStub{value: validNarrative("说明")}
	service := newService(t, &resolverStub{value: validLossAuthority()},
		&searchStub{values: []report.Evidence{evidence}}, generator)
	result, err := service.Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	payload, marshalErr := json.Marshal(struct {
		Result Result                `json:"result"`
		Input  report.NarrativeInput `json:"input"`
	}{Result: result, Input: generator.input})
	for _, sensitive := range []string{"张三", "E12345678", "人民南路四段27号"} {
		if bytes.Contains(payload, []byte(sensitive)) {
			t.Fatalf("自由文本 provenance 进入响应或 LLM 输入: %q payload=%s", sensitive, payload)
		}
	}
	if marshalErr != nil || len(result.Evidence) != 1 ||
		result.Evidence[0].Source.DatasetVersion != publicEvidenceDatasetVersion ||
		result.Evidence[0].Source.License != publicEvidenceLicenseStatement ||
		!strings.HasPrefix(result.Evidence[0].Source.ProviderRequestID, "sha256:") ||
		result.Evidence[0].Source.SHA256 != evidence.Source.SHA256 {
		t.Fatalf("公开 provenance 未使用固定值和独立摘要: evidence=%+v err=%v",
			result.Evidence, marshalErr)
	}
}

func TestGenerateDegradesConcurrentProviderQueueWithinStageBudget(t *testing.T) {
	search := &queuedSearchStub{limiter: rate.NewLimiter(rate.Every(200*time.Millisecond), 1)}
	service := newService(t, concurrentResolverStub{}, search, nil)
	service.searchTTL, service.llmTTL = 40*time.Millisecond, 40*time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	const workers = 14
	errorsChannel := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := service.Generate(ctx, validInput())
			if err == nil && result.Authority.ID != validReference().ID {
				err = fmt.Errorf("Authority 未保留")
			}
			errorsChannel <- err
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestGenerateRejectsProviderSuccessAfterStageDeadline(t *testing.T) {
	for _, test := range []struct {
		name      string
		search    ports.EvidenceSearcher
		generator ports.NarrativeGenerator
	}{
		{name: "search", search: &lateSearchStub{delay: 15 * time.Millisecond}},
		{name: "llm", generator: &lateGeneratorStub{delay: 15 * time.Millisecond}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := newService(t, concurrentResolverStub{}, test.search, test.generator)
			service.searchTTL, service.llmTTL = 5*time.Millisecond, 5*time.Millisecond
			result, err := service.Generate(context.Background(), validInput())
			if err != nil || result.Authority.ID != validReference().ID {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if result.EvidenceAvailable || result.NarrativeAvailable {
				t.Fatalf("迟到供应商结果未降级: %+v", result)
			}
		})
	}
}

func TestContextCompletionErrorDetectsDeadlineBeforeErrPublication(t *testing.T) {
	ctx := pendingDeadlineContext{
		Context:  context.Background(),
		deadline: time.Now().Add(-time.Second),
	}
	if err := contextCompletionError(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline 已过但 Err 尚未发布时 error=%v", err)
	}
}

func TestGenerateRejectsExpiredParentWithoutCallingResolver(t *testing.T) {
	resolver := &resolverStub{value: validLossAuthority()}
	service := newService(t, resolver, nil, nil)
	ctx := pendingDeadlineContext{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	result, err := service.Generate(ctx, validInput())
	if !errors.Is(err, context.DeadlineExceeded) || resolver.calls != 0 {
		t.Fatalf("过期父请求未在入口拒绝: calls=%d result=%+v error=%v", resolver.calls, result, err)
	}
	assertEmptyAgentResult(t, result)
}

func TestGenerateStopsDownstreamAfterDependencyCancelsParent(t *testing.T) {
	t.Run("resolver", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		resolver := &resolverStub{value: validLossAuthority(), cancel: cancel}
		search, generator := &searchStub{}, &generatorStub{}
		result, err := newService(t, resolver, search, generator).Generate(ctx, validInput())
		assertCanceledAgentResult(t, result, err)
		if resolver.calls != 1 || search.calls != 0 || generator.calls != 0 {
			t.Fatalf("resolver cancel 后仍调用下游: %d/%d/%d", resolver.calls, search.calls, generator.calls)
		}
	})
	t.Run("search", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		search := &searchStub{values: []report.Evidence{validEvidence()}, cancel: cancel}
		generator := &generatorStub{value: validNarrative("说明")}
		result, err := newService(t, concurrentResolverStub{}, search, generator).Generate(ctx, validInput())
		assertCanceledAgentResult(t, result, err)
		if search.calls != 1 || generator.calls != 0 {
			t.Fatalf("search cancel 后仍调用 LLM: %d/%d", search.calls, generator.calls)
		}
	})
	t.Run("llm", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		generator := &generatorStub{value: validNarrative("说明"), cancel: cancel}
		result, err := newService(t, concurrentResolverStub{}, nil, generator).Generate(ctx, validInput())
		assertCanceledAgentResult(t, result, err)
		if generator.calls != 1 {
			t.Fatalf("LLM 调用次数=%d", generator.calls)
		}
	})
}

func TestGenerateFinalGateRejectsCancellationAfterNarrative(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clock := cancelingClock{now: fixedClock{}.Now(), cancel: cancel}
	service, err := New(concurrentResolverStub{}, nil,
		&generatorStub{value: validNarrative("说明")}, clock)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Generate(ctx, validInput())
	assertCanceledAgentResult(t, result, err)
}

func TestGenerateRejectsPendingParentDeadlineAfterSearch(t *testing.T) {
	deadline := time.Now().Add(20 * time.Millisecond)
	ctx := pendingDeadlineContext{Context: context.Background(), deadline: deadline}
	search := &deadlineSearchStub{deadline: deadline}
	generator := &generatorStub{value: validNarrative("说明")}
	result, err := newService(t, concurrentResolverStub{}, search, generator).Generate(ctx, validInput())
	assertDeadlineAgentResult(t, result, err)
	if search.calls != 1 || generator.calls != 0 {
		t.Fatalf("父 deadline 后仍调用下游: search=%d generator=%d", search.calls, generator.calls)
	}
}

func TestGenerateRejectsPendingParentDeadlineAfterNarrative(t *testing.T) {
	deadline := time.Now().Add(20 * time.Millisecond)
	ctx := pendingDeadlineContext{Context: context.Background(), deadline: deadline}
	generator := &deadlineGeneratorStub{deadline: deadline}
	result, err := newService(t, concurrentResolverStub{}, nil, generator).Generate(ctx, validInput())
	assertDeadlineAgentResult(t, result, err)
	if generator.calls != 1 {
		t.Fatalf("LLM 调用次数=%d", generator.calls)
	}
}

func TestGenerateFinalGateRejectsPendingParentDeadline(t *testing.T) {
	deadline := time.Now().Add(20 * time.Millisecond)
	ctx := pendingDeadlineContext{Context: context.Background(), deadline: deadline}
	clock := deadlineClock{deadline: deadline, now: fixedClock{}.Now()}
	service, err := New(concurrentResolverStub{}, nil,
		&generatorStub{value: validNarrative("说明")}, clock)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Generate(ctx, validInput())
	assertDeadlineAgentResult(t, result, err)
}

func TestGenerateParentDeadlineWinsMatchingStageDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	search := &successAfterContextDoneSearchStub{}
	generator := &generatorStub{value: validNarrative("说明")}
	service := newService(t, concurrentResolverStub{}, search, generator)
	service.searchTTL = 20 * time.Millisecond
	result, err := service.Generate(ctx, validInput())
	if !errors.Is(err, context.DeadlineExceeded) || search.calls != 1 || generator.calls != 0 {
		t.Fatalf("父/阶段 deadline 优先级错误: search=%d generator=%d result=%+v error=%v",
			search.calls, generator.calls, result, err)
	}
	assertEmptyAgentResult(t, result)
}

func TestGenerateDetachesGeneratorInputFromReturnedResult(t *testing.T) {
	search := &searchStub{values: []report.Evidence{validEvidence()}}
	generator := &generatorStub{value: validNarrative("说明")}
	result, err := newService(t, concurrentResolverStub{}, search, generator).Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	before := marshalAgentResult(t, result)
	digest := result.AuthoritySHA256
	generator.input.AnalysisJSON[0] = '['
	generator.input.ImmutableFields[0] = "tampered"
	generator.input.Evidence[0].Source.QualityFlags[0] = "tampered"
	generator.input.Evidence[0].Source.Limitations[0] = "tampered"
	assertStableAgentResult(t, result, before, digest)
}

func TestGenerateIsolatesNarrativeInputDuringGeneratorCall(t *testing.T) {
	search := &searchStub{values: []report.Evidence{validEvidence()}}
	generator := &mutatingInputGeneratorStub{}
	result, err := newService(t, concurrentResolverStub{}, search, generator).Generate(context.Background(), validInput())
	if err != nil || !result.NarrativeAvailable || generator.calls != 1 {
		t.Fatalf("调用期间输入改写影响了结果: calls=%d result=%+v error=%v", generator.calls, result, err)
	}
	if err = result.Validate(); err != nil {
		t.Fatalf("调用期间输入改写后结果无效: %v", err)
	}
}

func TestGenerateUsesNonNilEvidenceArrayWhenSearchDegrades(t *testing.T) {
	cases := []struct {
		name   string
		search ports.EvidenceSearcher
		stage  bool
	}{
		{name: "unconfigured"},
		{name: "provider_error", search: &searchStub{err: errors.New("provider down")}},
		{name: "stage_deadline", search: &successAfterContextDoneSearchStub{}, stage: true},
		{name: "no_valid_evidence", search: &searchStub{values: []report.Evidence{{}}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			generator := &generatorStub{value: validNarrative("说明")}
			service := newService(t, concurrentResolverStub{}, test.search, generator)
			if test.stage {
				service.searchTTL = time.Millisecond
			}
			result, err := service.Generate(context.Background(), validInput())
			assertNonNilEmptyEvidence(t, generator.input, result, err)
		})
	}
}

func TestGenerateBoundsSearchResultsBeforeProcessing(t *testing.T) {
	template := report.Evidence{Source: provenance.Provenance{
		QualityFlags: []string{"quality"}, Limitations: []string{"limitation"},
		SourceParts: []provenance.SourcePart{{Reference: "https://example.test/part"}},
	}}
	values := make([]report.Evidence, 10_000)
	for index := range values {
		values[index] = template
	}
	service := newService(t, concurrentResolverStub{}, &searchStub{values: values}, nil)
	input := validInput()
	input.EvidenceLimit = maxEvidenceLimit
	result, err := service.Generate(context.Background(), input)
	if err != nil || result.Validate() != nil ||
		!containsAny(strings.Join(result.Limitations, "\n"), "截取前 20 条") {
		t.Fatalf("搜索数量上限未生效: result=%+v error=%v", result, err)
	}
	var runErr error
	allocations := testing.AllocsPerRun(3, func() {
		_, runErr = service.Generate(context.Background(), input)
	})
	if runErr != nil || allocations >= 1_000 {
		t.Fatalf("搜索尾部在上限前被处理: allocations=%.0f error=%v", allocations, runErr)
	}
}

func TestGenerateRejectsOversizedEvidenceShapeWithoutCopying(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*report.Evidence)
	}{
		{name: "quality_flags", mutate: func(value *report.Evidence) { value.Source.QualityFlags = make([]string, 1<<20) }},
		{name: "limitations", mutate: func(value *report.Evidence) { value.Source.Limitations = make([]string, 1<<20) }},
		{name: "source_parts", mutate: func(value *report.Evidence) { value.Source.SourceParts = make([]provenance.SourcePart, 1<<17) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			evidence := validEvidence()
			test.mutate(&evidence)
			service := newService(t, concurrentResolverStub{}, &searchStub{values: []report.Evidence{evidence}}, nil)
			allocated, result, err := measureAgentAllocation(func() (Result, error) {
				return service.Generate(context.Background(), validInput())
			})
			if err != nil || len(result.Evidence) != 0 || allocated >= 8<<20 {
				t.Fatalf("超大证据形状在校验前被复制: bytes=%d result=%+v error=%v", allocated, result, err)
			}
			if err = result.Validate(); err != nil {
				t.Fatalf("超大证据降级结果无效: %v", err)
			}
		})
	}
}

func TestGenerateDetachesGeneratorOutputFromReturnedResult(t *testing.T) {
	narrative := validNarrative("说明")
	narrative.KeyFindings, narrative.Actions, narrative.Caveats = []string{"发现"}, []string{"行动"}, []string{"限制"}
	narrative.Source.QualityFlags, narrative.Source.Limitations = []string{"quality"}, []string{"limitation"}
	narrative.Source.SourceParts = []provenance.SourcePart{{Reference: "https://example.test/part", Revision: "v1", SizeBytes: 1}}
	narrative.Source.SourceRevision = provenance.CompositeSourceRevision(narrative.Source.SourceParts)
	generator := &generatorStub{value: narrative}
	result, err := newService(t, concurrentResolverStub{}, nil, generator).Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	before := marshalAgentResult(t, result)
	digest := result.AuthoritySHA256
	generator.value.KeyFindings[0], generator.value.Actions[0], generator.value.Caveats[0] = "改", "改", "改"
	generator.value.Source.QualityFlags[0], generator.value.Source.Limitations[0] = "改", "改"
	generator.value.Source.SourceParts[0].Reference = "https://example.test/tampered"
	assertStableAgentResult(t, result, before, digest)
}

func TestGenerateResultHasNoRaceWithProviderRetainedValues(t *testing.T) {
	narrative := validNarrative("说明")
	narrative.KeyFindings, narrative.Actions, narrative.Caveats = []string{"发现"}, []string{"行动"}, []string{"限制"}
	narrative.Source.QualityFlags, narrative.Source.Limitations = []string{"quality"}, []string{"limitation"}
	search := &searchStub{values: []report.Evidence{validEvidence()}}
	generator := &generatorStub{value: narrative}
	result, err := newService(t, concurrentResolverStub{}, search, generator).Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	before, digest := marshalAgentResult(t, result), result.AuthoritySHA256
	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := 0; index < 1000; index++ {
			search.values[0].Title = fmt.Sprintf("search-%d", index)
			search.values[0].Source.QualityFlags[0] = fmt.Sprintf("search-quality-%d", index)
			generator.input.AnalysisJSON[0] = byte('[' + index%2)
			generator.input.Evidence[0].Source.QualityFlags[0] = fmt.Sprintf("input-%d", index)
			generator.value.KeyFindings[0] = fmt.Sprintf("output-%d", index)
			generator.value.Source.Limitations[0] = fmt.Sprintf("source-%d", index)
		}
	}()
	for {
		select {
		case <-done:
			assertStableAgentResult(t, result, before, digest)
			return
		default:
			assertStableAgentResult(t, result, before, digest)
		}
	}
}

type bindingCase struct {
	name      string
	reference report.AnalysisReference
	authority report.Authority
}

func bindingMismatchCases() []bindingCase {
	hazard := report.HazardAuthorityAnalysis{AffectedAreaSquareMeters: 10, ConfidenceLevel: "high", DataStatus: "current", HazardType: "landslide", RiskLevel: "high", RiskZoneCount: 1, RuleVersion: "rule-v2", SnapshotID: "snapshot-1"}
	routeSnapshot := validRouteAnalysis()
	routeSnapshot.SnapshotID = ""
	routeRank := validRouteAnalysis()
	routeRank.Rank = 0
	routeID := validRouteAnalysis()
	routeID.RouteAnalysisID = "route-analysis-2"
	loss := validLossAnalysis()
	loss.FormulaVersion = "loss-v2"
	survival := validSurvivalAnalysis()
	survival.HumanReviewStatus = "optional"
	survivalID := validSurvivalAnalysis()
	survivalID.AssessmentID = sha256AgentTest("c")
	survivalAssessmentID := validSurvivalAssessmentID()
	return []bindingCase{
		{name: "风险规则", reference: reference(report.AuthorityHazardSnapshot, "snapshot-1"), authority: authority(report.AuthorityHazardSnapshot, "snapshot-1", "rule-v1", report.AuthoritySchemaHazardV1, hazard)},
		{name: "路线快照", reference: reference(report.AuthorityEvacuationRoute, "route-analysis-1"), authority: authority(report.AuthorityEvacuationRoute, "route-analysis-1", "route-v1", report.AuthoritySchemaRouteV1, routeSnapshot)},
		{name: "路线排名", reference: reference(report.AuthorityEvacuationRoute, "route-analysis-1"), authority: authority(report.AuthorityEvacuationRoute, "route-analysis-1", "route-v1", report.AuthoritySchemaRouteV1, routeRank)},
		{name: "路线分析标识", reference: reference(report.AuthorityEvacuationRoute, "route-analysis-1"), authority: authority(report.AuthorityEvacuationRoute, "route-analysis-1", "route-v1", report.AuthoritySchemaRouteV1, routeID)},
		{name: "损失公式", reference: validReference(), authority: authority(report.AuthorityLossAssessment, "loss-1", "loss-v1", report.AuthoritySchemaLossV1, loss)},
		{name: "生还复核", reference: reference(report.AuthoritySurvivalAssessment, survivalAssessmentID), authority: authority(report.AuthoritySurvivalAssessment, survivalAssessmentID, survivaldomain.ModelVersion, report.AuthoritySchemaSurvivalV1, survival)},
		{name: "生还评估标识", reference: reference(report.AuthoritySurvivalAssessment, survivalAssessmentID), authority: authority(report.AuthoritySurvivalAssessment, survivalAssessmentID, survivaldomain.ModelVersion, report.AuthoritySchemaSurvivalV1, survivalID)},
	}
}

type degradationCase struct {
	name           string
	search         ports.EvidenceSearcher
	generator      ports.NarrativeGenerator
	sourceExpected bool
}

type resultMutationCase struct {
	name   string
	mutate func(*Result)
}

func degradationCases() []degradationCase {
	return []degradationCase{
		{name: "未配置供应商"},
		{name: "搜索失败", search: &searchStub{err: errors.New("search failed")}, generator: &generatorStub{value: validNarrative("说明")}, sourceExpected: true},
		{name: "LLM 503", generator: &generatorStub{err: domain.ErrProviderUnavailable}},
		{name: "LLM 非法结构", generator: &generatorStub{value: report.Narrative{Available: true}}},
	}
}

func resultFutureTimeCases(base Result) []resultMutationCase {
	future := base.GeneratedAt.Add(time.Minute)
	return []resultMutationCase{
		{name: "Authority 解析时间", mutate: func(value *Result) { value.Authority.ResolvedAt = future }},
		{name: "证据抓取时间", mutate: func(value *Result) {
			evidence := validEvidence()
			evidence.CrawledAt = future
			value.Evidence, value.EvidenceAvailable = []report.Evidence{evidence}, true
		}},
		{name: "证据来源获取时间", mutate: func(value *Result) {
			evidence := validEvidence()
			evidence.Source.FetchedAt = future
			value.Evidence, value.EvidenceAvailable = []report.Evidence{evidence}, true
		}},
		{name: "说明生成时间", mutate: func(value *Result) { value.Narrative.GeneratedAt = future }},
		{name: "说明来源获取时间", mutate: func(value *Result) {
			value.Narrative = normalizeNarrativeSlices(validNarrative("说明"))
			value.Narrative.Source.FetchedAt = future
			value.NarrativeAvailable = true
		}},
	}
}

func resultPastTimeCases(base Result) []resultMutationCase {
	past := base.Authority.ResolvedAt.Add(-time.Minute)
	return []resultMutationCase{
		{name: "说明生成时间", mutate: func(value *Result) { value.Narrative.GeneratedAt = past }},
		{name: "证据来源获取时间", mutate: func(value *Result) {
			evidence := validEvidence()
			evidence.Source.FetchedAt = past
			value.Evidence, value.EvidenceAvailable = []report.Evidence{evidence}, true
		}},
		{name: "说明来源获取时间", mutate: func(value *Result) {
			value.Narrative = normalizeNarrativeSlices(validNarrative("说明"))
			value.Narrative.Source.FetchedAt = past
			value.NarrativeAvailable = true
		}},
	}
}

func temporalEvidenceCases(now time.Time) []struct {
	name      string
	authority report.Authority
	evidence  report.Evidence
} {
	base, evidence := validLossAuthority(), validEvidence()
	futureSource := evidence
	futureSource.Source.FetchedAt = now.Add(time.Hour)
	futureCrawl := evidence
	futureCrawl.CrawledAt = now.Add(time.Hour)
	pastSource := evidence
	pastSource.Source.FetchedAt = now.Add(-time.Minute)
	invertedAuthority, inverted := base, evidence
	invertedAuthority.ResolvedAt = now.Add(-2 * time.Hour)
	inverted.Source.FetchedAt, inverted.CrawledAt = now.Add(-time.Hour), now.Add(-30*time.Minute)
	return []struct {
		name      string
		authority report.Authority
		evidence  report.Evidence
	}{
		{"来源时间在未来", base, futureSource}, {"抓取时间在未来", base, futureCrawl},
		{"来源早于 Authority", base, pastSource}, {"抓取晚于来源", invertedAuthority, inverted},
	}
}

func TestResultValidateRejectsEvidenceAfterNarrative(t *testing.T) {
	base := generateWithoutProviders(t, validLossAuthority())
	base.Narrative = normalizeNarrativeSlices(validNarrative("说明"))
	base.NarrativeAvailable = true
	base.GeneratedAt = base.Narrative.GeneratedAt.Add(time.Minute)
	for _, field := range []string{"crawledAt", "fetchedAt"} {
		t.Run(field, func(t *testing.T) {
			value := base
			evidence := validEvidence()
			evidence.CrawledAt = base.Narrative.GeneratedAt.Add(time.Second)
			evidence.Source.FetchedAt = base.Narrative.GeneratedAt.Add(time.Second)
			if field == "crawledAt" {
				evidence.Source.FetchedAt = base.Narrative.GeneratedAt
			} else {
				evidence.CrawledAt = base.Narrative.GeneratedAt
			}
			value.Evidence, value.EvidenceAvailable = []report.Evidence{evidence}, true
			if err := value.Validate(); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("Validate() error=%v", err)
			}
		})
	}
}

func assertNarrativeWire(t *testing.T, value report.Narrative, sourceExpected bool) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, field := range []string{`"keyFindings":[]`, `"actions":[]`, `"caveats":[]`} {
		if !strings.Contains(text, field) {
			t.Fatalf("Narrative 数组 wire 不稳定: %s", text)
		}
	}
	if sourceExpected != strings.Contains(text, `"source"`) {
		t.Fatalf("Narrative source 可选状态错误: %s", text)
	}
}

func generateWithoutProviders(t *testing.T, value report.Authority) Result {
	t.Helper()
	service := newService(t, &resolverStub{value: value}, nil, nil)
	result, err := service.Generate(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func newService(t *testing.T, resolver AuthoritativeAnalysisResolver,
	search ports.EvidenceSearcher, generator ports.NarrativeGenerator,
) *Service {
	t.Helper()
	service, err := New(resolver, search, generator, fixedClock{})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func validInput() Input { return Input{AnalysisRef: validReference()} }

func validReference() report.AnalysisReference {
	return reference(report.AuthorityLossAssessment, "loss-1")
}

func reference(kind report.AuthorityKind, id string) report.AnalysisReference {
	return report.AnalysisReference{Kind: kind, ID: id}
}

func validLossAuthority() report.Authority {
	return authority(report.AuthorityLossAssessment, "loss-1", "loss-v1", report.AuthoritySchemaLossV1, validLossAnalysis())
}

func authorityWithID(id string) report.Authority {
	analysis := validLossAnalysis()
	analysis.AssessmentID = id
	return authority(report.AuthorityLossAssessment, id, "loss-v1", report.AuthoritySchemaLossV1, analysis)
}

func authority(kind report.AuthorityKind, id, version, schema string, analysis any) report.Authority {
	return report.Authority{
		Kind: kind, ID: id, Version: version, SchemaVersion: schema,
		AnalysisJSON: marshalAnalysis(analysis), ResolvedAt: fixedClock{}.Now(),
	}
}

func validLossAnalysis() report.LossAuthorityAnalysis {
	return report.LossAuthorityAnalysis{
		AffectedPopulation: 10, AssessmentID: "loss-1", ConditionalCentralCents: "2000",
		ConditionalHighCents: "3000", ConditionalLowCents: "1000", Confidence: 0.8,
		ConfidenceBand: "high", FormulaVersion: "loss-v1", ImpactAreaSquareMeters: 100,
		SnapshotID: "snapshot-1", Status: "available",
	}
}

func validRouteAnalysis() report.RouteAuthorityAnalysis {
	return report.RouteAuthorityAnalysis{
		DistanceMeters: 1200, DurationSeconds: 600, Mode: "driving", Rank: 1,
		RiskScore: 10, RiskScoreAvailable: true, RouteAnalysisID: "route-analysis-1", RouteID: "provider-route-1",
		RuleVersion: "route-v1", SnapshotID: "snapshot-1",
	}
}

func validSurvivalAnalysis() report.SurvivalAuthorityAnalysis {
	return report.SurvivalAuthorityAnalysis{
		AssessmentID: validSurvivalAssessmentID(), CaseID: "case-1", HumanReviewStatus: "required",
		Factors:      []string{"失联时间处于四小时内", "搜救输入仍有缺口"},
		Limitations:  []string{"仅用于历史案例回放", "必须由专业人员复核"},
		ModelVersion: survivaldomain.ModelVersion, Priority: string(survivaldomain.PriorityUrgent),
		ProbabilityBand: string(survivaldomain.ProbabilityModerate), ProbabilityHigh: 0.59, ProbabilityLow: 0.35,
		ScenarioDigest: sha256AgentTest("b"), ScenarioID: "scenario-1", Score: 60,
		ScoreBand: "moderate", Usage: survivaldomain.HistoricalReplayUsage(),
	}
}

func validSurvivalAssessmentID() string { return sha256AgentTest("a") }

func sha256AgentTest(char string) string { return "sha256:" + strings.Repeat(char, 64) }

func marshalAnalysis(value any) json.RawMessage {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return payload
}

func validNarrative(summary string) report.Narrative {
	return report.Narrative{
		Summary: summary, GeneratedAt: fixedClock{}.Now(), Model: "gpt-5.6-terra",
		Available: true, Source: validSource("llm"),
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func sameStringValues(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func assertEmptyAgentResult(t *testing.T, value Result) {
	t.Helper()
	if !reflect.DeepEqual(value, Result{}) {
		t.Fatalf("取消路径发布了部分结果: %+v", value)
	}
}

func assertCanceledAgentResult(t *testing.T, value Result, err error) {
	t.Helper()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("父取消未透传: %v", err)
	}
	assertEmptyAgentResult(t, value)
}

func assertDeadlineAgentResult(t *testing.T, value Result, err error) {
	t.Helper()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("父 deadline 未透传: %v", err)
	}
	assertEmptyAgentResult(t, value)
}

func assertNonNilEmptyEvidence(t *testing.T, input report.NarrativeInput, result Result, err error) {
	t.Helper()
	if err != nil || input.Evidence == nil || result.Evidence == nil || len(input.Evidence) != 0 {
		t.Fatalf("降级证据数组契约错误: input=%+v result=%+v error=%v", input.Evidence, result.Evidence, err)
	}
	payload, marshalErr := json.Marshal(input)
	if marshalErr != nil || !bytes.Contains(payload, []byte(`"evidence":[]`)) {
		t.Fatalf("降级证据 wire 不是空数组: payload=%s error=%v", payload, marshalErr)
	}
}

func measureAgentAllocation(run func() (Result, error)) (uint64, Result, error) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	result, err := run()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc, result, err
}

func marshalAgentResult(t *testing.T, value Result) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func assertStableAgentResult(t *testing.T, value Result, before []byte, digest string) {
	t.Helper()
	after := marshalAgentResult(t, value)
	if !bytes.Equal(before, after) || value.AuthoritySHA256 != digest {
		t.Fatalf("供应商迟到改写了已返回结果: before=%s after=%s", before, after)
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("供应商迟到改写后结果校验失败: %v", err)
	}
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC) }

type cancelingClock struct {
	now    time.Time
	cancel context.CancelFunc
}

func (c cancelingClock) Now() time.Time {
	c.cancel()
	return c.now
}

type sequenceClock struct {
	values []time.Time
	index  int
}

func (c *sequenceClock) Now() time.Time {
	if len(c.values) == 0 {
		return time.Time{}
	}
	if c.index >= len(c.values) {
		return c.values[len(c.values)-1]
	}
	value := c.values[c.index]
	c.index++
	return value
}

type resolverStub struct {
	value  report.Authority
	err    error
	calls  int
	cancel context.CancelFunc
}

func (s *resolverStub) Resolve(_ context.Context, _ report.AnalysisReference) (report.Authority, error) {
	s.calls++
	if s.cancel != nil {
		s.cancel()
	}
	return s.value, s.err
}

type searchStub struct {
	values []report.Evidence
	err    error
	calls  int
	query  string
	cancel context.CancelFunc
}

func (s *searchStub) Search(_ context.Context, query string, _ int) ([]report.Evidence, error) {
	s.calls++
	s.query = query
	if s.cancel != nil {
		s.cancel()
	}
	return s.values, s.err
}

type generatorStub struct {
	value  report.Narrative
	err    error
	input  report.NarrativeInput
	calls  int
	cancel context.CancelFunc
}

type mutatingInputGeneratorStub struct{ calls int }

func (s *mutatingInputGeneratorStub) Generate(_ context.Context, input report.NarrativeInput) (report.Narrative, error) {
	s.calls++
	input.AnalysisJSON[0] = '['
	input.ImmutableFields[0] = "tampered"
	input.Evidence[0].Source.FetchedAt = fixedClock{}.Now().Add(time.Hour)
	return validNarrative("说明"), nil
}

type clockedGenerator struct{ clock ports.Clock }

func (g *clockedGenerator) Generate(_ context.Context, _ report.NarrativeInput) (report.Narrative, error) {
	now := g.clock.Now().UTC()
	value := normalizeNarrativeSlices(validNarrative("说明"))
	value.GeneratedAt, value.Source.FetchedAt = now, now.Add(-time.Minute)
	return value, nil
}

func (s *generatorStub) Generate(_ context.Context, input report.NarrativeInput) (report.Narrative, error) {
	s.calls++
	s.input = input
	if s.cancel != nil {
		s.cancel()
	}
	return s.value, s.err
}

func validEvidence() report.Evidence {
	value := report.Evidence{
		Title: "公开通报", URL: "https://www.mnr.gov.cn/news/1", Summary: "公开摘要",
		Source: validSource("bocha"),
	}
	value.Source.DatasetVersion = "v1"
	value.Source.SourceRevision = "sha256:" + strings.Repeat("d", 64)
	value.Source.License = "供应商服务条款"
	value.Source.SHA256 = strings.Repeat("e", 64)
	value.Source.ProviderRequestID = "search-request-1"
	value.Source.QualityFlags = []string{report.TrustedDomainQualityFlagPrefix + "mnr.gov.cn"}
	return value
}

func distinctEvidence(count int) []report.Evidence {
	values := make([]report.Evidence, count)
	for index := range values {
		values[index] = validEvidence()
		host := fmt.Sprintf("news-%d.example.test", index)
		values[index].URL = "https://" + host + "/item"
		values[index].Source.QualityFlags = []string{report.TrustedDomainQualityFlagPrefix + host}
	}
	return values
}

func budgetEvidence(index int) report.Evidence {
	value := validEvidence()
	value.Title, value.Summary, value.SiteName = strings.Repeat("标", 512), strings.Repeat("摘", 4096), strings.Repeat("站", 256)
	value.URL = fmt.Sprintf("https://www.mnr.gov.cn/news/%d", index)
	value.Source.QualityFlags, value.Source.Limitations = make([]string, 20), make([]string, 20)
	value.Source.QualityFlags[0] = report.TrustedDomainQualityFlagPrefix + "example.test"
	for item := 1; item < len(value.Source.QualityFlags); item++ {
		value.Source.QualityFlags[item] = strings.Repeat("质", 400)
	}
	for item := range value.Source.Limitations {
		value.Source.Limitations[item] = strings.Repeat("限", 500)
	}
	return value
}

type concurrentResolverStub struct{}

func (concurrentResolverStub) Resolve(context.Context, report.AnalysisReference) (report.Authority, error) {
	return validLossAuthority(), nil
}

type queuedSearchStub struct{ limiter *rate.Limiter }

func (s *queuedSearchStub) Search(ctx context.Context, _ string, _ int) ([]report.Evidence, error) {
	if err := s.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

type successAfterContextDoneSearchStub struct{ calls int }

func (s *successAfterContextDoneSearchStub) Search(ctx context.Context, _ string, _ int) ([]report.Evidence, error) {
	s.calls++
	<-ctx.Done()
	return []report.Evidence{validEvidence()}, nil
}

type deadlineSearchStub struct {
	deadline time.Time
	calls    int
}

func (s *deadlineSearchStub) Search(context.Context, string, int) ([]report.Evidence, error) {
	s.calls++
	waitPastDeadline(s.deadline)
	return []report.Evidence{validEvidence()}, nil
}

type deadlineGeneratorStub struct {
	deadline time.Time
	calls    int
}

func (s *deadlineGeneratorStub) Generate(context.Context, report.NarrativeInput) (report.Narrative, error) {
	s.calls++
	waitPastDeadline(s.deadline)
	return validNarrative("说明"), nil
}

type deadlineClock struct {
	deadline time.Time
	now      time.Time
}

func (c deadlineClock) Now() time.Time {
	waitPastDeadline(c.deadline)
	return c.now
}

func waitPastDeadline(deadline time.Time) {
	if delay := time.Until(deadline) + time.Millisecond; delay > 0 {
		time.Sleep(delay)
	}
}

type lateSearchStub struct{ delay time.Duration }

func (s *lateSearchStub) Search(context.Context, string, int) ([]report.Evidence, error) {
	time.Sleep(s.delay)
	return []report.Evidence{validEvidence()}, nil
}

type lateGeneratorStub struct{ delay time.Duration }

func (g *lateGeneratorStub) Generate(context.Context, report.NarrativeInput) (report.Narrative, error) {
	time.Sleep(g.delay)
	return validNarrative("迟到说明"), nil
}

type pendingDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c pendingDeadlineContext) Deadline() (time.Time, bool) {
	return c.deadline, true
}

func validSource(provider string) provenance.Provenance {
	return provenance.Provenance{
		Provider: provider, Dataset: "test", SourceURI: "https://example.test/source",
		DataKind: provenance.DataKindObservation, FetchedAt: fixedClock{}.Now(),
	}
}
