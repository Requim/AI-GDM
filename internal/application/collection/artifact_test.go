package collection

import (
	"context"
	"testing"

	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func TestArtifactCollector(t *testing.T) {
	discovery := discoveryStub{artifact: provenance.Artifact{Reference: "https://example.test/latest.tif"}}
	fetcher := fetcherStub{path: "/data/latest.tif"}
	collector := NewArtifactCollector(discovery, fetcher)
	artifact, err := collector.CollectLatest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if artifact.LocalPath != fetcher.path {
		t.Fatalf("CollectLatest() = %+v", artifact)
	}
}

type discoveryStub struct {
	artifact provenance.Artifact
}

func (s discoveryStub) DiscoverLatest(context.Context) (provenance.Artifact, error) {
	return s.artifact, nil
}

type fetcherStub struct {
	path string
}

func (s fetcherStub) Fetch(_ context.Context, artifact provenance.Artifact) (provenance.Artifact, error) {
	artifact.LocalPath = s.path
	return artifact, nil
}
