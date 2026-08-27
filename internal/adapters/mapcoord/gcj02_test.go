package mapcoord

import (
	"errors"
	"math"
	"testing"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

func TestTransformerConvertsBeijingAndApproximatelyReverses(t *testing.T) {
	t.Parallel()
	transformer := New()
	original := spatial.Point{Longitude: 116.3974, Latitude: 39.9093}
	forward, err := transformer.Convert(original, spatial.CoordinateWGS84, spatial.CoordinateGCJ02)
	if err != nil {
		t.Fatalf("WGS84 转 GCJ-02: %v", err)
	}
	if !forward.Transformed || math.Abs(forward.Point.Longitude-original.Longitude) < 0.001 {
		t.Fatalf("未产生预期偏移: %+v", forward)
	}
	back, err := transformer.Convert(forward.Point, spatial.CoordinateGCJ02, spatial.CoordinateWGS84)
	if err != nil {
		t.Fatalf("GCJ-02 反向转换: %v", err)
	}
	if math.Abs(back.Point.Longitude-original.Longitude) > 1e-6 ||
		math.Abs(back.Point.Latitude-original.Latitude) > 1e-6 {
		t.Fatalf("反向误差过大: original=%+v back=%+v", original, back.Point)
	}
}

func TestTransformerLeavesOutsideChinaUnchanged(t *testing.T) {
	transformer := New()
	point := spatial.Point{Longitude: -73.9857, Latitude: 40.7484}
	result, err := transformer.Convert(point, spatial.CoordinateWGS84, spatial.CoordinateGCJ02)
	if err != nil {
		t.Fatalf("境外点转换: %v", err)
	}
	if result.Transformed || result.Point != point || len(result.Limitations) == 0 {
		t.Fatalf("境外点处理不符合约定: %+v", result)
	}
}

func TestTransformerHandlesChinaBoundaryAndSameSystem(t *testing.T) {
	transformer := New()
	for _, point := range []spatial.Point{
		{Longitude: minChinaLon, Latitude: minChinaLat},
		{Longitude: maxChinaLon, Latitude: maxChinaLat},
	} {
		result, err := transformer.Convert(point, spatial.CoordinateWGS84, spatial.CoordinateGCJ02)
		if err != nil || !result.Transformed {
			t.Fatalf("边界点未转换 point=%+v result=%+v err=%v", point, result, err)
		}
	}
	point := spatial.Point{Longitude: 10, Latitude: 10}
	result, err := transformer.Convert(point, spatial.CoordinateWGS84, spatial.CoordinateWGS84)
	if err != nil || result.Transformed || result.Point != point {
		t.Fatalf("同坐标系转换错误: %+v, %v", result, err)
	}
}

func TestTransformerRejectsUnsupportedAndNonFiniteInputs(t *testing.T) {
	transformer := New()
	if !errors.Is(mustConvertError(transformer, spatial.Point{Longitude: 1, Latitude: 1},
		spatial.CoordinateSystem("EPSG:3857"), spatial.CoordinateWGS84), domain.ErrInvalidInput) {
		t.Fatal("应拒绝不支持的坐标系")
	}
	if !errors.Is(mustConvertError(transformer, spatial.Point{Longitude: math.NaN(), Latitude: 1},
		spatial.CoordinateWGS84, spatial.CoordinateGCJ02), domain.ErrInvalidInput) {
		t.Fatal("应拒绝非有限坐标")
	}
}

func TestTransformerBatchIsOrderedAndAtomicOnError(t *testing.T) {
	transformer := New()
	points := []spatial.Point{{Longitude: 116.4, Latitude: 39.9}, {Longitude: -73, Latitude: 40}}
	results, err := transformer.ConvertBatch(points, spatial.CoordinateWGS84, spatial.CoordinateGCJ02)
	if err != nil || len(results) != len(points) || results[1].Point != points[1] {
		t.Fatalf("批量结果错误: results=%+v err=%v", results, err)
	}
	results, err = transformer.ConvertBatch([]spatial.Point{points[0], {Longitude: 181}},
		spatial.CoordinateWGS84, spatial.CoordinateGCJ02)
	if err == nil || results != nil || !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("批量失败应原子返回: results=%+v err=%v", results, err)
	}
}

func mustConvertError(transformer Transformer, point spatial.Point,
	from, to spatial.CoordinateSystem,
) error {
	_, err := transformer.Convert(point, from, to)
	return err
}
