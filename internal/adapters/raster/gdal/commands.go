package gdal

import "strconv"

type pipelinePaths struct {
	clipped        string
	classified     string
	boundary       string
	rawPolygons    string
	polygons       string
	levelPolygons  [3]string
	levelRaster    [3]string
	statistics     [3]string
	boundaryErrors string
	geometryErrors string
}

var statisticsLevels = [3]int{1, 2, 3}

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
		"--pixels", "fractional",
		"--stat", "min", "--stat", "mean", "--stat", "max",
		"--include-field", "level", "--include-geom", "--output-format", "GeoJSON", "--overwrite",
	}
}

func levelFilterArguments(input, output string, level int) []string {
	return []string{
		"vector", "filter", "--input-format", "GeoJSON", "--where", "level = " + strconv.Itoa(level),
		"--output-format", "GeoJSON", "--overwrite", input, output,
	}
}

func levelProbabilityArguments(input, classified, output string, level int) []string {
	value := strconv.Itoa(level)
	return []string{
		"raster", "calc", "--input-format", "GTiff", "--input", "X=" + input,
		"--input", "C=" + classified, "--calc", "((C==" + value + ")*X)+((C!=" + value + ")*-9999)",
		"--output-data-type", "Float32", "--nodata", "-9999", "--output-format", "GTiff",
		"--creation-option", "TILED=YES", "--creation-option", "COMPRESS=ZSTD",
		"--creation-option", "PREDICTOR=3", "--overwrite", "--output", output,
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
