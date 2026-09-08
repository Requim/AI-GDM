package gdal

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/Requim/AI-GDM/internal/domain"
)

//go:embed normalize.py
var normalizeScript string

const PortalTransformVersion = "lhasa-nccs-1-native-nodata-china-adm0"

func (p *Processor) probabilitySemantics() string {
	if p.config.NormalizeNCCS {
		return "原生 30 弧秒滑坡发生概率模型估计，缺失值不表示零风险；等级按严格大于阈值派生，边界均值按像元相交比例加权"
	}
	return "固定 30 弧秒目标网格最近邻导出的日尺度滑坡发生概率模型估计；等级按严格大于阈值派生，边界均值按像元相交比例加权，最小和最大值取相交的同等级像元"
}

func (p *Processor) rasterCoverageLimitation() string {
	if p.config.NormalizeNCCS {
		return "完整全球栅格在本地裁剪为 WGS84 中国外接矩形，再按版本化 CHN ADM0 行政边界精确裁剪"
	}
	return "数据先按 WGS84 中国外接矩形下载，再按版本化 CHN ADM0 行政边界精确裁剪"
}

func normalizeNCCS(ctx context.Context, input, output string, bbox [4]float64) error {
	bounds, err := json.Marshal(bbox)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "python3", "-c", normalizeScript, input, output, string(bounds))
	result, err := command.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: 规范化 NCCS 栅格: %w: %.2048s", domain.ErrProviderUnavailable, err, result)
	}
	return nil
}

func (p *Processor) prepareClippedRaster(ctx context.Context, input string, paths pipelinePaths) error {
	if p.config.NormalizeNCCS {
		return normalizeNCCS(ctx, input, paths.clipped, p.config.BBox)
	}
	info, err := p.run(ctx, paths.clipped, infoArguments(input))
	if err != nil {
		return fmt.Errorf("检查原始 LHASA 栅格: %w", err)
	}
	if err = validateSourceRasterInfo(info, p.config.BBox); err != nil {
		return err
	}
	_, err = p.run(ctx, paths.clipped, clipArguments(input, paths.clipped, p.config.BBox))
	if err != nil {
		return fmt.Errorf("裁剪中国外包范围栅格: %w", err)
	}
	return nil
}
