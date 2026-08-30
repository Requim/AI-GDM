package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	ociIndexMediaType    = "application/vnd.oci.image.index.v1+json"
	ociManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	ociConfigMediaType   = "application/vnd.oci.image.config.v1+json"
	ociImageNameKey      = "io.containerd.image.name"
	maxOCIDescriptors    = 64
)

type savedBinding struct {
	ConfigPath   string
	ConfigDigest string
	Layers       []string
}

type ociIndex struct {
	SchemaVersion int64
	MediaType     string
	Manifests     []ociDescriptor
}

type ociDescriptor struct {
	MediaType   string
	Digest      string
	Size        int64
	Annotations map[string]string
	Platform    *ociPlatform
}

type ociPlatform struct {
	Architecture string
	OS           string
	Variant      string
}

type ociImageManifest struct {
	SchemaVersion int64
	MediaType     string
	Config        ociDescriptor
	Layers        []ociDescriptor
}

func savedConfigTargets(bindings map[string]savedBinding) map[string]string {
	targets := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		targets[binding.ConfigPath] = binding.ConfigDigest
	}
	return targets
}

func validateOCIImageBindings(filename string, rawIndex []byte, bindings map[string]savedBinding, images []releaseImage, configs map[string][]byte) error {
	index, err := decodeOCIIndex(rawIndex)
	if err != nil {
		return fmt.Errorf("解析 OCI index: %w", err)
	}
	top, err := bindTopDescriptors(index, images)
	if err != nil {
		return err
	}
	topPayloads, err := loadDescriptorPayloads(filename, descriptorValues(top))
	if err != nil {
		return err
	}
	manifests, nested, err := resolvePlatformManifests(top, topPayloads)
	if err != nil {
		return err
	}
	nestedPayloads, err := loadDescriptorPayloads(filename, nested)
	if err != nil {
		return err
	}
	return validatePlatformManifests(manifests, topPayloads, nestedPayloads, bindings, configs)
}

func bindTopDescriptors(index ociIndex, images []releaseImage) (map[string]ociDescriptor, error) {
	if len(index.Manifests) != len(images) {
		return nil, fmt.Errorf("OCI 顶层索引必须恰好包含三个镜像")
	}
	expected, result := releaseImagesByReference(images), map[string]ociDescriptor{}
	for _, descriptor := range index.Manifests {
		reference, err := descriptorReference(descriptor)
		image, exists := expected[reference]
		if err != nil || !exists || descriptor.Digest != image.ID || descriptor.Platform != nil {
			return nil, fmt.Errorf("OCI 镜像名称、摘要或平台绑定无效")
		}
		if descriptor.MediaType != ociManifestMediaType && descriptor.MediaType != ociIndexMediaType {
			return nil, fmt.Errorf("OCI 顶层 descriptor 类型无效")
		}
		if _, duplicate := result[reference]; duplicate {
			return nil, fmt.Errorf("OCI 顶层镜像重复")
		}
		result[reference] = descriptor
	}
	return result, nil
}

func descriptorReference(descriptor ociDescriptor) (string, error) {
	name := descriptor.Annotations[ociImageNameKey]
	if !strings.HasPrefix(name, "docker.io/") {
		return "", fmt.Errorf("OCI 镜像名称缺失")
	}
	reference := strings.TrimPrefix(name, "docker.io/")
	if !validImageReference(reference) {
		return "", fmt.Errorf("OCI 镜像名称无效")
	}
	return reference, nil
}

func resolvePlatformManifests(top map[string]ociDescriptor, payloads map[string][]byte) (map[string]ociDescriptor, []ociDescriptor, error) {
	result := map[string]ociDescriptor{}
	nested := make([]ociDescriptor, 0, len(top))
	for reference, descriptor := range top {
		if descriptor.MediaType == ociManifestMediaType {
			result[reference] = descriptor
			continue
		}
		index, err := decodeOCIIndex(payloads[descriptorPath(descriptor.Digest)])
		if err != nil {
			return nil, nil, fmt.Errorf("解析 %s 的 OCI 平台索引: %w", reference, err)
		}
		platform, err := selectLinuxAMD64(index.Manifests)
		if err != nil {
			return nil, nil, fmt.Errorf("选择 %s 的 OCI 平台镜像: %w", reference, err)
		}
		result[reference] = platform
		nested = append(nested, platform)
	}
	return result, nested, nil
}

func selectLinuxAMD64(values []ociDescriptor) (ociDescriptor, error) {
	var selected ociDescriptor
	count := 0
	for _, descriptor := range values {
		platform := descriptor.Platform
		if descriptor.MediaType != ociManifestMediaType || platform == nil {
			continue
		}
		if platform.OS == "linux" && platform.Architecture == "amd64" && platform.Variant == "" {
			selected, count = descriptor, count+1
		}
	}
	if count != 1 {
		return ociDescriptor{}, fmt.Errorf("linux/amd64 descriptor 数量不是 1")
	}
	return selected, nil
}

func validatePlatformManifests(manifests map[string]ociDescriptor, top, nested map[string][]byte, bindings map[string]savedBinding, configs map[string][]byte) error {
	for reference, descriptor := range manifests {
		filename := descriptorPath(descriptor.Digest)
		payload := top[filename]
		if payload == nil {
			payload = nested[filename]
		}
		manifest, err := decodeOCIImageManifest(payload)
		if err != nil {
			return fmt.Errorf("解析 %s 的 OCI manifest: %w", reference, err)
		}
		binding := bindings[reference]
		if manifest.Config.Digest != "sha256:"+binding.ConfigDigest {
			return fmt.Errorf("OCI 平台 manifest 与 legacy Config 未绑定")
		}
		if manifest.Config.Size != int64(len(configs[binding.ConfigPath])) {
			return fmt.Errorf("OCI config descriptor 大小与配置内容不一致")
		}
		if !sameLayerDescriptors(binding.Layers, manifest.Layers) {
			return fmt.Errorf("OCI layers 与 legacy Layers 未绑定")
		}
	}
	return nil
}

func sameLayerDescriptors(legacy []string, descriptors []ociDescriptor) bool {
	if len(legacy) != len(descriptors) {
		return false
	}
	for index, layer := range legacy {
		if layer != descriptorPath(descriptors[index].Digest) || descriptors[index].Platform != nil {
			return false
		}
	}
	return true
}

func loadDescriptorPayloads(filename string, descriptors []ociDescriptor) (map[string][]byte, error) {
	if len(descriptors) == 0 {
		return map[string][]byte{}, nil
	}
	targets := map[string]string{}
	byPath := map[string]ociDescriptor{}
	for _, descriptor := range descriptors {
		path := descriptorPath(descriptor.Digest)
		if _, exists := byPath[path]; exists {
			return nil, fmt.Errorf("OCI descriptor 重复")
		}
		targets[path], byPath[path] = descriptorDigest(descriptor.Digest), descriptor
	}
	payloads, err := readTarEntries(filename, targets, descriptorLimit)
	if err != nil {
		return nil, fmt.Errorf("读取 OCI descriptor: %w", err)
	}
	for path, descriptor := range byPath {
		if err = validateDescriptorPayload(payloads[path], descriptor); err != nil {
			return nil, err
		}
	}
	return payloads, nil
}

func validateDescriptorPayload(payload []byte, descriptor ociDescriptor) error {
	if int64(len(payload)) != descriptor.Size {
		return fmt.Errorf("OCI descriptor 大小不一致")
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != descriptorDigest(descriptor.Digest) {
		return fmt.Errorf("OCI descriptor 摘要不一致")
	}
	return nil
}

func descriptorValues(values map[string]ociDescriptor) []ociDescriptor {
	result := make([]ociDescriptor, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func releaseImagesByReference(images []releaseImage) map[string]releaseImage {
	result := make(map[string]releaseImage, len(images))
	for _, image := range images {
		result[image.Reference] = image
	}
	return result
}

func descriptorPath(digest string) string {
	return "blobs/sha256/" + descriptorDigest(digest)
}

func descriptorDigest(value string) string {
	return strings.TrimPrefix(value, "sha256:")
}

func decodeOCIIndex(payload []byte) (ociIndex, error) {
	if err := validateJSONTokens(payload); err != nil {
		return ociIndex{}, err
	}
	fields, err := exactObject(payload, map[string]bool{
		"schemaVersion": true, "mediaType": true, "manifests": true,
	})
	if err != nil {
		return ociIndex{}, err
	}
	value := ociIndex{}
	if value.SchemaVersion, err = decodeInt64(fields["schemaVersion"]); err != nil {
		return value, err
	}
	if value.MediaType, err = decodeString(fields["mediaType"]); err != nil {
		return value, err
	}
	if value.SchemaVersion != 2 || value.MediaType != ociIndexMediaType {
		return value, fmt.Errorf("OCI index 版本或媒体类型无效")
	}
	value.Manifests, err = decodeOCIDescriptorArray(fields["manifests"])
	return value, err
}

func decodeOCIImageManifest(payload []byte) (ociImageManifest, error) {
	if err := validateJSONTokens(payload); err != nil {
		return ociImageManifest{}, err
	}
	fields, err := exactObject(payload, map[string]bool{
		"schemaVersion": true, "mediaType": true, "config": true, "layers": true, "annotations": false,
	})
	if err != nil {
		return ociImageManifest{}, err
	}
	value := ociImageManifest{}
	if value.SchemaVersion, err = decodeInt64(fields["schemaVersion"]); err != nil {
		return value, err
	}
	if value.MediaType, err = decodeString(fields["mediaType"]); err != nil {
		return value, err
	}
	if value.SchemaVersion != 2 || value.MediaType != ociManifestMediaType {
		return value, fmt.Errorf("OCI manifest 版本或媒体类型无效")
	}
	if raw, exists := fields["annotations"]; exists {
		if _, err = decodeOCIAnnotations(raw); err != nil {
			return value, err
		}
	}
	if value.Config, err = decodeOCIDescriptor(fields["config"]); err != nil {
		return value, err
	}
	if value.Config.MediaType != ociConfigMediaType || value.Config.Platform != nil {
		return value, fmt.Errorf("OCI config descriptor 无效")
	}
	value.Layers, err = decodeOCIDescriptorArray(fields["layers"])
	if err != nil || len(value.Layers) == 0 {
		return value, fmt.Errorf("OCI layers 无效")
	}
	return value, nil
}

func decodeOCIDescriptorArray(payload []byte) ([]ociDescriptor, error) {
	var raws []json.RawMessage
	if err := json.Unmarshal(payload, &raws); err != nil || len(raws) == 0 || len(raws) > maxOCIDescriptors {
		return nil, fmt.Errorf("OCI descriptor 数组无效")
	}
	values := make([]ociDescriptor, 0, len(raws))
	seen := map[string]struct{}{}
	for _, raw := range raws {
		value, err := decodeOCIDescriptor(raw)
		if err != nil || duplicate(seen, value.Digest) {
			return nil, fmt.Errorf("OCI descriptor 无效或重复")
		}
		values = append(values, value)
	}
	return values, nil
}

func decodeOCIDescriptor(payload []byte) (ociDescriptor, error) {
	fields, err := exactObject(payload, map[string]bool{
		"mediaType": true, "digest": true, "size": true, "annotations": false, "platform": false,
	})
	if err != nil {
		return ociDescriptor{}, err
	}
	value := ociDescriptor{}
	if value.MediaType, err = decodeString(fields["mediaType"]); err != nil {
		return value, err
	}
	if value.Digest, err = decodeString(fields["digest"]); err != nil {
		return value, err
	}
	if value.Size, err = decodeInt64(fields["size"]); err != nil {
		return value, err
	}
	if value.MediaType == "" || !validOCIDigest(value.Digest) || value.Size <= 0 {
		return value, fmt.Errorf("OCI descriptor 字段无效")
	}
	if raw, exists := fields["annotations"]; exists {
		if value.Annotations, err = decodeOCIAnnotations(raw); err != nil {
			return value, err
		}
	}
	if raw, exists := fields["platform"]; exists {
		platform, platformErr := decodeOCIPlatform(raw)
		value.Platform, err = &platform, platformErr
	}
	return value, err
}

func decodeOCIAnnotations(payload []byte) (map[string]string, error) {
	var values map[string]string
	if err := json.Unmarshal(payload, &values); err != nil || values == nil || len(values) > 64 {
		return nil, fmt.Errorf("OCI annotations 无效")
	}
	for key, value := range values {
		if key == "" || len(key) > 256 || len(value) > 2048 {
			return nil, fmt.Errorf("OCI annotation 超出边界")
		}
	}
	return values, nil
}

func decodeOCIPlatform(payload []byte) (ociPlatform, error) {
	fields, err := exactObject(payload, map[string]bool{
		"architecture": true, "os": true, "variant": false,
	})
	if err != nil {
		return ociPlatform{}, err
	}
	value := ociPlatform{}
	if value.Architecture, err = decodeString(fields["architecture"]); err != nil {
		return value, err
	}
	if value.OS, err = decodeString(fields["os"]); err != nil {
		return value, err
	}
	if raw, exists := fields["variant"]; exists {
		value.Variant, err = decodeString(raw)
	}
	return value, err
}

func validOCIDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && hex64Pattern.MatchString(descriptorDigest(value))
}
