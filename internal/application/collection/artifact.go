package collection

import (
	"context"
	"fmt"

	"github.com/Requim/AI-GDM/internal/domain/provenance"
	"github.com/Requim/AI-GDM/internal/ports"
)

// ArtifactCollector 组合制品发现和下载端口。
type ArtifactCollector struct {
	discovery ports.ArtifactDiscovery
	fetcher   ports.ArtifactFetcher
}

// NewArtifactCollector 创建最新制品采集用例。
func NewArtifactCollector(discovery ports.ArtifactDiscovery, fetcher ports.ArtifactFetcher) *ArtifactCollector {
	return &ArtifactCollector{discovery: discovery, fetcher: fetcher}
}

// CollectLatest 发现并下载供应商当前最新制品。
func (c *ArtifactCollector) CollectLatest(ctx context.Context) (provenance.Artifact, error) {
	artifact, err := c.discovery.DiscoverLatest(ctx)
	if err != nil {
		return provenance.Artifact{}, fmt.Errorf("发现最新制品: %w", err)
	}
	artifact, err = c.fetcher.Fetch(ctx, artifact)
	if err != nil {
		return provenance.Artifact{}, fmt.Errorf("下载最新制品: %w", err)
	}
	return artifact, nil
}
