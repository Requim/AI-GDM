package artifactstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func TestStoreSave(t *testing.T) {
	store := New(t.TempDir(), 1024)
	artifact := fixtureArtifact(time.Now().UTC())
	stored, err := store.Save(context.Background(), artifact, strings.NewReader("risk-data"))
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(stored.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "risk-data" || stored.SizeBytes != 9 || len(stored.Provenance.SHA256) != 64 {
		t.Fatalf("Save() = %+v content=%q", stored, content)
	}
	metadata, err := os.ReadFile(stored.LocalPath + ".metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var restored provenance.Artifact
	if err = json.Unmarshal(metadata, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Provenance.SHA256 != stored.Provenance.SHA256 || restored.LocalPath != stored.LocalPath {
		t.Fatalf("metadata = %+v", restored)
	}
}

func TestStoreRejectsEmptyArtifact(t *testing.T) {
	store := New(t.TempDir(), 1024)
	_, err := store.Save(context.Background(), fixtureArtifact(time.Now().UTC()), strings.NewReader(""))
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Save() error = %v", err)
	}
}

func TestStoreRejectsOversizedArtifact(t *testing.T) {
	store := New(t.TempDir(), 4)
	_, err := store.Save(context.Background(), fixtureArtifact(time.Now().UTC()), strings.NewReader("12345"))
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Save() error = %v", err)
	}
}

func fixtureArtifact(now time.Time) provenance.Artifact {
	return provenance.Artifact{
		Reference: "https://example.test/hazard/latest.tif", MediaType: "image/tiff",
		Provenance: provenance.Provenance{
			Provider: "test", Dataset: "hazard", SourceURI: "https://example.test/hazard/latest.tif",
			DataKind: provenance.DataKindNowcast, FetchedAt: now,
		},
	}
}
