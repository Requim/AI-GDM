package exposurecollection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
)

type regionalGeometryStub struct {
	geometryStub
	region string
	called string
}

func (s *regionalGeometryStub) ReadExposureGeometryForRegion(_ context.Context, _, _ string,
	boundary AdministrativeBoundary,
) (GeometryInput, error) {
	s.called = boundary.RegionCode
	value := s.value
	value.Scope.Policy, value.Scope.RegionCode = RegionalScopePolicy, s.region
	return value, nil
}

func TestRegionReaderNeverFallsBackToNationalHotspot(t *testing.T) {
	fixture := newCollectorFixture(t)
	boundary := fixture.boundary.value
	boundary.RegionCode, boundary.BoundaryType = "CN-110000", "ADM1"
	if _, err := fixture.collector.readGeometry(context.Background(), "snapshot", "analysis", boundary);
		!errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("未配置区域读取却使用全国热点: %v", err)
	}
	reader := &regionalGeometryStub{geometryStub: *fixture.geometries, region: boundary.RegionCode}
	fixture.collector.geometries = reader
	if _, err := fixture.collector.readGeometry(context.Background(), "snapshot", "analysis", boundary);
		err != nil || reader.called != boundary.RegionCode {
		t.Fatalf("区域未传入几何读取: %v", err)
	}
	reader.region = "CN-310000"
	if _, err := fixture.collector.readGeometry(context.Background(), "snapshot", "analysis", boundary);
		!errors.Is(err, domain.ErrInsufficientData) {
		t.Fatalf("接受了其他地区的输入: %v", err)
	}
}

func TestRegionalScopeIdentityIncludesRegionAndRejectsTruncation(t *testing.T) {
	input := geometryFixture(time.Now().UTC())
	scope := input.Scope
	scope.Policy, scope.RegionCode = RegionalScopePolicy, "CN-110000"
	scope.Window = Bounds{West: 100, East: 120, South: 20, North: 40}
	if err := BindExposureScopeIdentity(&scope, input.Zones); err != nil {
		t.Fatal(err)
	}
	first := scope.ID
	scope.RegionCode = "CN-310000"
	if err := ValidateExposureScopeIdentity(scope, input.Zones); err == nil {
		t.Fatal("地区改变后旧摘要仍有效")
	}
	if err := BindExposureScopeIdentity(&scope, input.Zones); err != nil || scope.ID == first {
		t.Fatal("身份未绑定区域")
	}
	scope.TotalZoneCount++
	if err := BindExposureScopeIdentity(&scope, input.Zones); err == nil {
		t.Fatal("部分热点被标为完整行政区")
	}
}
