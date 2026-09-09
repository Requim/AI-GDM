// Package regioncache 保存经校验的中文省市目录及 WGS84 边界，业务请求只读本地快照。
package regioncache

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/spatial"
)

const Filename = "chn-province-city-v1.json.gz"
const maxCacheBytes = 128 << 20

var regionCodePattern = regexp.MustCompile(`^CN-[0-9]{6}$`)

// Source 仅供部署刷新器调用，业务请求不得调用供应商网络。
type Source interface {
	AdministrativeCatalog(context.Context) ([]exposurecollection.AdministrativeRegion, error)
	AdministrativeBoundary(context.Context, exposurecollection.AdministrativeRegion) (exposurecollection.AdministrativeRegion, error)
}

type fileSnapshot struct {
	Version     string
	GeneratedAt time.Time
	Digest      string
	Regions     []exposurecollection.AdministrativeRegion
}

// Catalog 是不可变的本地目录，启动时完整验证后共享给并发请求。
type Catalog struct {
	regions  []exposurecollection.AdministrativeRegion
	national exposurecollection.AdministrativeBoundaryProvider
}

// Open 校验并装入本地目录，不进行网络请求。
func Open(path string, national exposurecollection.AdministrativeBoundaryProvider) (*Catalog, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开中文省市缓存: %w", err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("%w: 省市缓存压缩格式无效", domain.ErrInsufficientData)
	}
	defer reader.Close()
	payload, err := io.ReadAll(io.LimitReader(reader, maxCacheBytes+1))
	if err != nil || len(payload) > maxCacheBytes {
		return nil, fmt.Errorf("%w: 省市缓存读取失败或超限", domain.ErrInsufficientData)
	}
	var snapshot fileSnapshot
	if json.Unmarshal(payload, &snapshot) != nil || snapshot.Version != "chn-province-city-v1" ||
		snapshot.GeneratedAt.IsZero() || digestRegions(snapshot.Regions) != snapshot.Digest {
		return nil, fmt.Errorf("%w: 省市缓存身份或摘要无效", domain.ErrInsufficientData)
	}
	if err = validateRegions(snapshot.Regions); err != nil {
		return nil, err
	}
	return &Catalog{regions: snapshot.Regions, national: national}, nil
}

// Refresh 完整下载目录及每个边界，全部验证成功后才替换旧缓存。
func Refresh(ctx context.Context, path string, source Source) error {
	regions, err := source.AdministrativeCatalog(ctx)
	if err != nil {
		return err
	}
	for index, region := range regions {
		regions[index], err = source.AdministrativeBoundary(ctx, region)
		if err != nil {
			return fmt.Errorf("缓存行政区 %s: %w", region.Code, err)
		}
		if regions[index].Code != region.Code || regions[index].ParentCode != region.ParentCode ||
			regions[index].Name != region.Name || regions[index].Level != region.Level {
			return fmt.Errorf("%w: 边界与目录身份不一致", domain.ErrInsufficientData)
		}
	}
	sort.Slice(regions, func(i, j int) bool { return regions[i].Code < regions[j].Code })
	if err = validateRegions(regions); err != nil {
		return err
	}
	value := fileSnapshot{Version: "chn-province-city-v1", GeneratedAt: time.Now().UTC(),
		Regions: regions, Digest: digestRegions(regions)}
	return replaceSnapshot(path, value)
}

func replaceSnapshot(path string, value fileSnapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".region-cache-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = file.Chmod(0o600); err != nil {
		return err
	}
	compressed := gzip.NewWriter(file)
	if err = json.NewEncoder(compressed).Encode(value); err != nil {
		_ = compressed.Close()
		return err
	}
	if err = compressed.Close(); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if _, err = Open(file.Name(), nil); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("替换中文省市缓存: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func validateRegions(regions []exposurecollection.AdministrativeRegion) error {
	parents, seen := make(map[string]bool), make(map[string]bool)
	if len(regions) < 34 || len(regions) > 1000 {
		return fmt.Errorf("%w: 省市目录数量不完整或超限", domain.ErrInsufficientData)
	}
	for _, region := range regions {
		var geometry spatial.Geometry
		digest := sha256.Sum256(region.Geometry)
		if seen[region.Code] || !regionCodePattern.MatchString(region.Code) || region.Name == "" ||
			region.Digest != hex.EncodeToString(digest[:]) ||
			len(region.Geometry) > exposurecollection.MaxAdministrativeBoundaryBytes ||
			json.Unmarshal(region.Geometry, &geometry) != nil || geometry.ValidateArea() != nil ||
			region.CollectedAt.IsZero() || region.Reference == "" || region.Source == "" ||
			region.License == "" || region.BoundaryYear == "" || len(region.InputReferences) == 0 ||
			region.BoundaryID != "CHN-"+region.Level+"-AMAP-"+region.Code[3:] {
			return fmt.Errorf("%w: 行政区 %s 身份或几何无效", domain.ErrInsufficientData, region.Code)
		}
		seen[region.Code] = true
		if region.Level == "ADM1" && region.ParentCode == "" {
			parents[region.Code] = true
		} else if region.Level != "ADM2" || region.ParentCode == "" {
			return fmt.Errorf("%w: 省市层级无效", domain.ErrInsufficientData)
		}
	}
	if len(parents) != 34 {
		return fmt.Errorf("%w: 全国省级目录不完整", domain.ErrInsufficientData)
	}
	for _, region := range regions {
		if region.Level == "ADM2" && !parents[region.ParentCode] {
			return fmt.Errorf("%w: 城市缺少所属省份", domain.ErrInsufficientData)
		}
	}
	return nil
}

func digestRegions(regions []exposurecollection.AdministrativeRegion) string {
	payload, _ := json.Marshal(regions)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// RegionCatalog 返回中文目录副本，父子代码来自同一份已校验本地快照。
func (c *Catalog) RegionCatalog(ctx context.Context, countryISO, level string) ([]exposurecollection.AdministrativeRegion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if countryISO != "CHN" || (level != "ADM1" && level != "ADM2") {
		return nil, fmt.Errorf("%w: 省市目录参数无效", domain.ErrInvalidInput)
	}
	result := make([]exposurecollection.AdministrativeRegion, 0)
	for _, record := range c.regions {
		if record.Level == level {
			record.Geometry = nil
			record.InputReferences = append([]string(nil), record.InputReferences...)
			result = append(result, record)
		}
	}
	return result, nil
}

// BoundaryForRegion 从本地中文行政区缓存读取 WGS84 边界。
func (c *Catalog) BoundaryForRegion(ctx context.Context, code string) (exposurecollection.AdministrativeBoundary, error) {
	if err := ctx.Err(); err != nil {
		return exposurecollection.AdministrativeBoundary{}, err
	}
	for _, record := range c.regions {
		if record.Code == code {
			return exposurecollection.AdministrativeBoundary{BoundaryID: record.BoundaryID,
				RegionCode: record.Code, BoundaryType: record.Level, BoundaryYear: record.BoundaryYear,
				Source: record.Source, License: record.License, Digest: record.Digest, Reference: record.Reference,
				CollectedAt: record.CollectedAt, Geometry: append(json.RawMessage(nil), record.Geometry...),
				InputReferences: append([]string(nil), record.InputReferences...)}, nil
		}
	}
	return exposurecollection.AdministrativeBoundary{}, domain.ErrNotFound
}

// Boundary 保留全国后台采集使用的原边界来源，不将城市边界冒充全国边界。
func (c *Catalog) Boundary(ctx context.Context) (exposurecollection.AdministrativeBoundary, error) {
	if c.national == nil {
		return exposurecollection.AdministrativeBoundary{}, domain.ErrInsufficientData
	}
	return c.national.Boundary(ctx)
}
