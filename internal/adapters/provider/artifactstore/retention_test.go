package artifactstore

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Requim/AI-GDM/internal/domain/provenance"
)

func retainFixture(reference string) provenance.Artifact {
	now := time.Now().UTC()
	return provenance.Artifact{Reference: reference, MediaType: "image/tiff",
		Provenance: provenance.Provenance{Provider: "NASA NCCS", Dataset: "LHASA NRT",
			SourceURI: reference, SourceRevision: `"a"`, FetchedAt: now, DataKind: provenance.DataKindNowcast}}
}

func TestRetainArtifactDeletesPreviousFilesOnly(t *testing.T) {
	ctx, root := context.Background(), t.TempDir()
	store := New(root, 1<<20)
	old, err := store.Save(ctx, retainFixture("https://example.test/old.tif"), bytes.NewReader([]byte("old")))
	if err != nil {
		t.Fatal(err)
	}
	next, err := store.Save(ctx, retainFixture("https://example.test/new.tif"), bytes.NewReader([]byte("new")))
	if err != nil {
		t.Fatal(err)
	}
	otherArtifact := retainFixture("https://example.test/unrelated.tif")
	otherArtifact.Provenance.Provider = "other"
	other, err := store.Save(ctx, otherArtifact, bytes.NewReader([]byte("other")))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RetainArtifact(ctx, next.Provenance); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{old.LocalPath, old.LocalPath + ".metadata.json"} {
		if _, err = os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("旧制品残留: %s", path)
		}
	}
	for _, path := range []string{next.LocalPath, other.LocalPath} {
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("误删保留制品: %v", err)
		}
	}
}

func TestRetainArtifactRejectsEscapedMetadata(t *testing.T) {
	ctx, root := context.Background(), t.TempDir()
	store := New(root, 1<<20)
	artifact, err := store.Save(ctx, retainFixture("https://example.test/old.tif"), bytes.NewReader([]byte("old")))
	if err != nil {
		t.Fatal(err)
	}
	metadataPath := artifact.LocalPath + ".metadata.json"
	artifact.LocalPath = filepath.Join(t.TempDir(), "outside")
	payload, _ := json.Marshal(artifact)
	if err = os.WriteFile(metadataPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = store.RetainArtifact(ctx, artifact.Provenance); err == nil {
		t.Fatal("越界路径未拒绝")
	}
}

func TestRetainArtifactKeepsOldWhenCurrentFileMissing(t *testing.T) {
	ctx, root := context.Background(), t.TempDir()
	store := New(root, 1<<20)
	old, err := store.Save(ctx, retainFixture("https://example.test/old.tif"), bytes.NewReader([]byte("old")))
	if err != nil {
		t.Fatal(err)
	}
	next, err := store.Save(ctx, retainFixture("https://example.test/new.tif"), bytes.NewReader([]byte("new")))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(next.LocalPath); err != nil {
		t.Fatal(err)
	}
	if err = store.RetainArtifact(ctx, next.Provenance); err == nil {
		t.Fatal("缺失当前制品仍清理旧文件")
	}
	if _, err = os.Stat(old.LocalPath); err != nil {
		t.Fatal(err)
	}
}
