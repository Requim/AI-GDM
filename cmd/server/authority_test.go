package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	authorityadapter "github.com/Requim/AI-GDM/internal/adapters/authority"
	"github.com/Requim/AI-GDM/internal/application/agent"
	applicationsurvival "github.com/Requim/AI-GDM/internal/application/survival"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/report"
	"github.com/Requim/AI-GDM/internal/platform/config"
	"github.com/Requim/AI-GDM/internal/platform/httpserver"
)

func TestNewAIServiceInjectsAuthorityResolver(t *testing.T) {
	resolver := fixedAuthorityResolver{}
	service, err := newAIService(config.Config{}, nil, resolver, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if service == nil {
		t.Fatal("智能研判服务不能为空")
	}
}

func TestNewAIHandlerSkipsWithoutAuthorityResolver(t *testing.T) {
	handler, err := newAIHandler(config.Config{}, nil, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if handler != nil {
		t.Fatal("缺少权威 resolver 时不应挂载 AI API")
	}
}

func TestNoDatabaseAuthorityResolverMountsSurvivalAI(t *testing.T) {
	harness := newNoDatabaseAuthorityHarness(t)
	response := postAuthorityReport(t, harness.handler, report.AuthoritySurvivalAssessment,
		harness.replay.AssessmentID)
	if response.Code != http.StatusOK {
		t.Fatalf("survival AI status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			Authority report.Authority `json:"authority"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Authority.Kind != report.AuthoritySurvivalAssessment ||
		payload.Data.Authority.ID != harness.replay.AssessmentID {
		t.Fatalf("survival authority=%+v", payload.Data.Authority)
	}
}

func TestNoDatabaseAuthorityResolverReportsDatabaseBranchesUnavailable(t *testing.T) {
	harness := newNoDatabaseAuthorityHarness(t)
	cases := []struct {
		kind report.AuthorityKind
		id   string
	}{
		{kind: report.AuthorityHazardSnapshot, id: "snapshot-no-database"},
		{kind: report.AuthorityLossAssessment, id: "loss-no-database"},
	}
	for _, item := range cases {
		t.Run(string(item.kind), func(t *testing.T) {
			_, err := harness.resolver.Resolve(context.Background(), report.AnalysisReference{
				Kind: item.kind, ID: item.id,
			})
			if !errors.Is(err, domain.ErrProviderUnavailable) {
				t.Fatalf("Resolve() error=%v", err)
			}
			response := postAuthorityReport(t, harness.handler, item.kind, item.id)
			if response.Code != http.StatusServiceUnavailable || !bytes.Contains(response.Body.Bytes(),
				[]byte(`"code":"provider_unavailable"`)) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestNewAuthorityResolverRejectsIncompleteDatabaseRuntime(t *testing.T) {
	survival, err := newSurvivalRuntime(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := newAuthorityResolver(&hazardRuntime{database: &pgxpool.Pool{}}, survival, nil)
	if err == nil || resolver != nil {
		t.Fatalf("resolver=%v error=%v", resolver, err)
	}
}

type noDatabaseAuthorityHarness struct {
	handler  http.Handler
	resolver *authorityadapter.Resolver
	replay   applicationsurvival.ReplayAssessment
}

func newNoDatabaseAuthorityHarness(t *testing.T) noDatabaseAuthorityHarness {
	t.Helper()
	logger := slog.Default()
	survival, err := newSurvivalRuntime(logger)
	if err != nil {
		t.Fatal(err)
	}
	cases, err := survival.catalog.ListCases(context.Background())
	if err != nil || len(cases) == 0 {
		t.Fatalf("cases=%d error=%v", len(cases), err)
	}
	replay, err := survival.assessment.AssessCase(context.Background(), cases[0].Event.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := newAuthorityResolver(nil, survival, nil)
	if err != nil || resolver == nil {
		t.Fatalf("resolver=%v error=%v", resolver, err)
	}
	aiHandler, err := newAIHandler(config.Config{}, nil, resolver, logger)
	if err != nil || aiHandler == nil {
		t.Fatalf("ai handler=%v error=%v", aiHandler, err)
	}
	server := httpserver.New("127.0.0.1:0", time.Second, logger)
	if err = mountApplicationAPI(server, nil, config.Config{}, nil, logger,
		aiHandler, survival.handler, resolver); err != nil {
		t.Fatal(err)
	}
	return noDatabaseAuthorityHarness{handler: server.Handler(), resolver: resolver, replay: replay}
}

func postAuthorityReport(t *testing.T, handler http.Handler, kind report.AuthorityKind,
	id string,
) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"analysisRef": map[string]any{"kind": kind, "id": id}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ai/report", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

var _ agent.AuthoritativeAnalysisResolver = fixedAuthorityResolver{}

type fixedAuthorityResolver struct{}

func (fixedAuthorityResolver) Resolve(context.Context, report.AnalysisReference) (report.Authority, error) {
	return report.Authority{}, nil
}
