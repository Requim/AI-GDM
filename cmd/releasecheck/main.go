package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	manifestLimit   = 2 << 20
	envLimit        = 64 << 10
	configLimit     = 2 << 20
	descriptorLimit = 16 << 20
	maxJSONDepth    = 64
	imageTarPath    = "images/ai-gdm-images-linux-amd64.tar"
)

var (
	versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	hex40Pattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	pathPattern    = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

var requiredDeploymentArtifacts = [...]string{"deploy/deploy.sh", "deploy/deploy.ps1"}

type releaseManifest struct {
	SchemaVersion int64             `json:"schemaVersion"`
	Version       string            `json:"version"`
	CreatedAt     string            `json:"createdAt"`
	SourceCommit  string            `json:"sourceCommit"`
	SourceTree    string            `json:"sourceTree"`
	SourceSHA256  string            `json:"sourceSha256"`
	Platform      string            `json:"platform"`
	Images        []releaseImage    `json:"images"`
	Artifacts     []releaseArtifact `json:"artifacts"`
}

type releaseImage struct {
	Reference string `json:"reference"`
	Source    string `json:"source,omitempty"`
	ID        string `json:"id"`
	Platform  string `json:"platform"`
	SizeBytes int64  `json:"sizeBytes"`
}

type releaseArtifact struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
}

type savedImage struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) != 1 {
		return fmt.Errorf("发布包校验只接受一个目录参数")
	}
	if err := validatePackage(arguments[0]); err != nil {
		return fmt.Errorf("发布包校验失败: %w", err)
	}
	fmt.Println("发布包只读校验通过")
	return nil
}

func validatePackage(root string) error {
	manifest, err := loadReleaseManifest(filepath.Join(root, "manifest.json"))
	if err != nil {
		return err
	}
	if err = validatePackageArtifacts(root, manifest.Artifacts); err != nil {
		return err
	}
	references, err := loadReleaseImages(filepath.Join(root, "deploy", "release-images.env"))
	if err != nil {
		return err
	}
	if err = validateRuntimeTemplate(filepath.Join(root, "deploy", "runtime.env.example")); err != nil {
		return err
	}
	return validateSavedImages(filepath.Join(root, filepath.FromSlash(imageTarPath)), manifest.Images, references)
}

func loadReleaseManifest(filename string) (releaseManifest, error) {
	payload, err := readBoundedFile(filename, manifestLimit)
	if err != nil {
		return releaseManifest{}, fmt.Errorf("读取 manifest.json: %w", err)
	}
	value, err := decodeReleaseManifest(payload)
	if err != nil {
		return releaseManifest{}, fmt.Errorf("解析 manifest.json: %w", err)
	}
	if err = validateManifestIdentity(value); err != nil {
		return releaseManifest{}, err
	}
	if err = validateReleaseImages(value.Images); err != nil {
		return releaseManifest{}, err
	}
	return value, nil
}

func validateManifestIdentity(value releaseManifest) error {
	if value.SchemaVersion != 1 || !versionPattern.MatchString(value.Version) {
		return fmt.Errorf("发布版本或 schemaVersion 无效")
	}
	parsed, err := time.Parse("2006-01-02T15:04:05Z", value.CreatedAt)
	if err != nil || parsed.Format("2006-01-02T15:04:05Z") != value.CreatedAt {
		return fmt.Errorf("createdAt 不是严格 UTC 秒级时间")
	}
	if value.SourceCommit != "unknown" && !hex40Pattern.MatchString(value.SourceCommit) {
		return fmt.Errorf("源码提交 SHA 无效")
	}
	if !hex40Pattern.MatchString(value.SourceTree) {
		return fmt.Errorf("源码提交或 tree SHA 无效")
	}
	if !hex64Pattern.MatchString(value.SourceSHA256) || value.Platform != "linux/amd64" {
		return fmt.Errorf("源码摘要或发布平台无效")
	}
	return nil
}

func validateReleaseImages(images []releaseImage) error {
	if len(images) != 3 {
		return fmt.Errorf("发布清单必须包含三个运行镜像")
	}
	references, identifiers := map[string]struct{}{}, map[string]struct{}{}
	for _, image := range images {
		if !validImageReference(image.Reference) || !validImageID(image.ID) {
			return fmt.Errorf("发布镜像引用或标识无效")
		}
		if image.Platform != "linux/amd64" || image.SizeBytes <= 0 {
			return fmt.Errorf("发布镜像平台或大小无效")
		}
		if duplicate(references, image.Reference) || duplicate(identifiers, image.ID) {
			return fmt.Errorf("发布镜像引用或标识重复")
		}
		if image.Source != "" && !validImageSource(image.Source) {
			return fmt.Errorf("发布镜像来源无效")
		}
		if image.Source != "" && !sourceDigestMatchesImage(image.Source, image.ID) {
			return fmt.Errorf("发布镜像来源摘要与镜像 ID 不一致")
		}
	}
	return nil
}

func validatePackageArtifacts(root string, artifacts []releaseArtifact) error {
	files, err := collectPackageFiles(root)
	if err != nil {
		return err
	}
	if len(artifacts) != len(files) {
		return fmt.Errorf("artifact 数量与发布包文件不一致")
	}
	seen := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		if err = validateArtifact(root, artifact, files, seen); err != nil {
			return err
		}
	}
	for _, required := range requiredDeploymentArtifacts {
		if _, exists := seen[required]; !exists {
			return fmt.Errorf("发布包缺少一键部署制品: %s", required)
		}
	}
	return nil
}

func validateArtifact(root string, artifact releaseArtifact, files map[string]fs.FileInfo, seen map[string]struct{}) error {
	if !safeRelativePath(artifact.Path) || !hex64Pattern.MatchString(artifact.SHA256) || artifact.SizeBytes < 0 {
		return fmt.Errorf("artifact 路径、摘要或大小无效: %q", artifact.Path)
	}
	if duplicate(seen, artifact.Path) {
		return fmt.Errorf("artifact 路径重复: %s", artifact.Path)
	}
	info, exists := files[artifact.Path]
	if !exists || info.Size() != artifact.SizeBytes {
		return fmt.Errorf("artifact 文件缺失或大小不一致: %s", artifact.Path)
	}
	digest, err := hashFile(filepath.Join(root, filepath.FromSlash(artifact.Path)))
	if err != nil || digest != artifact.SHA256 {
		return fmt.Errorf("artifact 摘要不一致: %s", artifact.Path)
	}
	return nil
}

func collectPackageFiles(root string) (map[string]fs.FileInfo, error) {
	files := map[string]fs.FileInfo{}
	err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("发布包包含符号链接: %s", filename)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("发布包包含非普通文件: %s", filename)
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative != "manifest.json" && relative != "SHA256SUMS" {
			files[relative] = info
		}
		return nil
	})
	return files, err
}

func loadReleaseImages(filename string) (map[string]string, error) {
	payload, err := readBoundedFile(filename, envLimit)
	if err != nil {
		return nil, fmt.Errorf("读取 release-images.env: %w", err)
	}
	expected := map[string]struct{}{
		"AI_GDM_IMAGE": {}, "AI_GDM_POSTGIS_IMAGE": {}, "AI_GDM_REDIS_IMAGE": {},
	}
	values, err := parseExactEnvironment(payload, expected)
	if err != nil {
		return nil, fmt.Errorf("解析 release-images.env: %w", err)
	}
	seen := map[string]struct{}{}
	for key, value := range values {
		if !validImageReference(value) || !validReleaseImageRole(key, value) || duplicate(seen, value) {
			return nil, fmt.Errorf("%s 的镜像引用无效或重复", key)
		}
	}
	return values, nil
}

func validateRuntimeTemplate(filename string) error {
	payload, err := readBoundedFile(filename, envLimit)
	if err != nil {
		return fmt.Errorf("读取 runtime.env.example: %w", err)
	}
	sensitive := map[string]struct{}{
		"POSTGRES_" + "PASSWORD": {}, "REDIS_" + "PASSWORD": {}, "DATABASE_" + "URL": {},
		"APP_ADMIN_" + "TOKEN": {}, "AMAP_API_" + "KEY": {}, "BOCHA_API_" + "KEY": {},
		"LLM_API_" + "KEY": {},
	}
	counts := map[string]int{}
	for number, line := range splitEnvironmentLines(payload) {
		key, value, skip, parseErr := parseEnvironmentLine(line)
		if parseErr != nil {
			return fmt.Errorf("runtime.env.example 第 %d 行无效", number+1)
		}
		if skip {
			continue
		}
		if _, tracked := sensitive[key]; tracked {
			counts[key]++
			if strings.TrimSpace(value) != "" {
				return fmt.Errorf("runtime.env.example 的 %s 必须为空", key)
			}
		}
	}
	for key := range sensitive {
		if counts[key] != 1 {
			return fmt.Errorf("runtime.env.example 的 %s 必须恰好出现一次", key)
		}
	}
	return nil
}

func validateSavedImages(filename string, images []releaseImage, references map[string]string) error {
	payload, err := readTarEntry(filename, "manifest.json", manifestLimit)
	if err != nil {
		return fmt.Errorf("读取 Docker manifest: %w", err)
	}
	saved, err := decodeSavedImages(payload)
	if err != nil {
		return fmt.Errorf("解析 Docker manifest: %w", err)
	}
	bindings, err := validateSavedImageBindings(saved, images, references)
	if err != nil {
		return err
	}
	configs := savedConfigTargets(bindings)
	payloads, err := readTarEntries(filename, configs, configLimit)
	if err != nil {
		return fmt.Errorf("读取 Docker 配置: %w", err)
	}
	for config, expectedDigest := range configs {
		if err = validateImageConfig(payloads[config], expectedDigest); err != nil {
			return fmt.Errorf("Docker 配置 %s 无效: %w", config, err)
		}
	}
	indexPayload, err := readTarEntry(filename, "index.json", manifestLimit)
	if err != nil {
		return fmt.Errorf("读取 OCI index: %w", err)
	}
	return validateOCIImageBindings(filename, indexPayload, bindings, images, payloads)
}

func validateSavedImageBindings(saved []savedImage, images []releaseImage, references map[string]string) (map[string]savedBinding, error) {
	if len(saved) != 3 {
		return nil, fmt.Errorf("Docker 原始镜像必须恰好包含三个清单项")
	}
	expected := map[string]releaseImage{}
	for _, image := range images {
		expected[image.Reference] = image
	}
	if !sameReferenceSet(expected, references) {
		return nil, fmt.Errorf("release-images.env 与 manifest 镜像不一致")
	}
	bindings, tags := map[string]savedBinding{}, map[string]struct{}{}
	configs := map[string]struct{}{}
	for _, entry := range saved {
		if len(entry.RepoTags) != 1 || duplicate(tags, entry.RepoTags[0]) {
			return nil, fmt.Errorf("Docker RepoTag 缺失、重复或包含额外标签")
		}
		_, exists := expected[entry.RepoTags[0]]
		digest, digestErr := dockerConfigDigest(entry.Config)
		if !exists || digestErr != nil {
			return nil, fmt.Errorf("Docker RepoTag、Config 与镜像 ID 未绑定")
		}
		if duplicate(configs, entry.Config) {
			return nil, fmt.Errorf("Docker Config 重复")
		}
		bindings[entry.RepoTags[0]] = savedBinding{
			ConfigPath: entry.Config, ConfigDigest: digest, Layers: append([]string(nil), entry.Layers...),
		}
	}
	return bindings, nil
}

func sameReferenceSet(images map[string]releaseImage, references map[string]string) bool {
	if len(images) != len(references) {
		return false
	}
	for _, reference := range references {
		if _, exists := images[reference]; !exists {
			return false
		}
	}
	return true
}

func validateImageConfig(payload []byte, expectedDigest string) error {
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != expectedDigest {
		return fmt.Errorf("配置内容摘要与路径不一致")
	}
	if err := validateJSONTokens(payload); err != nil {
		return err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(payload, &values); err != nil {
		return err
	}
	for key := range values {
		if (strings.EqualFold(key, "os") && key != "os") ||
			(strings.EqualFold(key, "architecture") && key != "architecture") {
			return fmt.Errorf("平台字段大小写不规范")
		}
	}
	operatingSystem, err := decodeString(values["os"])
	if err != nil || operatingSystem != "linux" {
		return fmt.Errorf("镜像 OS 不是 linux")
	}
	architecture, err := decodeString(values["architecture"])
	if err != nil || architecture != "amd64" {
		return fmt.Errorf("镜像架构不是 amd64")
	}
	return nil
}

func decodeReleaseManifest(payload []byte) (releaseManifest, error) {
	if err := validateJSONTokens(payload); err != nil {
		return releaseManifest{}, err
	}
	fields, err := exactObject(payload, requiredManifestFields())
	if err != nil {
		return releaseManifest{}, err
	}
	value, err := decodeManifestScalars(fields)
	if err != nil {
		return releaseManifest{}, err
	}
	if value.Images, err = decodeReleaseImageArray(fields["images"]); err != nil {
		return releaseManifest{}, err
	}
	if value.Artifacts, err = decodeArtifactArray(fields["artifacts"]); err != nil {
		return releaseManifest{}, err
	}
	return value, nil
}

func requiredManifestFields() map[string]bool {
	return map[string]bool{
		"schemaVersion": true, "version": true, "createdAt": true, "sourceCommit": true,
		"sourceTree": true, "sourceSha256": true, "platform": true, "images": true, "artifacts": true,
	}
}

func decodeManifestScalars(fields map[string]json.RawMessage) (releaseManifest, error) {
	var value releaseManifest
	var err error
	if value.SchemaVersion, err = decodeInt64(fields["schemaVersion"]); err != nil {
		return value, err
	}
	stringsByField := []struct {
		raw    json.RawMessage
		target *string
	}{
		{fields["version"], &value.Version}, {fields["createdAt"], &value.CreatedAt},
		{fields["sourceCommit"], &value.SourceCommit}, {fields["sourceTree"], &value.SourceTree},
		{fields["sourceSha256"], &value.SourceSHA256}, {fields["platform"], &value.Platform},
	}
	for _, item := range stringsByField {
		if *item.target, err = decodeString(item.raw); err != nil {
			return value, err
		}
	}
	return value, nil
}

func decodeReleaseImageArray(payload []byte) ([]releaseImage, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(payload, &values); err != nil {
		return nil, fmt.Errorf("images 不是数组")
	}
	result := make([]releaseImage, 0, len(values))
	for _, raw := range values {
		value, err := decodeReleaseImage(raw)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func decodeReleaseImage(payload []byte) (releaseImage, error) {
	fields, err := exactObject(payload, map[string]bool{
		"reference": true, "source": false, "id": true, "platform": true, "sizeBytes": true,
	})
	if err != nil {
		return releaseImage{}, err
	}
	value := releaseImage{}
	if value.Reference, err = decodeString(fields["reference"]); err != nil {
		return value, err
	}
	if raw, exists := fields["source"]; exists {
		if value.Source, err = decodeString(raw); err != nil {
			return value, err
		}
	}
	if value.ID, err = decodeString(fields["id"]); err != nil {
		return value, err
	}
	if value.Platform, err = decodeString(fields["platform"]); err != nil {
		return value, err
	}
	value.SizeBytes, err = decodeInt64(fields["sizeBytes"])
	return value, err
}

func decodeArtifactArray(payload []byte) ([]releaseArtifact, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(payload, &values); err != nil {
		return nil, fmt.Errorf("artifacts 不是数组")
	}
	result := make([]releaseArtifact, 0, len(values))
	for _, raw := range values {
		fields, err := exactObject(raw, map[string]bool{"path": true, "sha256": true, "sizeBytes": true})
		if err != nil {
			return nil, err
		}
		value, err := decodeArtifact(fields)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func decodeArtifact(fields map[string]json.RawMessage) (releaseArtifact, error) {
	value := releaseArtifact{}
	var err error
	if value.Path, err = decodeString(fields["path"]); err != nil {
		return value, err
	}
	if value.SHA256, err = decodeString(fields["sha256"]); err != nil {
		return value, err
	}
	value.SizeBytes, err = decodeInt64(fields["sizeBytes"])
	return value, err
}

func decodeSavedImages(payload []byte) ([]savedImage, error) {
	if err := validateJSONTokens(payload); err != nil {
		return nil, err
	}
	var values []json.RawMessage
	if err := json.Unmarshal(payload, &values); err != nil {
		return nil, fmt.Errorf("Docker manifest 不是数组")
	}
	result := make([]savedImage, 0, len(values))
	for _, raw := range values {
		value, err := decodeSavedImage(raw)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func decodeSavedImage(payload []byte) (savedImage, error) {
	fields, err := exactObject(payload, map[string]bool{
		"Config": true, "RepoTags": true, "Layers": true, "LayerSources": false,
	})
	if err != nil {
		return savedImage{}, err
	}
	value := savedImage{}
	if value.Config, err = decodeString(fields["Config"]); err != nil {
		return value, err
	}
	if value.RepoTags, err = decodeStringArray(fields["RepoTags"]); err != nil {
		return value, err
	}
	if value.Layers, err = decodeStringArray(fields["Layers"]); err != nil || len(value.Layers) == 0 {
		return value, fmt.Errorf("Docker Layers 无效")
	}
	for _, layer := range value.Layers {
		if !safeRelativePath(layer) {
			return value, fmt.Errorf("Docker layer 路径无效")
		}
	}
	return value, nil
}

func exactObject(payload []byte, expected map[string]bool) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("JSON 对象无效")
	}
	for key := range fields {
		if _, exists := expected[key]; !exists {
			return nil, fmt.Errorf("JSON 包含未知字段 %q", key)
		}
	}
	for key, required := range expected {
		if _, exists := fields[key]; required && !exists {
			return nil, fmt.Errorf("JSON 缺少字段 %q", key)
		}
	}
	return fields, nil
}

func validateJSONTokens(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("JSON 包含尾随内容")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return fmt.Errorf("JSON 嵌套层级超限")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		return scanJSONObject(decoder, depth+1)
	case '[':
		return scanJSONArray(decoder, depth+1)
	default:
		return fmt.Errorf("JSON 定界符无效")
	}
}

func scanJSONObject(decoder *json.Decoder, depth int) error {
	seen := map[string]struct{}{}
	for decoder.More() {
		token, err := decoder.Token()
		key, valid := token.(string)
		if err != nil || !valid || duplicate(seen, key) {
			return fmt.Errorf("JSON 字段重复或无效")
		}
		if err = scanJSONValue(decoder, depth); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

func scanJSONArray(decoder *json.Decoder, depth int) error {
	for decoder.More() {
		if err := scanJSONValue(decoder, depth); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

func parseExactEnvironment(payload []byte, expected map[string]struct{}) (map[string]string, error) {
	values := map[string]string{}
	for _, line := range splitEnvironmentLines(payload) {
		key, value, skip, err := parseEnvironmentLine(line)
		if err != nil || skip {
			return nil, fmt.Errorf("环境文件只允许 KEY=value")
		}
		if _, exists := expected[key]; !exists {
			return nil, fmt.Errorf("环境文件包含未知键 %s", key)
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("环境键 %s 重复", key)
		}
		values[key] = value
	}
	if len(values) != len(expected) {
		return nil, fmt.Errorf("环境文件缺少必需镜像键")
	}
	return values, nil
}

func splitEnvironmentLines(payload []byte) []string {
	if !utf8.Valid(payload) || bytes.IndexByte(payload, 0) >= 0 {
		return []string{"\x00"}
	}
	text := strings.TrimSuffix(string(payload), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func parseEnvironmentLine(line string) (string, string, bool, error) {
	if strings.ContainsRune(line, '\r') {
		return "", "", false, fmt.Errorf("环境行包含 CR")
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", true, nil
	}
	key, value, found := strings.Cut(trimmed, "=")
	key = strings.TrimSpace(key)
	if !found || key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false, fmt.Errorf("环境赋值无效")
	}
	return key, strings.TrimSpace(value), false, nil
}

func readTarEntry(filename, target string, maximum int64) ([]byte, error) {
	values, err := readTarEntries(filename, map[string]string{target: ""}, maximum)
	if err != nil {
		return nil, err
	}
	return values[target], nil
}

func readTarEntries(filename string, targets map[string]string, maximum int64) (map[string][]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader, result := tar.NewReader(file), map[string][]byte{}
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nil, nextErr
		}
		if _, wanted := targets[header.Name]; !wanted {
			continue
		}
		if _, exists := result[header.Name]; exists {
			return nil, fmt.Errorf("tar 条目重复: %s", header.Name)
		}
		payload, readErr := readTarPayload(reader, header, maximum)
		if readErr != nil {
			return nil, readErr
		}
		result[header.Name] = payload
	}
	if len(result) != len(targets) {
		return nil, fmt.Errorf("tar 缺少必需条目")
	}
	return result, nil
}

func readTarPayload(reader io.Reader, header *tar.Header, maximum int64) ([]byte, error) {
	if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
		return nil, fmt.Errorf("tar 必需条目不是普通文件")
	}
	if header.Size < 0 || header.Size > maximum {
		return nil, fmt.Errorf("tar 必需条目超出大小预算")
	}
	payload, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil || int64(len(payload)) != header.Size {
		return nil, fmt.Errorf("读取 tar 必需条目失败")
	}
	return payload, nil
}

func dockerConfigDigest(config string) (string, error) {
	if !safeRelativePath(config) {
		return "", fmt.Errorf("Docker Config 路径无效")
	}
	parts := strings.Split(config, "/")
	digest := ""
	if len(parts) == 3 && parts[0] == "blobs" && parts[1] == "sha256" {
		digest = parts[2]
	} else if len(parts) == 1 && strings.HasSuffix(parts[0], ".json") {
		digest = strings.TrimSuffix(parts[0], ".json")
	}
	if !hex64Pattern.MatchString(digest) {
		return "", fmt.Errorf("Docker Config 摘要路径无效")
	}
	return digest, nil
}

func readBoundedFile(filename string, maximum int64) ([]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, fmt.Errorf("文件类型或大小无效")
	}
	payload, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(payload)) != info.Size() {
		return nil, fmt.Errorf("文件读取不完整")
	}
	return payload, nil
}

func hashFile(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func decodeString(payload []byte) (string, error) {
	var value string
	if len(payload) == 0 || json.Unmarshal(payload, &value) != nil {
		return "", fmt.Errorf("JSON 字符串无效")
	}
	return value, nil
}

func decodeInt64(payload []byte) (int64, error) {
	var value int64
	if len(payload) == 0 || json.Unmarshal(payload, &value) != nil {
		return 0, fmt.Errorf("JSON 整数无效")
	}
	return value, nil
}

func decodeStringArray(payload []byte) ([]string, error) {
	var values []string
	if len(payload) == 0 || json.Unmarshal(payload, &values) != nil || values == nil {
		return nil, fmt.Errorf("JSON 字符串数组无效")
	}
	return values, nil
}

func validImageID(value string) bool {
	return strings.HasPrefix(value, "sha256:") && hex64Pattern.MatchString(strings.TrimPrefix(value, "sha256:"))
}

func validImageReference(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, " \t\r\n@") {
		return false
	}
	colon, slash := strings.LastIndexByte(value, ':'), strings.LastIndexByte(value, '/')
	return colon > slash && colon > 0 && colon < len(value)-1
}

func validImageSource(value string) bool {
	if validImageReference(value) {
		return true
	}
	name, digest, found := strings.Cut(value, "@sha256:")
	return found && name != "" && !strings.ContainsAny(name, " \t\r\n@") && hex64Pattern.MatchString(digest)
}

func sourceDigestMatchesImage(source, imageID string) bool {
	_, digest, found := strings.Cut(source, "@sha256:")
	return found && imageID == "sha256:"+digest
}

func validReleaseImageRole(key, value string) bool {
	prefixes := map[string]string{
		"AI_GDM_IMAGE": "ai-gdm/server:", "AI_GDM_POSTGIS_IMAGE": "ai-gdm/postgis:",
		"AI_GDM_REDIS_IMAGE": "ai-gdm/redis:",
	}
	return strings.HasPrefix(value, prefixes[key])
}

func safeRelativePath(value string) bool {
	return value != "" && value != "." && pathPattern.MatchString(value) && !strings.Contains(value, "\\") &&
		!strings.HasPrefix(value, "/") && path.Clean(value) == value && !strings.HasPrefix(value, "../")
}

func duplicate(values map[string]struct{}, value string) bool {
	if _, exists := values[value]; exists {
		return true
	}
	values[value] = struct{}{}
	return false
}
