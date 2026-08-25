package lhasa

import (
	"context"
	"fmt"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/ports"
)

const (
	DefaultServiceURL = "https://gis.earthdata.nasa.gov/gis01/rest/services/Landslides/LHASA_Hazard_Today/ImageServer"
	ProviderName      = "NASA Earthdata GIS"
	DatasetName       = "LHASA Hazard Today"
	DatasetVersion    = "2.1"
	defaultTileWidth  = 2048
	defaultTileHeight = 2048
	defaultResolution = 1.0 / 120.0
	maxServiceWidth   = 15000
	maxServiceHeight  = 4100
	defaultMaxPart    = 32 << 20
	defaultMaxBytes   = 512 << 20
	maxTileCount      = 256
	minimumTIFFBytes  = 1024
)

var defaultBBox = [4]float64{73.5, 18.0, 135.1, 53.6}

// Config 配置 NASA Earthdata GIS 的 LHASA 固定目标网格分片和资源上限。
type Config struct {
	ServiceURL   string
	StaleAfter   time.Duration
	BBox         [4]float64
	Resolution   float64
	TileWidth    int
	TileHeight   int
	MaxPartBytes int64
	MaxBytes     int64
}

// Provider 通过公开 ImageServer 发现当前 LHASA Today 组合栅格修订。
type Provider struct {
	client       *httpclient.Client
	serviceURL   *url.URL
	staleAfter   time.Duration
	bbox         [4]float64
	resolution   float64
	tileWidth    int
	tileHeight   int
	maxPartBytes int64
	maxBytes     int64
}

type sourceTile struct {
	bbox   [4]float64
	width  int
	height int
}

var _ ports.ArtifactDiscovery = (*Provider)(nil)

// New 创建 Earthdata GIS LHASA 分片发现适配器。
func New(client *httpclient.Client, config Config) (*Provider, error) {
	config = applyDefaults(config)
	serviceURL, err := validateConfig(config)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = httpclient.New(httpclient.Options{})
	}
	return &Provider{
		client: client, serviceURL: serviceURL, staleAfter: config.StaleAfter,
		bbox: config.BBox, resolution: config.Resolution,
		tileWidth: config.TileWidth, tileHeight: config.TileHeight,
		maxPartBytes: config.MaxPartBytes, maxBytes: config.MaxBytes,
	}, nil
}

// DiscoverLatest 逐片读取强 ETag 并生成可审计组合修订。
func (p *Provider) DiscoverLatest(ctx context.Context) (provenance.Artifact, error) {
	parts, total, firstSeen, err := p.discoverParts(ctx)
	if err != nil {
		return provenance.Artifact{}, err
	}
	return p.artifact(parts, total, firstSeen), nil
}

// VerifyCurrent 确认逻辑制品的所有分片仍与发现阶段一致。
func (p *Provider) VerifyCurrent(ctx context.Context, artifact provenance.Artifact) error {
	parts, _, _, err := p.discoverParts(ctx)
	if err != nil {
		return err
	}
	if artifact.Provenance.SourceRevision != provenance.CompositeSourceRevision(parts) ||
		!sameSourceParts(artifact.Provenance.SourceParts, parts) {
		return fmt.Errorf("%w: Earthdata 组合修订已变化", domain.ErrProviderUnavailable)
	}
	return nil
}

func (p *Provider) discoverParts(ctx context.Context) ([]provenance.SourcePart, int64, time.Time, error) {
	tiles, err := buildTiles(p.bbox, p.resolution, p.tileWidth, p.tileHeight)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	parts := make([]provenance.SourcePart, 0, len(tiles))
	var total int64
	var firstSeen time.Time
	for _, tile := range tiles {
		part, fetchedAt, partErr := p.discoverPart(ctx, tile)
		if partErr != nil {
			return nil, 0, time.Time{}, partErr
		}
		if part.SizeBytes > p.maxBytes-total {
			return nil, 0, time.Time{}, fmt.Errorf("%w: Earthdata 分片总大小超过上限", domain.ErrProviderUnavailable)
		}
		total += part.SizeBytes
		parts = append(parts, part)
		if fetchedAt.After(firstSeen) {
			firstSeen = fetchedAt
		}
	}
	return parts, total, firstSeen.UTC(), nil
}

func (p *Provider) discoverPart(ctx context.Context, tile sourceTile) (provenance.SourcePart, time.Time, error) {
	reference := p.exportURL(tile)
	response, err := p.client.Do(ctx, httpclient.Request{Method: http.MethodHead, URL: reference})
	if err != nil {
		return provenance.SourcePart{}, time.Time{}, fmt.Errorf("读取 Earthdata LHASA 分片描述: %w", err)
	}
	size, revision, err := p.validateHeaders(response.Header)
	if err != nil {
		return provenance.SourcePart{}, time.Time{}, err
	}
	return provenance.SourcePart{
		Reference: reference, Revision: revision, SizeBytes: size, BBox: tile.bbox,
	}, response.FetchedAt.UTC(), nil
}

func applyDefaults(config Config) Config {
	if strings.TrimSpace(config.ServiceURL) == "" {
		config.ServiceURL = DefaultServiceURL
	}
	if config.StaleAfter <= 0 {
		config.StaleAfter = 12 * time.Hour
	}
	if config.BBox == [4]float64{} {
		config.BBox = defaultBBox
	}
	if config.Resolution <= 0 {
		config.Resolution = defaultResolution
	}
	if config.TileWidth <= 0 {
		config.TileWidth = defaultTileWidth
	}
	if config.TileHeight <= 0 {
		config.TileHeight = defaultTileHeight
	}
	if config.MaxPartBytes <= 0 {
		config.MaxPartBytes = defaultMaxPart
	}
	if config.MaxBytes <= 0 {
		config.MaxBytes = defaultMaxBytes
	}
	return config
}

func validateConfig(config Config) (*url.URL, error) {
	serviceURL, err := url.Parse(strings.TrimSpace(config.ServiceURL))
	if err != nil || serviceURL.Scheme == "" || serviceURL.Host == "" ||
		serviceURL.RawQuery != "" || serviceURL.Fragment != "" {
		return nil, fmt.Errorf("%w: Earthdata ImageServer 地址无效", domain.ErrInvalidInput)
	}
	if !strings.HasSuffix(strings.TrimRight(serviceURL.Path, "/"), "/ImageServer") {
		return nil, fmt.Errorf("%w: Earthdata 地址必须指向 ImageServer", domain.ErrInvalidInput)
	}
	if !validBBox(config.BBox) || !validResolution(config.BBox, config.Resolution) {
		return nil, fmt.Errorf("%w: Earthdata 栅格范围或分辨率无效", domain.ErrInvalidInput)
	}
	if config.TileWidth > maxServiceWidth || config.TileHeight > maxServiceHeight {
		return nil, fmt.Errorf("%w: Earthdata 分片尺寸超过服务上限", domain.ErrInvalidInput)
	}
	if config.MaxPartBytes < minimumTIFFBytes || config.MaxBytes < config.MaxPartBytes {
		return nil, fmt.Errorf("%w: Earthdata 制品上限无效", domain.ErrInvalidInput)
	}
	_, err = buildTiles(config.BBox, config.Resolution, config.TileWidth, config.TileHeight)
	return serviceURL, err
}

func validBBox(value [4]float64) bool {
	for _, coordinate := range value {
		if math.IsNaN(coordinate) || math.IsInf(coordinate, 0) {
			return false
		}
	}
	return value[0] >= -180 && value[2] <= 180 && value[1] >= -90 && value[3] <= 90 &&
		value[0] < value[2] && value[1] < value[3]
}

func validResolution(bbox [4]float64, resolution float64) bool {
	if math.IsNaN(resolution) || math.IsInf(resolution, 0) || resolution <= 0 {
		return false
	}
	width := math.Round((bbox[2] - bbox[0]) / resolution)
	height := math.Round((bbox[3] - bbox[1]) / resolution)
	return width > 0 && height > 0 &&
		math.Abs(width*resolution-(bbox[2]-bbox[0])) < 1e-9 &&
		math.Abs(height*resolution-(bbox[3]-bbox[1])) < 1e-9
}

func buildTiles(bbox [4]float64, resolution float64, tileWidth, tileHeight int) ([]sourceTile, error) {
	if tileWidth <= 0 || tileHeight <= 0 || !validResolution(bbox, resolution) {
		return nil, fmt.Errorf("%w: Earthdata 分片网格无效", domain.ErrInvalidInput)
	}
	width := int(math.Round((bbox[2] - bbox[0]) / resolution))
	height := int(math.Round((bbox[3] - bbox[1]) / resolution))
	count := ((width + tileWidth - 1) / tileWidth) * ((height + tileHeight - 1) / tileHeight)
	if count <= 0 || count > maxTileCount {
		return nil, fmt.Errorf("%w: Earthdata 分片数量超过上限", domain.ErrInvalidInput)
	}
	values := make([]sourceTile, 0, count)
	for y := 0; y < height; y += tileHeight {
		partHeight := min(tileHeight, height-y)
		for x := 0; x < width; x += tileWidth {
			partWidth := min(tileWidth, width-x)
			values = append(values, newSourceTile(bbox, resolution, x, y, partWidth, partHeight))
		}
	}
	return values, nil
}

func newSourceTile(bbox [4]float64, resolution float64,
	x, y, width, height int,
) sourceTile {
	return sourceTile{
		bbox: [4]float64{
			bbox[0] + float64(x)*resolution, bbox[1] + float64(y)*resolution,
			bbox[0] + float64(x+width)*resolution, bbox[1] + float64(y+height)*resolution,
		},
		width: width, height: height,
	}
}

func (p *Provider) exportURL(tile sourceTile) string {
	target := *p.serviceURL
	target.Path = strings.TrimRight(target.Path, "/") + "/exportImage"
	query := target.Query()
	query.Set("bbox", bboxValue(tile.bbox))
	query.Set("bboxSR", "4326")
	query.Set("imageSR", "4326")
	query.Set("size", fmt.Sprintf("%d,%d", tile.width, tile.height))
	query.Set("format", "tiff")
	query.Set("pixelType", "F32")
	query.Set("noData", "-9999")
	query.Set("interpolation", "RSP_NearestNeighbor")
	query.Set("adjustAspectRatio", "false")
	query.Set("f", "image")
	target.RawQuery = query.Encode()
	return target.String()
}

func bboxValue(value [4]float64) string {
	parts := make([]string, len(value))
	for index, coordinate := range value {
		parts[index] = strconv.FormatFloat(coordinate, 'f', -1, 64)
	}
	return strings.Join(parts, ",")
}

func (p *Provider) validateHeaders(header http.Header) (int64, string, error) {
	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || mediaType != "image/tiff" {
		return 0, "", fmt.Errorf("%w: Earthdata 未返回 TIFF", domain.ErrProviderUnavailable)
	}
	size, err := strconv.ParseInt(header.Get("Content-Length"), 10, 64)
	if err != nil || size < minimumTIFFBytes || size > p.maxPartBytes {
		return 0, "", fmt.Errorf("%w: Earthdata TIFF 分片大小无效", domain.ErrProviderUnavailable)
	}
	revision := header.Get("ETag")
	if !httpclient.IsStrongETag(revision) {
		return 0, "", fmt.Errorf("%w: Earthdata 分片缺少强 ETag", domain.ErrProviderUnavailable)
	}
	return size, revision, nil
}

func (p *Provider) artifact(parts []provenance.SourcePart, size int64,
	firstSeen time.Time,
) provenance.Artifact {
	return provenance.Artifact{
		Reference: p.serviceURL.String(), MediaType: "image/tiff", SizeBytes: size,
		Provenance: provenance.Provenance{
			Provider: ProviderName, Dataset: DatasetName, DatasetVersion: DatasetVersion,
			SourceRevision: provenance.CompositeSourceRevision(parts), SourceURI: p.serviceURL.String(),
			Citation: "NASA GSFC LHASA 2.1", DataKind: provenance.DataKindNowcast,
			RevisionFirstSeenAt: firstSeen, FetchedAt: firstSeen,
			ValidFrom: firstSeen, ValidTo: firstSeen.Add(p.staleAfter),
			SpatialResolution:  "Earthdata 最近邻导出到固定 30 弧秒目标网格",
			TemporalResolution: "官方标注每日更新两次，具体生产时刻未公开",
			CRS:                "EPSG:4326", BBox: p.bbox, Model: "LHASA 2.1", SourceParts: parts,
			QualityFlags: []string{"source_revision_first_seen", "composite_tile_revision", "target_grid_tiled_export"},
			Limitations: []string{
				"NASA 未提供精确模型运行时刻，观测时间保持未知，时效从组合修订首次发现时开始计算",
				"组合修订由固定目标网格分片 ETag 计算，供应商未提供跨分片原子版本号",
				"服务源网格与目标网格存在亚像元偏移，导出使用最近邻重采样且不插值概率值",
				"全球模型估计，不是中国官方地质灾害预警",
			},
		},
	}
}

func sameSourceParts(expected, actual []provenance.SourcePart) bool {
	if len(expected) != len(actual) {
		return false
	}
	for index := range expected {
		left, right := expected[index], actual[index]
		if left.Reference != right.Reference || left.Revision != right.Revision ||
			left.SizeBytes != right.SizeBytes || left.BBox != right.BBox {
			return false
		}
	}
	return true
}
