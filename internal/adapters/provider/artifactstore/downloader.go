package artifactstore

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Requim/AI-GDM/internal/adapters/provider/httpclient"
	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/ports"
)

// Downloader 使用受控 HTTP 客户端流式下载外部制品。
type Downloader struct {
	client   *httpclient.Client
	store    *Store
	maxBytes int64
	now      func() time.Time
}

var _ ports.ArtifactFetcher = (*Downloader)(nil)

// NewDownloader 创建通用外部制品下载器。
func NewDownloader(client *httpclient.Client, store *Store, maxBytes int64) *Downloader {
	if maxBytes <= 0 {
		maxBytes = defaultMaxArtifactBytes
	}
	return &Downloader{
		client: client, store: store, maxBytes: maxBytes,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// Fetch 下载、校验、计算摘要并原子保存制品。
func (d *Downloader) Fetch(ctx context.Context, artifact provenance.Artifact) (provenance.Artifact, error) {
	response, err := d.client.Open(ctx, httpclient.Request{Method: http.MethodGet, URL: artifact.Reference})
	if err != nil {
		return provenance.Artifact{}, err
	}
	defer response.Body.Close()
	if err = d.validateLength(response.ContentLength); err != nil {
		return provenance.Artifact{}, err
	}
	artifact.Provenance.FetchedAt = d.now()
	artifact.Provenance.ProviderRequestID = requestID(response)
	if mediaType := response.Header.Get("Content-Type"); mediaType != "" {
		artifact.MediaType = mediaType
	}
	stored, err := d.store.Save(ctx, artifact, response.Body)
	if err != nil {
		return provenance.Artifact{}, fmt.Errorf("保存下载制品: %w", err)
	}
	return stored, nil
}

func (d *Downloader) validateLength(length int64) error {
	if length > d.maxBytes {
		return fmt.Errorf("%w: 制品声明大小 %d 超过上限", domain.ErrInvalidInput, length)
	}
	return nil
}

func requestID(response *http.Response) string {
	for _, name := range []string{"X-Request-ID", "X-Log-ID", "X-Amzn-RequestId"} {
		if value := response.Header.Get(name); value != "" {
			return value
		}
	}
	return ""
}
