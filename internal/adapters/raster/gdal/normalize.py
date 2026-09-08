import json
import math
import sys

import numpy as np
from osgeo import gdal, osr

gdal.UseExceptions()
gdal.SetCacheMax(64 * 1024 * 1024)
NODATA = -9999.0


def validate(values):
    if np.isinf(values).any():
        raise ValueError("infinite probability")
    valid = np.isfinite(values) & (values != NODATA)
    selected = values[valid]
    if selected.size and (selected.min() < 0 or selected.max() > 1):
        raise ValueError("probability outside [0,1]")
    return int(selected.size)


def grid(source, bounds):
    if source.RasterXSize > 43200 or source.RasterYSize > 21600:
        raise ValueError("raster dimensions exceed resource budget")
    if source.GetDriver().ShortName != "GTiff" or source.GetRasterBand(1).DataType not in (gdal.GDT_Float32, gdal.GDT_Float64):
        raise ValueError("unexpected driver or probability type")
    expected = osr.SpatialReference()
    expected.ImportFromEPSG(4326)
    reference = source.GetSpatialRef()
    if reference is None:
        raise ValueError("missing CRS")
    reference.SetAxisMappingStrategy(osr.OAMS_TRADITIONAL_GIS_ORDER)
    expected.SetAxisMappingStrategy(osr.OAMS_TRADITIONAL_GIS_ORDER)
    gt = source.GetGeoTransform()
    if not reference.IsSame(expected) or source.RasterCount != 1:
        raise ValueError("unexpected CRS or bands")
    if gt[2] or gt[4] or abs(gt[1] - 1 / 120) > 1e-8 or abs(gt[5] - 1 / 120) > 1e-8:
        raise ValueError("expected native south-up 30 arc-second grid")
    if source.GetRasterBand(1).GetNoDataValue() != NODATA:
        raise ValueError("unexpected NoData declaration")
    left = math.floor((bounds[0] - gt[0]) / gt[1] + 1e-8)
    right = math.ceil((bounds[2] - gt[0]) / gt[1] - 1e-8)
    bottom = math.floor((bounds[1] - gt[3]) / gt[5] + 1e-8)
    top = math.ceil((bounds[3] - gt[3]) / gt[5] - 1e-8)
    if not (0 <= left < right <= source.RasterXSize and 0 <= bottom < top <= source.RasterYSize):
        raise ValueError("regional bounds outside source")
    return left, bottom, right - left, top - bottom, (gt[0] + left * gt[1], gt[1], 0, gt[3] + top * gt[5], 0, -gt[5])


# 完整读取全球基础分辨率后裁剪；仅翻转行序和规范化缺失值，不插值概率。
def normalize(source, output, bounds):
    left, bottom, width, height, transform = grid(source, bounds)
    for row in range(0, source.RasterYSize, 64):
        validate(source.ReadAsArray(0, row, source.RasterXSize, min(64, source.RasterYSize - row)))
    target = gdal.GetDriverByName("GTiff").Create(output, width, height, 1, gdal.GDT_Float64,
        ["COMPRESS=ZSTD", "TILED=YES", "BIGTIFF=IF_SAFER", "NUM_THREADS=1"])
    target.SetGeoTransform(transform)
    target.SetProjection(source.GetProjection())
    target.GetRasterBand(1).SetNoDataValue(NODATA)
    valid = 0
    for row in range(0, height, 64):
        rows = min(64, height - row)
        values = source.ReadAsArray(left, bottom + height - row - rows, width, rows)
        valid += validate(values)
        values[np.isnan(values)] = NODATA
        target.GetRasterBand(1).WriteArray(values[::-1], 0, row)
    target.FlushCache()
    target = None
    if valid == 0:
        raise ValueError("no valid regional probability")
    verify(source, output, left, bottom, width, height)


def verify(source, output, left, bottom, width, height):
    result = gdal.Open(output)
    for row in range(0, height, 64):
        rows = min(64, height - row)
        expected = source.ReadAsArray(left, bottom + height - row - rows, width, rows)
        expected[np.isnan(expected)] = NODATA
        if not np.array_equal(result.ReadAsArray(0, row, width, rows), expected[::-1]):
            raise ValueError("pixel preservation failed")


if __name__ == "__main__":
    normalize(gdal.Open(sys.argv[1]), sys.argv[2], json.loads(sys.argv[3]))
