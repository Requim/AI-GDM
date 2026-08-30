package main

import (
	"archive/tar"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type fixtureOCIIndex struct {
	SchemaVersion int64                  `json:"schemaVersion"`
	MediaType     string                 `json:"mediaType"`
	Manifests     []fixtureOCIDescriptor `json:"manifests"`
}

type fixtureOCIDescriptor struct {
	MediaType   string              `json:"mediaType"`
	Digest      string              `json:"digest"`
	Size        int64               `json:"size"`
	Annotations map[string]string   `json:"annotations,omitempty"`
	Platform    *fixtureOCIPlatform `json:"platform,omitempty"`
}

type fixtureOCIPlatform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
}

type fixtureOCIManifest struct {
	SchemaVersion int64                  `json:"schemaVersion"`
	MediaType     string                 `json:"mediaType"`
	Config        fixtureOCIDescriptor   `json:"config"`
	Layers        []fixtureOCIDescriptor `json:"layers"`
}

func TestDockerOCIIndexBindsTagTopIDPlatformAndConfig(t *testing.T) {
	tests := map[string]func(*testing.T, *releaseFixture){
		"missing-index": func(t *testing.T, fixture *releaseFixture) {
			delete(fixture.configs, "index.json")
			fixture.rewriteTarAndArtifacts(t, nil)
		},
		"duplicate-index": func(t *testing.T, fixture *releaseFixture) {
			fixture.rewriteTarWithDuplicateIndex(t)
		},
		"swapped-top-id": func(t *testing.T, fixture *releaseFixture) {
			fixture.manifest.Images[0].ID, fixture.manifest.Images[1].ID =
				fixture.manifest.Images[1].ID, fixture.manifest.Images[0].ID
			fixture.writeManifest(t)
		},
		"swapped-repo-tag": func(t *testing.T, fixture *releaseFixture) {
			fixture.saved[0].RepoTags[0], fixture.saved[1].RepoTags[0] =
				fixture.saved[1].RepoTags[0], fixture.saved[0].RepoTags[0]
			fixture.rewriteTarAndArtifacts(t, nil)
		},
		"swapped-config": func(t *testing.T, fixture *releaseFixture) {
			fixture.saved[0].Config, fixture.saved[1].Config = fixture.saved[1].Config, fixture.saved[0].Config
			fixture.rewriteTarAndArtifacts(t, nil)
		},
		"descriptor-size": func(t *testing.T, fixture *releaseFixture) {
			index := fixture.readOCIIndex(t, "index.json")
			index.Manifests[0].Size++
			fixture.writeOCIIndex(t, "index.json", index)
			fixture.rewriteTarAndArtifacts(t, nil)
		},
		"descriptor-content": func(t *testing.T, fixture *releaseFixture) {
			index := fixture.readOCIIndex(t, "index.json")
			path := descriptorPath(index.Manifests[0].Digest)
			fixture.configs[path] = append(fixture.configs[path], ' ')
			fixture.rewriteTarAndArtifacts(t, nil)
		},
		"config-size": func(t *testing.T, fixture *releaseFixture) {
			fixture.mutateDirectManifest(t, func(value *fixtureOCIManifest) { value.Config.Size++ })
		},
		"missing-platform": func(t *testing.T, fixture *releaseFixture) {
			fixture.mutateNestedIndex(t, 1, func(value *fixtureOCIIndex) {
				value.Manifests[0].Platform.Architecture = "arm64"
			})
		},
		"multiple-platforms": func(t *testing.T, fixture *releaseFixture) {
			fixture.addSecondLinuxManifest(t, 1)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			mutate(t, fixture)
			assertPackageRejected(t, fixture.root)
		})
	}
}

func (fixture *releaseFixture) prepareOCIIndex(t *testing.T) {
	t.Helper()
	top := make([]fixtureOCIDescriptor, 0, len(fixture.saved))
	for index, saved := range fixture.saved {
		manifestPayload := fixture.imageManifestPayload(t, saved)
		descriptor := fixture.storeOCIPayload(manifestPayload, ociManifestMediaType)
		if index > 0 {
			platform := &fixtureOCIPlatform{Architecture: "amd64", OS: "linux"}
			nested := fixtureOCIIndex{2, ociIndexMediaType, []fixtureOCIDescriptor{descriptor}}
			nested.Manifests[0].Platform = platform
			descriptor = fixture.storeOCIPayload(marshalFixture(t, nested), ociIndexMediaType)
		}
		descriptor.Annotations = map[string]string{ociImageNameKey: "docker.io/" + saved.RepoTags[0]}
		fixture.manifest.Images[index].ID = descriptor.Digest
		if source := fixture.manifest.Images[index].Source; source != "" {
			name, _, _ := strings.Cut(source, "@sha256:")
			fixture.manifest.Images[index].Source = name + "@" + descriptor.Digest
		}
		top = append(top, descriptor)
	}
	fixture.writeOCIIndex(t, "index.json", fixtureOCIIndex{2, ociIndexMediaType, top})
}

func (fixture *releaseFixture) imageManifestPayload(t *testing.T, saved savedImage) []byte {
	t.Helper()
	configDigest, err := dockerConfigDigest(saved.Config)
	if err != nil {
		t.Fatal(err)
	}
	layers := make([]fixtureOCIDescriptor, 0, len(saved.Layers))
	for _, layer := range saved.Layers {
		digest, digestErr := dockerConfigDigest(layer)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		layers = append(layers, fixtureOCIDescriptor{
			MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: "sha256:" + digest, Size: 1,
		})
	}
	manifest := fixtureOCIManifest{SchemaVersion: 2, MediaType: ociManifestMediaType, Layers: layers}
	manifest.Config = fixtureOCIDescriptor{
		MediaType: ociConfigMediaType, Digest: "sha256:" + configDigest, Size: int64(len(fixture.configs[saved.Config])),
	}
	return marshalFixture(t, manifest)
}

func (fixture *releaseFixture) storeOCIPayload(payload []byte, mediaType string) fixtureOCIDescriptor {
	digest := sha256Hex(payload)
	fixture.configs["blobs/sha256/"+digest] = payload
	return fixtureOCIDescriptor{MediaType: mediaType, Digest: "sha256:" + digest, Size: int64(len(payload))}
}

func (fixture *releaseFixture) readOCIIndex(t *testing.T, path string) fixtureOCIIndex {
	t.Helper()
	var value fixtureOCIIndex
	if err := json.Unmarshal(fixture.configs[path], &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func (fixture *releaseFixture) writeOCIIndex(t *testing.T, path string, value fixtureOCIIndex) {
	t.Helper()
	fixture.configs[path] = marshalFixture(t, value)
}

func (fixture *releaseFixture) replaceTopPayload(t *testing.T, index int, payload []byte, mediaType string) {
	t.Helper()
	top := fixture.readOCIIndex(t, "index.json")
	oldPath := descriptorPath(top.Manifests[index].Digest)
	delete(fixture.configs, oldPath)
	descriptor := fixture.storeOCIPayload(payload, mediaType)
	descriptor.Annotations = top.Manifests[index].Annotations
	top.Manifests[index] = descriptor
	fixture.manifest.Images[index].ID = descriptor.Digest
	fixture.writeOCIIndex(t, "index.json", top)
}

func (fixture *releaseFixture) mutateDirectManifest(t *testing.T, mutate func(*fixtureOCIManifest)) {
	t.Helper()
	top := fixture.readOCIIndex(t, "index.json")
	path := descriptorPath(top.Manifests[0].Digest)
	var value fixtureOCIManifest
	if err := json.Unmarshal(fixture.configs[path], &value); err != nil {
		t.Fatal(err)
	}
	mutate(&value)
	fixture.replaceTopPayload(t, 0, marshalFixture(t, value), ociManifestMediaType)
	fixture.rewriteTarAndArtifacts(t, nil)
}

func (fixture *releaseFixture) mutateNestedIndex(t *testing.T, index int, mutate func(*fixtureOCIIndex)) {
	t.Helper()
	top := fixture.readOCIIndex(t, "index.json")
	path := descriptorPath(top.Manifests[index].Digest)
	var value fixtureOCIIndex
	if err := json.Unmarshal(fixture.configs[path], &value); err != nil {
		t.Fatal(err)
	}
	mutate(&value)
	fixture.replaceTopPayload(t, index, marshalFixture(t, value), ociIndexMediaType)
	fixture.rewriteTarAndArtifacts(t, nil)
}

func (fixture *releaseFixture) addSecondLinuxManifest(t *testing.T, index int) {
	t.Helper()
	top := fixture.readOCIIndex(t, "index.json")
	nestedPath := descriptorPath(top.Manifests[index].Digest)
	nested := fixture.readOCIIndex(t, nestedPath)
	childPath := descriptorPath(nested.Manifests[0].Digest)
	secondPayload := append(append([]byte(nil), fixture.configs[childPath]...), '\n')
	second := fixture.storeOCIPayload(secondPayload, ociManifestMediaType)
	second.Platform = &fixtureOCIPlatform{Architecture: "amd64", OS: "linux"}
	nested.Manifests = append(nested.Manifests, second)
	fixture.replaceTopPayload(t, index, marshalFixture(t, nested), ociIndexMediaType)
	fixture.rewriteTarAndArtifacts(t, nil)
}

func (fixture *releaseFixture) rewriteTarWithDuplicateIndex(t *testing.T) {
	t.Helper()
	filename := filepath.Join(fixture.root, filepath.FromSlash(imageTarPath))
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	rawManifest, err := json.Marshal(fixture.saved)
	if err == nil {
		err = writeTarFile(writer, "manifest.json", rawManifest)
	}
	keys := make([]string, 0, len(fixture.configs))
	for key := range fixture.configs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err == nil {
			err = writeTarFile(writer, key, fixture.configs[key])
		}
	}
	if err == nil {
		err = writeTarFile(writer, "index.json", fixture.configs["index.json"])
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	files, collectErr := collectPackageFiles(fixture.root)
	if collectErr != nil {
		t.Fatal(collectErr)
	}
	fixture.manifest.Artifacts = buildArtifacts(t, fixture.root, files)
	fixture.writeManifest(t)
}

func marshalFixture(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
