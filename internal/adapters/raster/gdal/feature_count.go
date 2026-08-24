package gdal

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Requim/AI-GDM/internal/domain"
)

const emptyFeatureCollection = `{"type":"FeatureCollection","features":[]}`

func countFeatures(path string, maxBytes int64, maxCount int) (int, error) {
	if err := validateFileSize(path, maxBytes); err != nil {
		return 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("打开 GDAL GeoJSON: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return 0, fmt.Errorf("%w: GDAL GeoJSON 根对象无效", domain.ErrInvalidInput)
	}
	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return 0, fmt.Errorf("%w: 读取 GDAL GeoJSON 字段: %v", domain.ErrInvalidInput, err)
		}
		if name == "features" {
			return countFeatureArray(decoder, maxCount)
		}
		var ignored json.RawMessage
		if err = decoder.Decode(&ignored); err != nil {
			return 0, fmt.Errorf("%w: 跳过 GDAL GeoJSON 字段: %v", domain.ErrInvalidInput, err)
		}
	}
	return 0, fmt.Errorf("%w: GDAL GeoJSON 缺少 features", domain.ErrInvalidInput)
}

func countFeatureArray(decoder *json.Decoder, maxCount int) (int, error) {
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return 0, fmt.Errorf("%w: GDAL GeoJSON features 不是数组", domain.ErrInvalidInput)
	}
	count := 0
	for decoder.More() {
		var ignored json.RawMessage
		if err = decoder.Decode(&ignored); err != nil {
			return 0, fmt.Errorf("%w: 读取 GDAL 要素: %v", domain.ErrInvalidInput, err)
		}
		count++
		if count > maxCount {
			return 0, fmt.Errorf("%w: GDAL 要素超过 %d 个", domain.ErrInvalidInput, maxCount)
		}
	}
	return count, nil
}

func validateFileSize(path string, maxBytes int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("检查 GDAL GeoJSON: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return fmt.Errorf("%w: GDAL GeoJSON 文件类型或大小无效", domain.ErrInvalidInput)
	}
	return nil
}

func prepareEmptyStatistics(path string, count int, err error) error {
	if err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("%w: 空风险区状态不一致", domain.ErrInvalidInput)
	}
	if err = os.WriteFile(path, []byte(emptyFeatureCollection), 0o600); err != nil {
		return fmt.Errorf("写入空风险区结果: %w", err)
	}
	return nil
}

func ensureNoGeometryErrors(payload []byte) error {
	var info struct {
		Layers []struct {
			FeatureCount int `json:"featureCount"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(payload, &info); err != nil {
		return fmt.Errorf("%w: 解析 GDAL 几何检查结果: %v", domain.ErrInvalidInput, err)
	}
	for _, layer := range info.Layers {
		if layer.FeatureCount > 0 {
			return fmt.Errorf("%w: GDAL 检测到无效风险区几何", domain.ErrInvalidInput)
		}
	}
	if len(info.Layers) > 1 {
		return fmt.Errorf("%w: GDAL 检测到无效风险区几何", domain.ErrInvalidInput)
	}
	return nil
}
