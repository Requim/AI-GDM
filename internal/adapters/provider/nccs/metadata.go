package nccs

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

type metadata struct {
	size      int64
	etag      string
	published time.Time
	seen      time.Time
}

func (p *Provider) inspect(ctx context.Context, reference string) (metadata, error) {
	response, err := p.client.Do(ctx, httpclient.Request{
		Method: http.MethodGet, URL: reference, MaxBodyBytes: 1,
		Headers:        http.Header{"Range": {"bytes=0-0"}, "Accept-Encoding": {"identity"}},
		RedirectPolicy: httpclient.RedirectDeny,
	})
	if err != nil {
		return metadata{}, fmt.Errorf("读取 NCCS 文件描述: %w", err)
	}
	size, parseErr := strconv.ParseInt(strings.TrimPrefix(response.Header.Get("Content-Range"), "bytes 0-0/"), 10, 64)
	media, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	etag := response.Header.Get("ETag")
	published, dateErr := http.ParseTime(response.Header.Get("Last-Modified"))
	if response.StatusCode != http.StatusPartialContent || parseErr != nil || dateErr != nil ||
		response.Header.Get("Content-Range") != fmt.Sprintf("bytes 0-0/%d", size) ||
		size < 1024 || size > MaxFileBytes || !httpclient.IsStrongETag(etag) ||
		media != "image/tiff" || len(response.Body) != 1 ||
		(response.Body[0] != 'I' && response.Body[0] != 'M') ||
		published.After(response.FetchedAt.Add(time.Minute)) {
		return metadata{}, fmt.Errorf("%w: NCCS 文件描述或强版本无效", domain.ErrProviderUnavailable)
	}
	return metadata{size: size, etag: etag, published: published.UTC(), seen: response.FetchedAt.UTC()}, nil
}

// VerifyCurrent 在下载前后复核同一文件；不在下载过程中偷偷切换到新文件。
func (p *Provider) VerifyCurrent(ctx context.Context, artifact provenance.Artifact) error {
	value, err := p.inspect(ctx, artifact.Reference)
	if err != nil {
		return err
	}
	if value.etag != artifact.Provenance.SourceRevision || value.size != artifact.SizeBytes ||
		!value.published.Equal(artifact.Provenance.PublishedAt) {
		return fmt.Errorf("%w: NCCS 文件版本发生变化", domain.ErrProviderUnavailable)
	}
	return nil
}

func (p *Provider) artifact(reference string, value metadata) provenance.Artifact {
	return provenance.Artifact{Reference: reference, MediaType: "image/tiff", SizeBytes: value.size,
		Provenance: provenance.Provenance{
			Provider: ProviderName, Dataset: DatasetName, DatasetVersion: "nccs-nrt",
			SourceURI: reference, SourceRevision: value.etag, DataKind: provenance.DataKindNowcast,
			RevisionFirstSeenAt: value.seen, FetchedAt: value.seen, PublishedAt: value.published,
			ValidFrom: value.published, ValidTo: value.published.Add(p.staleAfter),
			CRS: "EPSG:4326", Model: "LHASA", SpatialResolution: "原生 30 弧秒",
			Citation: "NASA NCCS LHASA 近实时公开栅格", TemporalResolution: "文件时间标识见来源 URI",
			QualityFlags: []string{"publication_freshness_not_model_validity", "source_revision_first_seen"},
			Limitations: []string{
				"有效期是按 Last-Modified 计算的本系统使用期限，不代表 NASA 模型覆盖期或运行时刻",
				"文件名时间仅为供应商文件标识，ObservedAt 保持未知，算法版本未由文件元数据确认",
				"全球模型估计，不是中国官方地质灾害预警；缺失像元不代表零风险",
			},
		}}
}
