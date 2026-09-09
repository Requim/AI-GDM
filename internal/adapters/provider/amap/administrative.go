package amap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

var administrativeCode = regexp.MustCompile(`^[0-9]{6}$`)

type districtRecord struct {
	Name      string           `json:"name"`
	Adcode    string           `json:"adcode"`
	Level     string           `json:"level"`
	Polyline  string           `json:"polyline"`
	Districts []districtRecord `json:"districts"`
}

type districtResponse struct {
	Districts []districtRecord `json:"districts"`
}

// AdministrativeCatalog 读取中文省市层级，密钥仅用于服务端请求。
func (p *Provider) AdministrativeCatalog(ctx context.Context) ([]exposurecollection.AdministrativeRegion, error) {
	body, _, _, err := p.do(ctx, "/v3/config/district", url.Values{
		"key": {p.apiKey}, "keywords": {"中国"}, "subdistrict": {"2"}, "extensions": {"base"},
	})
	if err != nil {
		return nil, fmt.Errorf("读取中文省市目录: %w", err)
	}
	var response districtResponse
	if err = decodeSuccess(body, &response); err != nil {
		return nil, err
	}
	if len(response.Districts) != 1 || len(response.Districts[0].Districts) != 34 {
		return nil, fmt.Errorf("%w: 全国省级目录不完整", domain.ErrProviderUnavailable)
	}
	var result []exposurecollection.AdministrativeRegion
	for _, province := range response.Districts[0].Districts {
		record, err := normalizeDistrict(province, "")
		if err != nil || province.Level != "province" {
			return nil, fmt.Errorf("%w: 省级目录层级无效", domain.ErrProviderUnavailable)
		}
		result = append(result, record)
		for _, city := range province.Districts {
			child, err := normalizeDistrict(city, record.Code)
			if err != nil {
				return nil, err
			}
			result = append(result, child)
		}
	}
	return result, nil
}

func normalizeDistrict(value districtRecord, parent string) (exposurecollection.AdministrativeRegion, error) {
	if !administrativeCode.MatchString(value.Adcode) || strings.TrimSpace(value.Name) == "" ||
		(value.Level != "province" && value.Level != "city" && value.Level != "district") {
		return exposurecollection.AdministrativeRegion{}, fmt.Errorf("%w: 中文行政区属性无效", domain.ErrProviderUnavailable)
	}
	level := "ADM1"
	if parent != "" {
		level = "ADM2"
	}
	return exposurecollection.AdministrativeRegion{Code: "CN-" + value.Adcode, ParentCode: parent,
		Name: value.Name, Level: level, BoundaryID: "CHN-" + level + "-AMAP-" + value.Adcode}, nil
}

// AdministrativeBoundary 下载单个行政区边界，并仅在地图适配器内由 GCJ-02 转为 WGS84。
func (p *Provider) AdministrativeBoundary(ctx context.Context, record exposurecollection.AdministrativeRegion) (
	exposurecollection.AdministrativeRegion, error,
) {
	code := strings.TrimPrefix(record.Code, "CN-")
	if !administrativeCode.MatchString(code) {
		return record, fmt.Errorf("%w: 行政区代码无效", domain.ErrInvalidInput)
	}
	body, _, _, err := p.do(ctx, "/v3/config/district", url.Values{
		"key": {p.apiKey}, "keywords": {code}, "subdistrict": {"0"}, "extensions": {"all"},
	})
	if err != nil {
		return record, fmt.Errorf("下载行政区边界 %s: %w", record.Code, err)
	}
	var response districtResponse
	if err = decodeSuccess(body, &response); err != nil {
		return record, err
	}
	if len(response.Districts) != 1 || response.Districts[0].Adcode != code ||
		response.Districts[0].Name != record.Name {
		return record, fmt.Errorf("%w: 行政区边界与目录不一致", domain.ErrProviderUnavailable)
	}
	geometry, err := p.districtGeometry(response.Districts[0].Polyline)
	if err != nil {
		return record, fmt.Errorf("转换行政区 %s 边界: %w", record.Code, err)
	}
	record.Geometry, record.CollectedAt = geometry, time.Now().UTC().Truncate(time.Microsecond)
	digest := sha256.Sum256(geometry)
	record.Digest, record.BoundaryYear = hex.EncodeToString(digest[:]), "amap-wgs84-v1"
	record.Source, record.License = providerName, "高德开放平台服务条款；非官方行政边界"
	record.Reference = DefaultBaseURL + "/v3/config/district?keywords=" + code + "&extensions=all"
	record.InputReferences = []string{record.Reference, "urn:ai-gdm:coordinate-transform:gcj02-wgs84"}
	return record, nil
}

func (p *Provider) districtGeometry(polyline string) (json.RawMessage, error) {
	if polyline == "" || len(polyline) > 2<<20 {
		return nil, fmt.Errorf("%w: 行政边界缺失或超限", domain.ErrProviderUnavailable)
	}
	polygons := make([][][][]float64, 0)
	for _, text := range strings.Split(polyline, "|") {
		var ring [][]float64
		for _, coordinate := range strings.Split(text, ";") {
			point, err := p.parseGCJPoint(coordinate)
			if err != nil {
				return nil, err
			}
			ring = append(ring, []float64{point.Longitude, point.Latitude})
		}
		if len(ring) < 3 {
			return nil, fmt.Errorf("%w: 行政边界环无效", domain.ErrProviderUnavailable)
		}
		last := ring[len(ring)-1]
		if last[0] != ring[0][0] || last[1] != ring[0][1] {
			ring = append(ring, ring[0])
		}
		polygons = append(polygons, [][][]float64{ring})
	}
	coordinates, err := json.Marshal(polygons)
	if err != nil {
		return nil, err
	}
	geometry := spatial.Geometry{Type: "MultiPolygon", Coordinates: coordinates}
	if err = geometry.ValidateArea(); err != nil {
		return nil, err
	}
	return json.Marshal(geometry)
}
