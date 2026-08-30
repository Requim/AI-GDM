package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type releaseFixture struct {
	root     string
	manifest releaseManifest
	saved    []savedImage
	configs  map[string][]byte
}

func TestValidatePackageAcceptsCompleteRelease(t *testing.T) {
	fixture := newReleaseFixture(t)
	if err := validatePackage(fixture.root); err != nil {
		t.Fatalf("完整发布包未通过校验: %v", err)
	}
	if err := run([]string{fixture.root}); err != nil {
		t.Fatalf("命令入口未通过校验: %v", err)
	}
}

func TestValidatePackageAcceptsUncommittedFixedTree(t *testing.T) {
	fixture := newReleaseFixture(t)
	fixture.manifest.SourceCommit = "unknown"
	fixture.writeManifest(t)
	if err := validatePackage(fixture.root); err != nil {
		t.Fatalf("预提交固定 tree 未通过校验: %v", err)
	}
}

func TestReleaseManifestRejectsStructuralAndIdentityDamage(t *testing.T) {
	tests := map[string]func(*testing.T, *releaseFixture){
		"unknown": func(t *testing.T, fixture *releaseFixture) {
			mutateManifestText(t, fixture, `"schemaVersion": 1`, `"unknown": true, "schemaVersion": 1`)
		},
		"duplicate": func(t *testing.T, fixture *releaseFixture) {
			mutateManifestText(t, fixture, `"schemaVersion": 1`, `"schemaVersion": 1, "schemaVersion": 1`)
		},
		"trailing": func(t *testing.T, fixture *releaseFixture) {
			appendFile(t, filepath.Join(fixture.root, "manifest.json"), []byte(`{}`))
		},
		"version": func(t *testing.T, fixture *releaseFixture) {
			fixture.manifest.Version = "v01.0.0"
			fixture.writeManifest(t)
		},
		"utc": func(t *testing.T, fixture *releaseFixture) {
			fixture.manifest.CreatedAt = "2026-08-30T12:00:00+00:00"
			fixture.writeManifest(t)
		},
		"source": func(t *testing.T, fixture *releaseFixture) {
			fixture.manifest.SourceTree = strings.Repeat("A", 40)
			fixture.writeManifest(t)
		},
		"source-commit": func(t *testing.T, fixture *releaseFixture) {
			fixture.manifest.SourceCommit = "uncommitted"
			fixture.writeManifest(t)
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

func TestReleaseManifestRejectsImageContractDamage(t *testing.T) {
	tests := map[string]func(*releaseFixture){
		"count": func(fixture *releaseFixture) {
			fixture.manifest.Images = fixture.manifest.Images[:2]
		},
		"duplicate-reference": func(fixture *releaseFixture) {
			fixture.manifest.Images[1].Reference = fixture.manifest.Images[0].Reference
		},
		"duplicate-id": func(fixture *releaseFixture) {
			fixture.manifest.Images[1].ID = fixture.manifest.Images[0].ID
		},
		"platform": func(fixture *releaseFixture) {
			fixture.manifest.Images[0].Platform = "linux/arm64"
		},
		"source-digest": func(fixture *releaseFixture) {
			fixture.manifest.Images[1].Source = "postgis/postgis@sha256:" + strings.Repeat("0", 64)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			mutate(fixture)
			fixture.writeManifest(t)
			assertPackageRejected(t, fixture.root)
		})
	}
}

func TestPackageArtifactsMustMatchEveryFile(t *testing.T) {
	t.Run("content", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		writeFile(t, filepath.Join(fixture.root, "compose.yaml"), []byte("tampered"))
		assertPackageRejected(t, fixture.root)
	})
	t.Run("extra", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		writeFile(t, filepath.Join(fixture.root, "unexpected.txt"), []byte("extra"))
		assertPackageRejected(t, fixture.root)
	})
	t.Run("missing-entry", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		fixture.manifest.Artifacts = fixture.manifest.Artifacts[1:]
		fixture.writeManifest(t)
		assertPackageRejected(t, fixture.root)
	})
	t.Run("unsafe-path", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		fixture.manifest.Artifacts[0].Path = "../outside"
		fixture.writeManifest(t)
		assertPackageRejected(t, fixture.root)
	})
}

func TestReleaseImageEnvironmentIsExact(t *testing.T) {
	valid := "AI_GDM_IMAGE=ai-gdm/server:v0.1.0\n" +
		"AI_GDM_POSTGIS_IMAGE=ai-gdm/postgis:17-3.5-v0.1.0\n" +
		"AI_GDM_REDIS_IMAGE=ai-gdm/redis:7.4.10-v0.1.0\n"
	tests := map[string]string{
		"duplicate": valid + "AI_GDM_IMAGE=ai-gdm/server:v0.1.0\n",
		"unknown":   valid + "OTHER_IMAGE=other/image:v1\n",
		"missing":   strings.Replace(valid, "AI_GDM_REDIS_IMAGE=ai-gdm/redis:7.4.10-v0.1.0\n", "", 1),
		"same-tag":  strings.Replace(valid, "ai-gdm/redis:7.4.10-v0.1.0", "ai-gdm/server:v0.1.0", 1),
		"swapped": "AI_GDM_IMAGE=ai-gdm/redis:7.4.10-v0.1.0\n" +
			"AI_GDM_POSTGIS_IMAGE=ai-gdm/postgis:17-3.5-v0.1.0\n" +
			"AI_GDM_REDIS_IMAGE=ai-gdm/server:v0.1.0\n",
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "release-images.env")
			writeFile(t, filename, []byte(payload))
			if _, err := loadReleaseImages(filename); err == nil {
				t.Fatal("损坏的镜像环境文件未被拒绝")
			}
		})
	}
}

func TestRuntimeTemplateRequiresSingleEmptySensitiveValues(t *testing.T) {
	valid := runtimeTemplate()
	adminKey := "APP_ADMIN_" + "TOKEN"
	tests := map[string]string{
		"non-empty": strings.Replace(valid, adminKey+"=", adminKey+"="+strings.Repeat("x", 12), 1),
		"duplicate": valid + "\n" + "LLM_API_" + "KEY=\n",
		"missing":   strings.Replace(valid, "BOCHA_API_"+"KEY=\n", "", 1),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "runtime.env.example")
			writeFile(t, filename, []byte(payload))
			if err := validateRuntimeTemplate(filename); err == nil {
				t.Fatal("损坏的运行配置模板未被拒绝")
			}
		})
	}
}

func TestDockerSaveRejectsTagsIdentityAndPlatformDamage(t *testing.T) {
	tests := map[string]func(*testing.T, *releaseFixture){
		"extra-tag": func(t *testing.T, fixture *releaseFixture) {
			fixture.saved[0].RepoTags = append(fixture.saved[0].RepoTags, "ai-gdm/server:extra")
			fixture.rewriteTarAndArtifacts(t, nil)
		},
		"image-id": func(t *testing.T, fixture *releaseFixture) {
			fixture.manifest.Images[0].ID = "sha256:" + strings.Repeat("f", 64)
			fixture.writeManifest(t)
		},
		"windows": func(t *testing.T, fixture *releaseFixture) {
			fixture.replaceConfig(t, 0, []byte(`{"architecture":"amd64","os":"windows"}`))
		},
		"arm64": func(t *testing.T, fixture *releaseFixture) {
			fixture.replaceConfig(t, 1, []byte(`{"architecture":"arm64","os":"linux"}`))
		},
		"missing-config": func(t *testing.T, fixture *releaseFixture) {
			delete(fixture.configs, fixture.saved[2].Config)
			fixture.rewriteTarAndArtifacts(t, nil)
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

func TestDockerSaveManifestRejectsUnknownDuplicateAndTrailingJSON(t *testing.T) {
	tests := map[string]func(string) string{
		"unknown": func(value string) string {
			return strings.Replace(value, `"Config":`, `"Unknown":true,"Config":`, 1)
		},
		"duplicate": func(value string) string {
			return strings.Replace(value, `"Config":`, `"Config":"bad.json","Config":`, 1)
		},
		"trailing": func(value string) string { return value + `{}` },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			payload, err := json.Marshal(fixture.saved)
			if err != nil {
				t.Fatal(err)
			}
			fixture.rewriteTarAndArtifacts(t, []byte(mutate(string(payload))))
			assertPackageRejected(t, fixture.root)
		})
	}
}

func TestDockerConfigDigestAndCriticalFieldsAreStrict(t *testing.T) {
	t.Run("content-digest", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		config := fixture.saved[0].Config
		fixture.configs[config] = []byte(`{"architecture":"amd64","os":"linux","changed":true}`)
		fixture.rewriteTarAndArtifacts(t, nil)
		assertPackageRejected(t, fixture.root)
	})
	t.Run("case-alias", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		fixture.replaceConfig(t, 0, []byte(`{"architecture":"amd64","OS":"linux","os":"linux"}`))
		assertPackageRejected(t, fixture.root)
	})
	t.Run("duplicate", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		fixture.replaceConfig(t, 0, []byte(`{"architecture":"amd64","os":"linux","os":"linux"}`))
		assertPackageRejected(t, fixture.root)
	})
}

func newReleaseFixture(t *testing.T) *releaseFixture {
	t.Helper()
	root := t.TempDir()
	writeFixtureFiles(t, root)
	fixture := &releaseFixture{root: root, configs: map[string][]byte{}}
	fixture.manifest = releaseManifest{
		SchemaVersion: 1, Version: "v0.1.0", CreatedAt: "2026-08-30T12:00:00Z",
		SourceCommit: strings.Repeat("a", 40), SourceTree: strings.Repeat("b", 40),
		SourceSHA256: strings.Repeat("c", 64), Platform: "linux/amd64",
	}
	fixture.prepareImages(t)
	fixture.prepareOCIIndex(t)
	fixture.rewriteTarAndArtifacts(t, nil)
	return fixture
}

func writeFixtureFiles(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "bin", "ai-gdm-server-linux-amd64"), []byte("linux"))
	writeFile(t, filepath.Join(root, "bin", "ai-gdm-server-windows-amd64.exe"), []byte("windows"))
	writeFile(t, filepath.Join(root, "bin", "ai-gdm-healthcheck-linux-amd64"), []byte("health"))
	writeFile(t, filepath.Join(root, "compose.yaml"), []byte("services: {}\n"))
	writeFile(t, filepath.Join(root, "deploy", "compose.offline.yaml"), []byte("services: {}\n"))
	writeFile(t, filepath.Join(root, "deploy", "release-images.env"), []byte(releaseImageEnvironment()))
	writeFile(t, filepath.Join(root, "deploy", "runtime.env.example"), []byte(runtimeTemplate()))
	writeFile(t, filepath.Join(root, "docs", "deployment-v1.md"), []byte("deployment\n"))
	writeFile(t, filepath.Join(root, "images", "IMAGE-SOURCES.txt"), []byte("sources\n"))
	writeFile(t, filepath.Join(root, "SHA256SUMS"), []byte("generated externally\n"))
}

func (fixture *releaseFixture) prepareImages(t *testing.T) {
	t.Helper()
	references := []string{
		"ai-gdm/server:v0.1.0", "ai-gdm/postgis:17-3.5-v0.1.0", "ai-gdm/redis:7.4.10-v0.1.0",
	}
	sources := []string{"", "postgis/postgis@sha256:" + strings.Repeat("d", 64),
		"redis@sha256:" + strings.Repeat("e", 64)}
	for index, reference := range references {
		payload := []byte(fmt.Sprintf(`{"architecture":"amd64","config":{"Image":"fixture-%d"},"os":"linux"}`, index))
		digest := sha256Hex(payload)
		config := "blobs/sha256/" + digest
		fixture.configs[config] = payload
		fixture.saved = append(fixture.saved, savedImage{
			Config: config, RepoTags: []string{reference}, Layers: []string{"blobs/sha256/" + strings.Repeat(string(rune('1'+index)), 64)},
		})
		fixture.manifest.Images = append(fixture.manifest.Images, releaseImage{
			Reference: reference, Source: sources[index], ID: "sha256:" + digest,
			Platform: "linux/amd64", SizeBytes: int64(1024 + index),
		})
	}
}

func (fixture *releaseFixture) replaceConfig(t *testing.T, index int, payload []byte) {
	t.Helper()
	oldConfig := fixture.saved[index].Config
	delete(fixture.configs, oldConfig)
	digest := sha256Hex(payload)
	config := "blobs/sha256/" + digest
	fixture.configs[config] = payload
	fixture.saved[index].Config = config
	fixture.manifest.Images[index].ID = "sha256:" + digest
	fixture.rewriteTarAndArtifacts(t, nil)
}

func (fixture *releaseFixture) rewriteTarAndArtifacts(t *testing.T, rawManifest []byte) {
	t.Helper()
	filename := filepath.Join(fixture.root, filepath.FromSlash(imageTarPath))
	writeDockerTar(t, filename, fixture.saved, fixture.configs, rawManifest)
	files, err := collectPackageFiles(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	fixture.manifest.Artifacts = buildArtifacts(t, fixture.root, files)
	fixture.writeManifest(t)
}

func (fixture *releaseFixture) writeManifest(t *testing.T) {
	t.Helper()
	payload, err := json.MarshalIndent(fixture.manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(fixture.root, "manifest.json"), append(payload, '\n'))
}

func buildArtifacts(t *testing.T, root string, files map[string]os.FileInfo) []releaseArtifact {
	t.Helper()
	paths := make([]string, 0, len(files))
	for filename := range files {
		paths = append(paths, filename)
	}
	sort.Strings(paths)
	result := make([]releaseArtifact, 0, len(paths))
	for _, filename := range paths {
		digest, err := hashFile(filepath.Join(root, filepath.FromSlash(filename)))
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, releaseArtifact{Path: filename, SHA256: digest, SizeBytes: files[filename].Size()})
	}
	return result
}

func writeDockerTar(t *testing.T, filename string, saved []savedImage, configs map[string][]byte, rawManifest []byte) {
	t.Helper()
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	if rawManifest == nil {
		rawManifest, err = json.Marshal(saved)
	}
	if err == nil {
		err = writeTarFile(writer, "manifest.json", rawManifest)
	}
	keys := make([]string, 0, len(configs))
	for key := range configs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err == nil {
			err = writeTarFile(writer, key, configs[key])
		}
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
}

func writeTarFile(writer *tar.Writer, name string, payload []byte) error {
	header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(payload)), Typeflag: tar.TypeReg}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	_, err := writer.Write(payload)
	return err
}

func releaseImageEnvironment() string {
	return "AI_GDM_IMAGE=ai-gdm/server:v0.1.0\n" +
		"AI_GDM_POSTGIS_IMAGE=ai-gdm/postgis:17-3.5-v0.1.0\n" +
		"AI_GDM_REDIS_IMAGE=ai-gdm/redis:7.4.10-v0.1.0\n"
}

func runtimeTemplate() string {
	return "POSTGRES_" + "PASSWORD=\nREDIS_" + "PASSWORD=\nDATABASE_" + "URL=\nAPP_ADMIN_" + "TOKEN=\n" +
		"AMAP_API_" + "KEY=\nBOCHA_API_" + "KEY=\nLLM_API_" + "KEY=\nAPP_ENV=production\n"
}

func mutateManifestText(t *testing.T, fixture *releaseFixture, oldValue, newValue string) {
	t.Helper()
	filename := filepath.Join(fixture.root, "manifest.json")
	payload, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(payload), oldValue, newValue, 1)
	if updated == string(payload) {
		t.Fatal("manifest 变更目标不存在")
	}
	writeFile(t, filename, []byte(updated))
}

func assertPackageRejected(t *testing.T, root string) {
	t.Helper()
	if err := validatePackage(root); err == nil {
		t.Fatal("损坏的发布包未被拒绝")
	}
}

func writeFile(t *testing.T, filename string, payload []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, filename string, payload []byte) {
	t.Helper()
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(payload); err == nil {
		err = file.Close()
	} else {
		_ = file.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
