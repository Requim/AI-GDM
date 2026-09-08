package gdal

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/artifactstore"
	"github.com/Requim/AI-GDM/internal/adapters/provider/geoboundaries"
	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/adapters/provider/nccs"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

// TestLiveNCCSNational 使用此前独立下载的真实文件，并在线验证其强版本身份后执行完整项目处理链。
func TestLiveNCCSNational(t *testing.T) {
	input := os.Getenv("NCCS_TEST_FILE")
	if input == "" {
		t.Skip("未配置 NCCS_TEST_FILE")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	client := httpclient.New(httpclient.Options{HTTPClient: &http.Client{Timeout: 30 * time.Second}, MaxAttempts: 1})
	root := t.TempDir()
	stored := loadNCCSFixture(t, ctx, client, input, root)
	boundaries, err := geoboundaries.New(geoboundaries.Options{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := boundaries.RiskBoundary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := New(Config{ArtifactRoot: root, NormalizeNCCS: true, BBox: [4]float64{73, 3, 136, 54}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, zones, err := processor.Process(ctx, stored, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Source.TransformVersion != PortalTransformVersion || snapshot.Coverage == nil {
		t.Fatalf("全国处理结果无效: %+v zones=%d", snapshot, len(zones))
	}
	t.Logf("snapshot=%s raw_sha256=%s boundary=%s zones=%d", snapshot.ID, stored.Provenance.SHA256, boundary.Coverage.BoundaryID, len(zones))
}

func loadNCCSFixture(t *testing.T, ctx context.Context, client *httpclient.Client, input, root string) provenance.Artifact {
	t.Helper()
	discovery, err := nccs.New(client, "", 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := discovery.DiscoverLatest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectedDigest := checkNCCSFixtureIdentity(t, artifact)
	file, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	stored, err := artifactstore.New(root, nccs.MaxFileBytes).Save(ctx, artifact, file)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Provenance.SHA256 != expectedDigest {
		t.Fatal("真实文件摘要不匹配已验证 POC 来源")
	}
	return stored
}

func checkNCCSFixtureIdentity(t *testing.T, artifact provenance.Artifact) string {
	t.Helper()
	payload, err := os.ReadFile(os.Getenv("NCCS_TEST_MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		URL  string `json:"url"`
		Size int64  `json:"size"`
		ETag string `json:"etag"`
	}
	if err = json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.URL != artifact.Reference || manifest.Size != artifact.SizeBytes || manifest.ETag != artifact.Provenance.SourceRevision {
		t.Fatal("POC 本地文件不匹配当前发现的源版本，不伪装实时数据")
	}
	payload, err = os.ReadFile(filepath.Join(filepath.Dir(os.Getenv("NCCS_TEST_MANIFEST")), "resumed.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		SHA256 string `json:"sha256"`
	}
	if err = json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	return result.SHA256
}
