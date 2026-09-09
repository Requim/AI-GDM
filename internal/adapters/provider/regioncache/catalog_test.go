package regioncache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
)

type cacheSource struct {
	regions []exposurecollection.AdministrativeRegion
	fail    string
}

func (s cacheSource) AdministrativeCatalog(context.Context) ([]exposurecollection.AdministrativeRegion, error) {
	return append([]exposurecollection.AdministrativeRegion(nil), s.regions...), nil
}

func (s cacheSource) AdministrativeBoundary(_ context.Context, r exposurecollection.AdministrativeRegion) (
	exposurecollection.AdministrativeRegion, error,
) {
	if r.Code == s.fail {
		return r, errors.New("test provider unavailable")
	}
	return r, nil
}

func TestCacheIsOfflineAndFailedRefreshPreservesSnapshot(t *testing.T) {
	ctx, path := context.Background(), filepath.Join(t.TempDir(), Filename)
	source := cacheSource{regions: cacheFixtures()}
	if err := Refresh(ctx, path, source); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source.fail = source.regions[len(source.regions)-1].Code
	if err = Refresh(ctx, path, source); err == nil {
		t.Fatal("部分边界失败却替换了缓存")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("失败刷新修改了旧缓存")
	}
	catalog, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	cities, err := catalog.RegionCatalog(ctx, "CHN", "ADM2")
	if err != nil || len(cities) != 34 || cities[0].ParentCode == "" {
		t.Fatalf("离线目录: %d %v", len(cities), err)
	}
	boundary, err := catalog.BoundaryForRegion(ctx, cities[0].Code)
	if err != nil || boundary.RegionCode != cities[0].Code {
		t.Fatalf("边界身份: %v", err)
	}
	boundary.Geometry[0] = 'x'
	again, err := catalog.BoundaryForRegion(ctx, cities[0].Code)
	if err != nil || again.Geometry[0] != '{' {
		t.Fatal("返回值修改污染缓存")
	}
}

func TestCacheRejectsInvalidHierarchyAndGeometryDigest(t *testing.T) {
	for _, name := range []string{"parent", "digest", "duplicate", "incomplete"} {
		t.Run(name, func(t *testing.T) {
			regions := cacheFixtures()
			switch name {
			case "parent":
				regions[1].ParentCode = "CN-999999"
			case "digest":
				regions[1].Digest = "modified"
			case "duplicate":
				regions[1].Code = regions[0].Code
			case "incomplete":
				regions = regions[:len(regions)-2]
			}
			if err := validateRegions(regions); err == nil {
				t.Fatal("接受了无效缓存")
			}
		})
	}
}

func cacheFixtures() []exposurecollection.AdministrativeRegion {
	raw := json.RawMessage(`{"type":"Polygon","coordinates":[[[116,39],[117,39],[117,40],[116,39]]]}`)
	sum := sha256.Sum256(raw)
	var regions []exposurecollection.AdministrativeRegion
	for index := 1; index <= 34; index++ {
		parent := fmt.Sprintf("CN-%02d0000", index)
		record := exposurecollection.AdministrativeRegion{Code: parent, Name: "测试省份", Level: "ADM1",
			BoundaryID: fmt.Sprintf("CHN-ADM1-AMAP-%02d0000", index), BoundaryYear: "amap-wgs84-v1",
			Digest: hex.EncodeToString(sum[:]), Geometry: raw, Source: "测试来源", License: "测试许可",
			Reference: "https://example.test/boundary", InputReferences: []string{"https://example.test/boundary"},
			CollectedAt: time.Now().UTC()}
		regions = append(regions, record)
		record.Code, record.ParentCode, record.Level = fmt.Sprintf("CN-%02d0100", index), parent, "ADM2"
		record.BoundaryID = fmt.Sprintf("CHN-ADM2-AMAP-%02d0100", index)
		regions = append(regions, record)
	}
	return regions
}
