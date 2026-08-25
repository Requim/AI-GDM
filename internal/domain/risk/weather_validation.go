package risk

import (
	"fmt"
	"strings"
	"time"

	"github.com/Requim/AI-GDM/internal/domain"
	"github.com/Requim/AI-GDM/internal/domain/hazard"
)

func validateWeatherContexts(values []WeatherContext, evaluatedAt time.Time) error {
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if err := validateWeatherContext(value, evaluatedAt); err != nil {
			return fmt.Errorf("气象上下文 %d: %w", index, err)
		}
		key := weatherLocationKey(value.Snapshot)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: 气象上下文包含重复位置", domain.ErrInvalidInput)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateWeatherContext(value WeatherContext, evaluatedAt time.Time) error {
	if strings.TrimSpace(value.SelectionMethod) == "" ||
		value.SelectionMethod != strings.TrimSpace(value.SelectionMethod) {
		return fmt.Errorf("%w: 气象上下文缺少空间选择方法", domain.ErrInvalidInput)
	}
	location := value.Snapshot.Location
	if !finite(location.Longitude) || !finite(location.Latitude) || location.Validate() != nil {
		return fmt.Errorf("%w: 气象上下文坐标无效", domain.ErrInvalidInput)
	}
	if err := value.Snapshot.Source.Validate(); err != nil {
		return err
	}
	if value.Snapshot.Source.SHA256 != "" && !validSHA256(value.Snapshot.Source.SHA256) {
		return fmt.Errorf("%w: 气象上下文来源校验和无效", domain.ErrInvalidInput)
	}
	source := value.Snapshot.Source
	if source.ValidFrom.IsZero() || source.ValidTo.IsZero() || source.ValidTo.Before(source.ValidFrom) {
		return fmt.Errorf("%w: 气象上下文有效期无效", domain.ErrInvalidInput)
	}
	if !evaluatedAt.Before(source.ValidFrom) && !evaluatedAt.After(source.ValidTo) &&
		source.Stale != hasQualityFlag(source.QualityFlags, fallbackQualityFlag) {
		return fmt.Errorf("%w: 气象上下文回退标记不一致", domain.ErrInvalidInput)
	}
	if len(value.Snapshot.Hourly) == 0 {
		return fmt.Errorf("%w: 气象上下文没有逐小时数据", domain.ErrInsufficientData)
	}
	return validateWeatherTimeline(value.Snapshot)
}

func validateWeatherTimeline(value hazard.WeatherSnapshot) error {
	var previous time.Time
	for index, point := range value.Hourly {
		if err := validateWeatherPoint(point, previous); err != nil {
			return fmt.Errorf("逐小时数据 %d: %w", index, err)
		}
		if point.Time.Before(value.Source.ValidFrom) || point.Time.Add(time.Hour).After(value.Source.ValidTo) {
			return fmt.Errorf("%w: 气象时间超出来源有效期", domain.ErrInvalidInput)
		}
		previous = point.Time
	}
	return nil
}

func validateWeatherPoint(point hazard.WeatherPoint, previous time.Time) error {
	if point.Time.IsZero() || !isUTC(point.Time) ||
		(!previous.IsZero() && !point.Time.Equal(previous.Add(time.Hour))) {
		return fmt.Errorf("%w: 气象时间必须是连续 UTC 小时", domain.ErrInvalidInput)
	}
	for _, value := range []float64{point.PrecipitationMM, point.RainMM, point.ShowersMM} {
		if !finite(value) || value < 0 {
			return fmt.Errorf("%w: 气象降水值无效", domain.ErrInvalidInput)
		}
	}
	if len(point.SoilMoistureByLayer) != 5 {
		return fmt.Errorf("%w: 气象土壤湿度必须包含五层", domain.ErrInvalidInput)
	}
	for _, value := range point.SoilMoistureByLayer {
		if !finite(value) || value < 0 || value > 1 {
			return fmt.Errorf("%w: 气象土壤湿度超出零到一范围", domain.ErrInvalidInput)
		}
	}
	return nil
}

func weatherLocationKey(value hazard.WeatherSnapshot) string {
	return fmt.Sprintf("%.6f,%.6f", value.Location.Longitude, value.Location.Latitude)
}
