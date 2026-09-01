#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
AI_GDM_SECURITY_GATE_LIBRARY=source
export AI_GDM_SECURITY_GATE_LIBRARY
. "$ROOT/scripts/security-gate.lib.sh"

GO_IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm@sha256:6ef6e30f0ea5c384f6d111cf856e024e3086bbdcb1779da3f3b3fbba0aea53d2}
GDAL_SOURCE=${PACKAGE_GDAL_SOURCE:-ghcr.io/osgeo/gdal@sha256:44fee7d4f9be0966851d7b14a0a387216897d8347f9e0ebc4e812f7217bc39d6}
POSTGIS_SOURCE=${PACKAGE_POSTGIS_SOURCE:-postgis/postgis:17-3.5@sha256:83e9999dc3ad8390c210e76130c3a16365ef4f957bb55200d22b7937cfbcb321}
REDIS_SOURCE=${PACKAGE_REDIS_SOURCE:-redis:7.4.10-bookworm@sha256:e9b2e45ecd47fbb69b877cf8d045d5cccaaaed52524b6e098b4abe8212994f73}
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')
VERSION=${PACKAGE_VERSION:-v0.1.1}
PULL_IMAGES=${PACKAGE_PULL_IMAGES:-1}
REQUIRE_SOURCE_COMMIT=${PACKAGE_REQUIRE_SOURCE_COMMIT:-0}
MIN_FREE_KB=${PACKAGE_MIN_FREE_KB:-4194304}
OUTPUT_ROOT=${PACKAGE_OUTPUT_DIR:-$ROOT/dist}
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-package.XXXXXX")
SNAPSHOT_DIR="$WORK_DIR/source"
BUILD_DIR="$WORK_DIR/build"
IMAGE_IIDFILE="$WORK_DIR/app.iid"
ACTIVE_CHILD_PID=
APP_TAG=
POSTGIS_TAG=
REDIS_TAG=
RUN_ID=
BUILD_CONTAINER=
PACKAGE_DIR=
PACKAGE_ARCHIVE=
COMPLETED=0
SOURCE_COMMIT=
IMAGE_REVISION=
RELEASE_NOTES_RELATIVE=
DOCKER_OWNERSHIP_STARTED=0

stop_active_child() {
  [ -n "$ACTIVE_CHILD_PID" ] || return 0
  kill -TERM "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  kill -KILL "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  wait "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  ACTIVE_CHILD_PID=
}

run_active_command() {
  "$@" &
  ACTIVE_CHILD_PID=$!
  status=0
  wait "$ACTIVE_CHILD_PID" || status=$?
  ACTIVE_CHILD_PID=
  return "$status"
}

remove_release_tag() {
  reference=$1
  expected=$2
  [ -n "$reference" ] && [ -n "$expected" ] || return 0
  actual=$(docker image inspect -f '{{.Id}}' "$reference" 2>/dev/null || true)
  [ "$actual" = "$expected" ] || return 0
  docker image rm "$reference" >/dev/null 2>&1 || true
}

remove_owned_container() {
  [ -n "$BUILD_CONTAINER" ] && [ -n "$RUN_ID" ] || return 0
  owner=$(docker inspect -f '{{ index .Config.Labels "ai.gdm.package.run" }}' "$BUILD_CONTAINER" 2>/dev/null || true)
  [ "$owner" = "$RUN_ID" ] || return 0
  docker rm -f "$BUILD_CONTAINER" >/dev/null 2>&1 || true
}

recover_app_image_id() {
  [ -z "${APP_IMAGE_ID:-}" ] || return 0
  if [ -s "$IMAGE_IIDFILE" ]; then
    APP_IMAGE_ID=$(sed -n '1p' "$IMAGE_IIDFILE")
    return
  fi
  [ -n "$APP_TAG" ] && [ -n "$RUN_ID" ] || return 0
  owner=$(docker image inspect -f '{{ index .Config.Labels "ai.gdm.package.run" }}' "$APP_TAG" 2>/dev/null || true)
  [ "$owner" = "$RUN_ID" ] || return 0
  APP_IMAGE_ID=$(docker image inspect -f '{{.Id}}' "$APP_TAG")
}

cleanup_release_tags() {
  recover_app_image_id
  remove_release_tag "$APP_TAG" "${APP_IMAGE_ID:-}"
  remove_release_tag "$POSTGIS_TAG" "${POSTGIS_IMAGE_ID:-}"
  remove_release_tag "$REDIS_TAG" "${REDIS_IMAGE_ID:-}"
}

cleanup_partial_output() {
  [ "$COMPLETED" = 1 ] && return 0
  case "$PACKAGE_DIR" in "$OUTPUT_ROOT"/ai-gdm-*) rm -rf -- "$PACKAGE_DIR" ;; esac
  case "$PACKAGE_ARCHIVE" in "$OUTPUT_ROOT"/ai-gdm-*.tar.gz)
    rm -f -- "$PACKAGE_ARCHIVE" "$PACKAGE_ARCHIVE.sha256"
    ;;
  esac
}

cleanup() {
  stop_active_child
  if [ "$DOCKER_OWNERSHIP_STARTED" = 1 ]; then
    remove_owned_container
    cleanup_release_tags
  fi
  cleanup_partial_output
  chmod -R u+w "$WORK_DIR" 2>/dev/null || true
  rm -rf "$WORK_DIR"
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

validate_version() {
  case "$VERSION" in v*) version_numbers=${VERSION#v} ;; *) security_fail 'PACKAGE_VERSION 必须是 vMAJOR.MINOR.PATCH' ;; esac
  case "$version_numbers" in ''|*[!0-9.]*) security_fail 'PACKAGE_VERSION 必须是 vMAJOR.MINOR.PATCH' ;; esac
  old_ifs=$IFS
  IFS=.
  set -- $version_numbers
  IFS=$old_ifs
  [ "$#" -eq 3 ] || security_fail 'PACKAGE_VERSION 必须是 vMAJOR.MINOR.PATCH'
  for number in "$@"; do
    case "$number" in ''|*[!0-9]*) security_fail 'PACKAGE_VERSION 必须是 vMAJOR.MINOR.PATCH' ;; esac
    case "$number" in 0|[1-9]*) ;; *) security_fail 'PACKAGE_VERSION 不允许前导零' ;; esac
  done
  RELEASE_NOTES_RELATIVE="docs/release-$VERSION.md"
  [ -f "$ROOT/$RELEASE_NOTES_RELATIVE" ] || security_fail "缺少当前版本发布说明: $RELEASE_NOTES_RELATIVE"
}

validate_created_at() {
  normalized=$(date -u -d "$CREATED_AT" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || true)
  [ "$normalized" = "$CREATED_AT" ] || security_fail 'PACKAGE_CREATED_AT 必须是严格 UTC 时间'
}

validate_source_commit() {
  requested=${PACKAGE_SOURCE_COMMIT:-auto}
  automatic=0
  if [ "$requested" = auto ]; then
    automatic=1
    if detected=$(git -c "safe.directory=$ROOT" -C "$ROOT" rev-parse --verify HEAD 2>/dev/null); then
      requested=$detected
    else
      requested=unknown
    fi
  fi
  SOURCE_COMMIT=$requested
  if [ "$SOURCE_COMMIT" != unknown ]; then
    security_validate_sha "$SOURCE_COMMIT" 40 'PACKAGE_SOURCE_COMMIT'
    commit_tree=$(git -c "safe.directory=$ROOT" -C "$ROOT" rev-parse "$SOURCE_COMMIT^{tree}" 2>/dev/null || true)
    if [ "$commit_tree" != "$SECURITY_TREE_SHA" ] && [ "$automatic" = 1 ]; then
      SOURCE_COMMIT=unknown
    elif [ "$commit_tree" != "$SECURITY_TREE_SHA" ]; then
      security_fail 'PACKAGE_SOURCE_COMMIT 与发布 tree 不一致'
    fi
  fi
  case "$REQUIRE_SOURCE_COMMIT" in 0|1) ;; *) security_fail 'PACKAGE_REQUIRE_SOURCE_COMMIT 必须是 0 或 1' ;; esac
  if [ "$SOURCE_COMMIT" = unknown ]; then
    [ "$REQUIRE_SOURCE_COMMIT" = 0 ] || security_fail '正式发布必须绑定源码提交'
  fi
  IMAGE_REVISION=$SECURITY_TREE_SHA
  [ "$SOURCE_COMMIT" = unknown ] || IMAGE_REVISION=$SOURCE_COMMIT
}

validate_image_source() {
  value=$1
  label=$2
  case "$value" in ''|*[!A-Za-z0-9._/@:-]*) security_fail "$label 包含非法字符" ;; esac
  security_validate_image_reference "$value" "$label"
}

validate_inputs() {
  validate_version
  validate_created_at
  case "$PULL_IMAGES" in 0|1) ;; *) security_fail 'PACKAGE_PULL_IMAGES 必须是 0 或 1' ;; esac
  case "$REQUIRE_SOURCE_COMMIT" in 0|1) ;; *) security_fail 'PACKAGE_REQUIRE_SOURCE_COMMIT 必须是 0 或 1' ;; esac
  case "$MIN_FREE_KB" in ''|*[!0-9]*) security_fail 'PACKAGE_MIN_FREE_KB 必须是正整数' ;; esac
  [ "$MIN_FREE_KB" -gt 0 ] || security_fail 'PACKAGE_MIN_FREE_KB 必须是正整数'
  validate_image_source "$GO_IMAGE" 'GO_VALIDATION_IMAGE'
  validate_image_source "$GDAL_SOURCE" 'PACKAGE_GDAL_SOURCE'
  validate_image_source "$POSTGIS_SOURCE" 'PACKAGE_POSTGIS_SOURCE'
  validate_image_source "$REDIS_SOURCE" 'PACKAGE_REDIS_SOURCE'
}

resolve_output_root() {
  case "$OUTPUT_ROOT" in /*) candidate=$OUTPUT_ROOT ;; *) candidate="$ROOT/$OUTPUT_ROOT" ;; esac
  if [ "$SECURITY_SOURCE_MODE" = archive ]; then
    case "$candidate" in "$ROOT"|"$ROOT"/*) security_fail '归档模式的发布目录必须位于源码目录之外' ;; esac
  fi
  mkdir -p "$candidate"
  OUTPUT_ROOT=$(CDPATH= cd -- "$candidate" && pwd -P)
  if [ "$SECURITY_SOURCE_MODE" = archive ]; then
    case "$OUTPUT_ROOT" in "$ROOT"|"$ROOT"/*) security_fail '归档模式的发布目录必须位于源码目录之外' ;; esac
  fi
}

prepare_output() {
  resolve_output_root
  PACKAGE_NAME="ai-gdm-$VERSION-linux-amd64"
  PACKAGE_DIR="$OUTPUT_ROOT/$PACKAGE_NAME"
  PACKAGE_ARCHIVE="$OUTPUT_ROOT/$PACKAGE_NAME.tar.gz"
  [ ! -e "$PACKAGE_DIR" ] && [ ! -e "$PACKAGE_ARCHIVE" ] && [ ! -e "$PACKAGE_ARCHIVE.sha256" ] || \
    security_fail '目标发布目录或归档已存在'
  mkdir -p "$PACKAGE_DIR/bin" "$PACKAGE_DIR/images" "$PACKAGE_DIR/deploy" "$PACKAGE_DIR/docs" "$BUILD_DIR"
  available_kb=$(df -Pk "$OUTPUT_ROOT" | awk 'NR == 2 {print $4}')
  case "$available_kb" in ''|*[!0-9]*) security_fail '无法读取发布目录可用空间' ;; esac
  [ "$available_kb" -ge "$MIN_FREE_KB" ] || security_fail '发布目录可用空间不足'
}

ensure_release_tags_available() {
  for reference in "$APP_TAG" "$POSTGIS_TAG" "$REDIS_TAG"; do
    docker image inspect "$reference" >/dev/null 2>&1 && security_fail "发布镜像标签已存在: $reference"
  done
  return 0
}

pull_runtime_images() {
  [ "$PULL_IMAGES" = 1 ] || return 0
  run_active_command docker pull "$GO_IMAGE"
  run_active_command docker pull "$GDAL_SOURCE"
  run_active_command docker pull "$POSTGIS_SOURCE"
  run_active_command docker pull "$REDIS_SOURCE"
}

build_binaries() {
  run_active_command docker run --rm --name "$BUILD_CONTAINER" \
    --label "ai.gdm.package.run=$RUN_ID" --label "ai.gdm.package.tree=$SECURITY_TREE_SHA" \
    -e GOPROXY="$GO_PROXY" -e VERSION="$VERSION" \
    -v "$SNAPSHOT_DIR:/src:ro" -v "$BUILD_DIR:/out" -w /src "$GO_IMAGE" sh -c '
      set -eu
      go mod verify
      CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -trimpath -buildvcs=false \
        -ldflags="-s -w -X main.version=$VERSION" -o /out/ai-gdm-server-linux-amd64 ./cmd/server
      CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod=readonly -trimpath -buildvcs=false \
        -ldflags="-s -w -X main.version=$VERSION" -o /out/ai-gdm-server-windows-amd64.exe ./cmd/server
      CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -trimpath -buildvcs=false \
        -ldflags="-s -w" -o /out/ai-gdm-healthcheck-linux-amd64 ./cmd/healthcheck
    '
}

build_release_images() {
  run_active_command docker build --pull=false --provenance=false --iidfile "$IMAGE_IIDFILE" \
    --build-arg "GO_IMAGE=$GO_IMAGE" --build-arg "GDAL_IMAGE=$GDAL_SOURCE" \
    --build-arg "GOPROXY=$GO_PROXY" --build-arg "VERSION=$VERSION" \
    --label "ai.gdm.package.run=$RUN_ID" \
    --build-arg "VCS_REF=$IMAGE_REVISION" --build-arg "BUILD_DATE=$CREATED_AT" \
    --label "ai.gdm.package.tree=$SECURITY_TREE_SHA" --label "ai.gdm.package.source=$SECURITY_SOURCE_SHA256" \
    -t "$APP_TAG" "$SNAPSHOT_DIR"
  APP_IMAGE_ID=$(docker image inspect -f '{{.Id}}' "$APP_TAG")
  POSTGIS_IMAGE_ID=$(docker image inspect -f '{{.Id}}' "$POSTGIS_SOURCE")
  REDIS_IMAGE_ID=$(docker image inspect -f '{{.Id}}' "$REDIS_SOURCE")
  docker tag "$POSTGIS_SOURCE" "$POSTGIS_TAG"
  docker tag "$REDIS_SOURCE" "$REDIS_TAG"
  APP_IMAGE_PLATFORM=$(docker image inspect -f '{{.Os}}/{{.Architecture}}' "$APP_TAG")
  POSTGIS_IMAGE_PLATFORM=$(docker image inspect -f '{{.Os}}/{{.Architecture}}' "$POSTGIS_TAG")
  REDIS_IMAGE_PLATFORM=$(docker image inspect -f '{{.Os}}/{{.Architecture}}' "$REDIS_TAG")
  [ "$APP_IMAGE_PLATFORM" = linux/amd64 ] || security_fail '应用镜像平台不是 linux/amd64'
  [ "$POSTGIS_IMAGE_PLATFORM" = linux/amd64 ] || security_fail 'PostGIS 镜像平台不是 linux/amd64'
  [ "$REDIS_IMAGE_PLATFORM" = linux/amd64 ] || security_fail 'Redis 镜像平台不是 linux/amd64'
  APP_IMAGE_SIZE=$(docker image inspect -f '{{.Size}}' "$APP_TAG")
  POSTGIS_IMAGE_SIZE=$(docker image inspect -f '{{.Size}}' "$POSTGIS_TAG")
  REDIS_IMAGE_SIZE=$(docker image inspect -f '{{.Size}}' "$REDIS_TAG")
}

copy_release_files() {
  cp "$SNAPSHOT_DIR/README.md" "$PACKAGE_DIR/README.md"
  cp "$BUILD_DIR/ai-gdm-server-linux-amd64" "$PACKAGE_DIR/bin/"
  cp "$BUILD_DIR/ai-gdm-server-windows-amd64.exe" "$PACKAGE_DIR/bin/"
  cp "$BUILD_DIR/ai-gdm-healthcheck-linux-amd64" "$PACKAGE_DIR/bin/"
  cp "$SNAPSHOT_DIR/compose.yaml" "$PACKAGE_DIR/compose.yaml"
  cp "$SNAPSHOT_DIR/deploy/compose.offline.yaml" "$PACKAGE_DIR/deploy/compose.offline.yaml"
  cp "$SNAPSHOT_DIR/deploy/deploy.sh" "$PACKAGE_DIR/deploy/deploy.sh"
  cp "$SNAPSHOT_DIR/deploy/deploy.ps1" "$PACKAGE_DIR/deploy/deploy.ps1"
  cp "$SNAPSHOT_DIR/deploy/runtime.env.example" "$PACKAGE_DIR/deploy/runtime.env.example"
  cp "$SNAPSHOT_DIR/docs/deployment-v1.md" "$PACKAGE_DIR/docs/deployment-v1.md"
  cp "$SNAPSHOT_DIR/docs/data-sources-v1.md" "$PACKAGE_DIR/docs/data-sources-v1.md"
  cp "$SNAPSHOT_DIR/docs/limitations-v1.md" "$PACKAGE_DIR/docs/limitations-v1.md"
  cp "$SNAPSHOT_DIR/docs/model-cards-v1.md" "$PACKAGE_DIR/docs/model-cards-v1.md"
  cp "$SNAPSHOT_DIR/docs/package-v1.md" "$PACKAGE_DIR/docs/package-v1.md"
  cp "$SNAPSHOT_DIR/$RELEASE_NOTES_RELATIVE" "$PACKAGE_DIR/$RELEASE_NOTES_RELATIVE"
  chmod 0755 "$PACKAGE_DIR/bin/ai-gdm-server-linux-amd64" "$PACKAGE_DIR/bin/ai-gdm-healthcheck-linux-amd64" \
    "$PACKAGE_DIR/deploy/deploy.sh"
}

write_image_environment() {
  cat >"$PACKAGE_DIR/deploy/release-images.env" <<EOF
AI_GDM_IMAGE=$APP_TAG
AI_GDM_POSTGIS_IMAGE=$POSTGIS_TAG
AI_GDM_REDIS_IMAGE=$REDIS_TAG
EOF
  cat >"$PACKAGE_DIR/images/IMAGE-SOURCES.txt" <<EOF
$POSTGIS_SOURCE
$REDIS_SOURCE
$GO_IMAGE
$GDAL_SOURCE
应用镜像由 sourceTree=$SECURITY_TREE_SHA 构建；Go 与 GDAL 只作为构建来源，不进入离线运行镜像清单。
EOF
}

artifact_sha() {
  security_hash_file "$PACKAGE_DIR/$1"
}

artifact_size() {
  wc -c <"$PACKAGE_DIR/$1" | tr -d '[:space:]'
}

write_manifest() {
  cat >"$PACKAGE_DIR/manifest.json" <<EOF
{
  "schemaVersion": 1,
  "version": "$VERSION",
  "createdAt": "$CREATED_AT",
  "sourceCommit": "$SOURCE_COMMIT",
  "sourceTree": "$SECURITY_TREE_SHA",
  "sourceSha256": "$SECURITY_SOURCE_SHA256",
  "platform": "linux/amd64",
  "images": [
    {"reference": "$APP_TAG", "id": "$APP_IMAGE_ID", "platform": "$APP_IMAGE_PLATFORM", "sizeBytes": $APP_IMAGE_SIZE},
    {"reference": "$POSTGIS_TAG", "source": "$POSTGIS_SOURCE", "id": "$POSTGIS_IMAGE_ID", "platform": "$POSTGIS_IMAGE_PLATFORM", "sizeBytes": $POSTGIS_IMAGE_SIZE},
    {"reference": "$REDIS_TAG", "source": "$REDIS_SOURCE", "id": "$REDIS_IMAGE_ID", "platform": "$REDIS_IMAGE_PLATFORM", "sizeBytes": $REDIS_IMAGE_SIZE}
  ],
  "artifacts": [
    {"path": "README.md", "sha256": "$(artifact_sha README.md)", "sizeBytes": $(artifact_size README.md)},
    {"path": "bin/ai-gdm-server-linux-amd64", "sha256": "$(artifact_sha bin/ai-gdm-server-linux-amd64)", "sizeBytes": $(artifact_size bin/ai-gdm-server-linux-amd64)},
    {"path": "bin/ai-gdm-server-windows-amd64.exe", "sha256": "$(artifact_sha bin/ai-gdm-server-windows-amd64.exe)", "sizeBytes": $(artifact_size bin/ai-gdm-server-windows-amd64.exe)},
    {"path": "bin/ai-gdm-healthcheck-linux-amd64", "sha256": "$(artifact_sha bin/ai-gdm-healthcheck-linux-amd64)", "sizeBytes": $(artifact_size bin/ai-gdm-healthcheck-linux-amd64)},
    {"path": "images/ai-gdm-images-linux-amd64.tar", "sha256": "$(artifact_sha images/ai-gdm-images-linux-amd64.tar)", "sizeBytes": $(artifact_size images/ai-gdm-images-linux-amd64.tar)},
    {"path": "images/IMAGE-SOURCES.txt", "sha256": "$(artifact_sha images/IMAGE-SOURCES.txt)", "sizeBytes": $(artifact_size images/IMAGE-SOURCES.txt)},
    {"path": "deploy/compose.offline.yaml", "sha256": "$(artifact_sha deploy/compose.offline.yaml)", "sizeBytes": $(artifact_size deploy/compose.offline.yaml)},
    {"path": "deploy/deploy.sh", "sha256": "$(artifact_sha deploy/deploy.sh)", "sizeBytes": $(artifact_size deploy/deploy.sh)},
    {"path": "deploy/deploy.ps1", "sha256": "$(artifact_sha deploy/deploy.ps1)", "sizeBytes": $(artifact_size deploy/deploy.ps1)},
    {"path": "deploy/release-images.env", "sha256": "$(artifact_sha deploy/release-images.env)", "sizeBytes": $(artifact_size deploy/release-images.env)},
    {"path": "deploy/runtime.env.example", "sha256": "$(artifact_sha deploy/runtime.env.example)", "sizeBytes": $(artifact_size deploy/runtime.env.example)},
    {"path": "docs/data-sources-v1.md", "sha256": "$(artifact_sha docs/data-sources-v1.md)", "sizeBytes": $(artifact_size docs/data-sources-v1.md)},
    {"path": "docs/deployment-v1.md", "sha256": "$(artifact_sha docs/deployment-v1.md)", "sizeBytes": $(artifact_size docs/deployment-v1.md)},
    {"path": "docs/limitations-v1.md", "sha256": "$(artifact_sha docs/limitations-v1.md)", "sizeBytes": $(artifact_size docs/limitations-v1.md)},
    {"path": "docs/model-cards-v1.md", "sha256": "$(artifact_sha docs/model-cards-v1.md)", "sizeBytes": $(artifact_size docs/model-cards-v1.md)},
    {"path": "docs/package-v1.md", "sha256": "$(artifact_sha docs/package-v1.md)", "sizeBytes": $(artifact_size docs/package-v1.md)},
    {"path": "$RELEASE_NOTES_RELATIVE", "sha256": "$(artifact_sha "$RELEASE_NOTES_RELATIVE")", "sizeBytes": $(artifact_size "$RELEASE_NOTES_RELATIVE")},
    {"path": "compose.yaml", "sha256": "$(artifact_sha compose.yaml)", "sizeBytes": $(artifact_size compose.yaml)}
  ]
}
EOF
}

write_checksums() (
  cd "$PACKAGE_DIR"
  find . -type f ! -name SHA256SUMS -print0 | LC_ALL=C sort -z | xargs -0 sha256sum >SHA256SUMS
)

archive_package() (
  cd "$OUTPUT_ROOT"
  tar -czf "$PACKAGE_ARCHIVE" "$PACKAGE_NAME"
  sha256sum "$(basename "$PACKAGE_ARCHIVE")" >"$(basename "$PACKAGE_ARCHIVE").sha256"
)

CREATED_AT=${PACKAGE_CREATED_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
validate_inputs
security_prepare_snapshot "$ROOT" "$SNAPSHOT_DIR"
validate_source_commit
RUN_ID=$(security_random_token)
BUILD_CONTAINER="ai-gdm-package-$RUN_ID-build"
prepare_output
APP_TAG="ai-gdm/server:$VERSION"
POSTGIS_TAG="ai-gdm/postgis:17-3.5-$VERSION"
REDIS_TAG="ai-gdm/redis:7.4.10-$VERSION"
DOCKER_OWNERSHIP_STARTED=1
ensure_release_tags_available
pull_runtime_images
build_binaries
build_release_images
copy_release_files
write_image_environment
run_active_command docker save -o "$PACKAGE_DIR/images/ai-gdm-images-linux-amd64.tar" \
  "$APP_TAG" "$POSTGIS_TAG" "$REDIS_TAG"
write_manifest
write_checksums
archive_package
security_verify_stability "$ROOT" "$SNAPSHOT_DIR"
cleanup_release_tags
for reference in "$APP_TAG" "$POSTGIS_TAG" "$REDIS_TAG"; do
  docker image inspect "$reference" >/dev/null 2>&1 && security_fail "发布临时镜像标签未回收: $reference"
done
COMPLETED=1

printf '%s\n' "PACKAGE_DIR=$PACKAGE_DIR" "PACKAGE_ARCHIVE=$PACKAGE_ARCHIVE" \
  "PACKAGE_TREE=$SECURITY_TREE_SHA" "PACKAGE_SOURCE_SHA256=$SECURITY_SOURCE_SHA256" \
  "PACKAGE_SOURCE_COMMIT=$SOURCE_COMMIT" "PACKAGE_CREATED_AT=$CREATED_AT"
