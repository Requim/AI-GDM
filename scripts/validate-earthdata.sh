#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_PROXY="${GOPROXY:-https://goproxy.cn,direct}"
GDAL_IMAGE="${GDAL_IMAGE:-ghcr.io/osgeo/gdal@sha256:44fee7d4f9be0966851d7b14a0a387216897d8347f9e0ebc4e812f7217bc39d6}"
OUTPUT=$(mktemp -d)
trap 'rm -rf "$OUTPUT"' EXIT

sh "$ROOT/scripts/run-validation-container.sh" -v "$ROOT:/src:ro" -v "$OUTPUT:/out" -w /src \
  -e "GOPROXY=$GO_PROXY" golang:1.26.7-bookworm \
  sh -c 'go test -c -o /out/gdal.test ./internal/adapters/raster/gdal'

sh "$ROOT/scripts/run-validation-container.sh" -v "$OUTPUT:/test" "$GDAL_IMAGE" \
  sh -c 'EARTHDATA_LIVE_TEST=1 EARTHDATA_CHINA_LIVE_TEST=1 \
    /test/gdal.test -test.run "TestLiveEarthdata(Pipeline|ChinaAcquisition)" -test.v'
