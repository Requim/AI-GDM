package gdal

import "strconv"

type pipelinePaths struct {
	clipped        string
	classified     string
	boundary       string
	rawPolygons    string
	polygons       string
	statistics     string
	boundaryErrors string
	geometryErrors string
}

func vectorClipArguments(input, boundary, output string) []string {
	return []string{
		"vector", "clip", "--input-format", "GeoJSON", "--like", boundary,
		"--output-format", "GeoJSON", "--overwrite", input, output,
	}
}

func clipArguments(input, output string, bbox [4]float64) []string {
	return []string{
		"raster", "clip", "--input-format", "GTiff", "--bbox", bboxValue(bbox), "--bbox-crs", "EPSG:4326",
		"--output-format", "GTiff", "--creation-option", "TILED=YES", "--creation-option", "COMPRESS=ZSTD",
		"--creation-option", "PREDICTOR=3", "--creation-option", "BIGTIFF=IF_SAFER", "--overwrite", input, output,
	}
}

func infoArguments(input string) []string {
	return []string{"raster", "info", "--input-format", "GTiff", "--output-format", "json", "--min-max", input}
}

func classifyArguments(input, output string) []string {
	return []string{
		"raster", "calc", "--input-format", "GTiff", "--input", "X=" + input, "--calc",
		"(X>0.1)*(1+(X>0.5)+(X>0.9))", "--output-data-type", "UInt8",
		"--nodata", "0", "--propagate-nodata", "--output-format", "GTiff",
		"--creation-option", "TILED=YES", "--creation-option", "COMPRESS=DEFLATE",
		"--overwrite", "--output", output,
	}
}

func polygonizeArguments(input, output string) []string {
	return []string{
		"raster", "polygonize", "--input-format", "GTiff", "--input", input, "--output", output,
		"--output-format", "GeoJSON", "--attribute-name", "level", "--overwrite",
	}
}

func statisticsArguments(raster, polygons, output string) []string {
	return []string{
		"raster", "zonal-stats", raster, output, "--zones", polygons,
		"--stat", "min", "--stat", "mean", "--stat", "max",
		"--include-field", "level", "--include-geom", "--output-format", "GeoJSON", "--overwrite",
	}
}

func checkGeometryArguments(input, output string) []string {
	return []string{
		"vector", "check-geometry", "--input", input, "--output", output,
		"--output-format", "GPKG", "--output-layer", "geometry_errors", "--overwrite",
	}
}

func vectorInfoArguments(input string) []string {
	return []string{"vector", "info", "--output-format", "json", input}
}

func bboxValue(value [4]float64) string {
	return strconv.FormatFloat(value[0], 'f', -1, 64) + "," +
		strconv.FormatFloat(value[1], 'f', -1, 64) + "," +
		strconv.FormatFloat(value[2], 'f', -1, 64) + "," +
		strconv.FormatFloat(value[3], 'f', -1, 64)
}
