package geoboundaries

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/application/exposurecollection"
	"github.com/Requim/AI-GDM/internal/domain"
)

const (
	regionCatalogCacheSchema = "geoBoundaries-region-catalog-v1"
	maxRegionCacheBytes      = 64 << 20
)

// CachedRegionCatalog 使用服务器本地缓存提供行政区目录和区域边界。
type CachedRegionCatalog struct {
	source *Provider
	path   string
}

type regionCatalogCacheFile struct {
	SchemaVersion string                  `json:"schemaVersion"`
	CountryISO    string                  `json:"countryISO"`
	GeneratedAt   time.Time               `json:"generatedAt"`
	ADM1          regionCatalogCacheEntry `json:"adm1"`
	ADM2          regionCatalogCacheEntry `json:"adm2"`
}

type regionCatalogCacheEntry struct {
	Level        string    `json:"level"`
	BoundaryYear string    `json:"boundaryYear"`
	Source       string    `json:"source"`
	License      string    `json:"license"`
	References   []string  `json:"references"`
	Digest       string    `json:"digest"`
	CollectedAt  time.Time `json:"collectedAt"`
	Payload      []byte    `json:"payload"`
}

// NewCachedRegionCatalog 创建本地行政区目录缓存。
func NewCachedRegionCatalog(source *Provider, path string) (*CachedRegionCatalog, error) {
	if source == nil || strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: 行政区本地缓存配置无效", domain.ErrInvalidInput)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析行政区本地缓存路径: %w", err)
	}
	return &CachedRegionCatalog{source: source, path: filepath.Clean(absolute)}, nil
}

// Refresh 下载并校验 ADM1/ADM2，两个层级均成功后原子替换旧缓存。
func (c *CachedRegionCatalog) Refresh(ctx context.Context) error {
	adm1, err := c.source.fetchRegionCatalog(ctx, "CHN", "ADM1")
	if err != nil {
		return fmt.Errorf("刷新 ADM1 行政区目录: %w", err)
	}
	adm2, err := c.source.fetchRegionCatalog(ctx, "CHN", "ADM2")
	if err != nil {
		return fmt.Errorf("刷新 ADM2 行政区目录: %w", err)
	}
	cache := regionCatalogCacheFile{
		SchemaVersion: regionCatalogCacheSchema,
		CountryISO:    "CHN",
		GeneratedAt:   time.Now().UTC().Truncate(time.Microsecond),
		ADM1:          newCacheEntry(adm1),
		ADM2:          newCacheEntry(adm2),
	}
	return writeRegionCatalogCache(c.path, cache)
}

// Validate 检查本地缓存中的 ADM1/ADM2 是否完整且可用于服务。
func (c *CachedRegionCatalog) Validate(ctx context.Context) error {
	_, err := c.load(ctx)
	return err
}

// IsFresh 判断完整本地缓存是否仍在刷新周期内。
func (c *CachedRegionCatalog) IsFresh(ctx context.Context, maxAge time.Duration) (bool, error) {
	if maxAge <= 0 {
		return false, fmt.Errorf("%w: 行政区缓存刷新周期无效", domain.ErrInvalidInput)
	}
	cache, err := c.load(ctx)
	if err != nil {
		return false, err
	}
	return time.Since(cache.GeneratedAt.UTC()) <= maxAge, nil
}

// RegionCatalog 从本地缓存读取行政区目录，不触发网络请求。
func (c *CachedRegionCatalog) RegionCatalog(ctx context.Context, countryISO, level string) ([]exposurecollection.AdministrativeRegion, error) {
	cache, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	if countryISO != cache.CountryISO {
		return nil, fmt.Errorf("%w: 行政区缓存国家代码不匹配", domain.ErrInvalidInput)
	}
	entry, err := cache.entry(level)
	if err != nil {
		return nil, err
	}
	return decodeCacheEntry(*entry, cache.CountryISO)
}

// BoundaryForRegion 从本地缓存读取指定省市的 WGS84 边界。
func (c *CachedRegionCatalog) BoundaryForRegion(ctx context.Context, regionCode string) (exposurecollection.AdministrativeBoundary, error) {
	if strings.TrimSpace(regionCode) == "" {
		return exposurecollection.AdministrativeBoundary{}, fmt.Errorf("%w: 行政区代码为空", domain.ErrInvalidInput)
	}
	level := "ADM1"
	if strings.Count(regionCode, "-") > 1 {
		level = "ADM2"
	}
	regions, err := c.RegionCatalog(ctx, "CHN", level)
	if err != nil {
		return exposurecollection.AdministrativeBoundary{}, err
	}
	for _, region := range regions {
		if region.Code == regionCode {
			return boundaryFromRegion(region), nil
		}
	}
	return exposurecollection.AdministrativeBoundary{}, domain.ErrNotFound
}

// Boundary 保持 ADM0 风险栅格裁剪继续使用实时版本化边界。
func (c *CachedRegionCatalog) Boundary(ctx context.Context) (exposurecollection.AdministrativeBoundary, error) {
	return c.source.Boundary(ctx)
}

func newCacheEntry(snapshot regionCatalogSnapshot) regionCatalogCacheEntry {
	return regionCatalogCacheEntry{
		Level:        snapshot.level,
		BoundaryYear: snapshot.boundaryYear,
		Source:       snapshot.source,
		License:      snapshot.license,
		References:   append([]string(nil), snapshot.references...),
		Digest:       snapshot.digest,
		CollectedAt:  snapshot.collectedAt,
		Payload:      append([]byte(nil), snapshot.payload...),
	}
}

func (c *CachedRegionCatalog) load(ctx context.Context) (regionCatalogCacheFile, error) {
	select {
	case <-ctx.Done():
		return regionCatalogCacheFile{}, ctx.Err()
	default:
	}
	info, err := os.Lstat(c.path)
	if err != nil {
		return regionCatalogCacheFile{}, fmt.Errorf("读取行政区本地缓存: %w", err)
	}
	if !info.Mode().IsRegular() {
		return regionCatalogCacheFile{}, fmt.Errorf("%w: 行政区本地缓存不是普通文件", domain.ErrProviderUnavailable)
	}
	file, err := os.Open(c.path)
	if err != nil {
		return regionCatalogCacheFile{}, fmt.Errorf("打开行政区本地缓存: %w", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxRegionCacheBytes+1))
	if err != nil {
		return regionCatalogCacheFile{}, fmt.Errorf("读取行政区本地缓存内容: %w", err)
	}
	if len(payload) > maxRegionCacheBytes {
		return regionCatalogCacheFile{}, fmt.Errorf("%w: 行政区本地缓存超过安全预算", domain.ErrProviderUnavailable)
	}
	var cache regionCatalogCacheFile
	if err = json.Unmarshal(payload, &cache); err != nil {
		return regionCatalogCacheFile{}, fmt.Errorf("%w: 行政区本地缓存格式无效", domain.ErrProviderUnavailable)
	}
	if err = cache.validate(); err != nil {
		return regionCatalogCacheFile{}, err
	}
	return cache, nil
}

func (cache regionCatalogCacheFile) validate() error {
	if cache.SchemaVersion != regionCatalogCacheSchema || cache.CountryISO != "CHN" ||
		cache.GeneratedAt.IsZero() || cache.GeneratedAt.Location() != time.UTC {
		return fmt.Errorf("%w: 行政区本地缓存身份无效", domain.ErrProviderUnavailable)
	}
	for _, entry := range []regionCatalogCacheEntry{cache.ADM1, cache.ADM2} {
		if err := validateCacheEntry(entry, cache.CountryISO); err != nil {
			return err
		}
	}
	return nil
}

func (cache regionCatalogCacheFile) entry(level string) (*regionCatalogCacheEntry, error) {
	switch level {
	case "ADM1":
		return &cache.ADM1, nil
	case "ADM2":
		return &cache.ADM2, nil
	default:
		return nil, fmt.Errorf("%w: 行政区层级无效", domain.ErrInvalidInput)
	}
}

func validateCacheEntry(entry regionCatalogCacheEntry, countryISO string) error {
	if (entry.Level != "ADM1" && entry.Level != "ADM2") ||
		!validBoundaryYear(entry.BoundaryYear) || !validRegionMetadataText(entry.Source) ||
		!validRegionMetadataText(entry.License) || len(entry.References) != 2 ||
		len(entry.Payload) == 0 || !validDigest(entry.Digest) || entry.CollectedAt.IsZero() {
		return fmt.Errorf("%w: 行政区本地缓存元数据无效", domain.ErrProviderUnavailable)
	}
	digest := sha256.Sum256(entry.Payload)
	if hex.EncodeToString(digest[:]) != entry.Digest {
		return fmt.Errorf("%w: 行政区本地缓存摘要不匹配", domain.ErrProviderUnavailable)
	}
	regions, err := DecodeRegionCollection(entry.Payload, countryISO, entry.Level)
	if err != nil {
		return fmt.Errorf("校验行政区本地缓存几何: %w", err)
	}
	if len(regions) == 0 {
		return fmt.Errorf("%w: 行政区本地缓存为空", domain.ErrProviderUnavailable)
	}
	return nil
}

func decodeCacheEntry(entry regionCatalogCacheEntry, countryISO string) ([]exposurecollection.AdministrativeRegion, error) {
	if err := validateCacheEntry(entry, countryISO); err != nil {
		return nil, err
	}
	regions, err := DecodeRegionCollection(entry.Payload, countryISO, entry.Level)
	if err != nil {
		return nil, err
	}
	for index := range regions {
		regions[index].BoundaryYear = entry.BoundaryYear
		regions[index].Source = entry.Source
		regions[index].License = entry.License
		regions[index].Reference = entry.References[1]
		regions[index].Digest = entry.Digest
		regions[index].CollectedAt = entry.CollectedAt.UTC().Truncate(time.Microsecond)
		regions[index].InputReferences = append([]string(nil), entry.References...)
	}
	return regions, nil
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func writeRegionCatalogCache(path string, cache regionCatalogCacheFile) error {
	if err := cache.validate(); err != nil {
		return fmt.Errorf("写入前校验行政区本地缓存: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("创建行政区本地缓存目录: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".region-catalog-*.tmp")
	if err != nil {
		return fmt.Errorf("创建行政区本地缓存临时文件: %w", err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err = temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("设置行政区本地缓存权限: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(cache); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("编码行政区本地缓存: %w", err)
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("同步行政区本地缓存: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("关闭行政区本地缓存: %w", err)
	}
	if err = os.Rename(name, path); err != nil {
		return fmt.Errorf("原子替换行政区本地缓存: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开行政区本地缓存目录: %w", err)
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return fmt.Errorf("同步行政区本地缓存目录: %w", err)
	}
	return nil
}
