#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
AI_GDM_SECURITY_GATE_LIBRARY=source
export AI_GDM_SECURITY_GATE_LIBRARY
. "$ROOT/scripts/security-gate.lib.sh"
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-package-validation.XXXXXX")
LOG_FILE="$WORK_DIR/package.log"
OUTPUT_DIR="$WORK_DIR/dist"
RUN_ID=$(security_random_token)
ACTIVE_CHILD_PID=
VALIDATION_CONTAINERS=

stop_active_child() {
  [ -n "$ACTIVE_CHILD_PID" ] || return 0
  kill -TERM "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  attempts=0
  while kill -0 "$ACTIVE_CHILD_PID" >/dev/null 2>&1 && [ "$attempts" -lt 5 ]; do
    sleep 1
    attempts=$((attempts + 1))
  done
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

remove_validation_containers() {
  for name in $VALIDATION_CONTAINERS; do
    owner=$(docker inspect -f '{{ index .Config.Labels "ai.gdm.package.validation.run" }}' "$name" 2>/dev/null || true)
    [ "$owner" = "$RUN_ID" ] || continue
    docker rm -f "$name" >/dev/null 2>&1 || true
  done
}

cleanup() {
  stop_active_child
  remove_validation_containers
  chmod -R u+w "$WORK_DIR" 2>/dev/null || true
  rm -rf "$WORK_DIR"
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

PACKAGE_OUTPUT_DIR="$OUTPUT_DIR"
PACKAGE_VERSION=${PACKAGE_VERSION:-v0.1.0}
PACKAGE_PULL_IMAGES=${PACKAGE_PULL_IMAGES:-0}
export PACKAGE_OUTPUT_DIR PACKAGE_VERSION PACKAGE_PULL_IMAGES
run_active_command sh "$ROOT/scripts/package-release.sh" >"$LOG_FILE" 2>&1 || {
    cat "$LOG_FILE"
    exit 1
  }
cat "$LOG_FILE"

PACKAGE_DIR=$(sed -n 's/^PACKAGE_DIR=//p' "$LOG_FILE")
PACKAGE_ARCHIVE=$(sed -n 's/^PACKAGE_ARCHIVE=//p' "$LOG_FILE")
PACKAGE_TREE=$(sed -n 's/^PACKAGE_TREE=//p' "$LOG_FILE")
PACKAGE_SOURCE_SHA256=$(sed -n 's/^PACKAGE_SOURCE_SHA256=//p' "$LOG_FILE")
PACKAGE_SOURCE_COMMIT=$(sed -n 's/^PACKAGE_SOURCE_COMMIT=//p' "$LOG_FILE")
PACKAGE_CREATED_AT=$(sed -n 's/^PACKAGE_CREATED_AT=//p' "$LOG_FILE")
[ -d "$PACKAGE_DIR" ] && [ -f "$PACKAGE_ARCHIVE" ] || {
  printf '%s\n' 'P10.2 打包输出缺失' >&2
  exit 1
}

cat >"$WORK_DIR/expected-files" <<'EOF'
./SHA256SUMS
./bin/ai-gdm-healthcheck-linux-amd64
./bin/ai-gdm-server-linux-amd64
./bin/ai-gdm-server-windows-amd64.exe
./compose.yaml
./deploy/compose.offline.yaml
./deploy/release-images.env
./deploy/runtime.env.example
./docs/deployment-v1.md
./docs/package-v1.md
./images/IMAGE-SOURCES.txt
./images/ai-gdm-images-linux-amd64.tar
./manifest.json
EOF
(cd "$PACKAGE_DIR" && find . -type f | LC_ALL=C sort) >"$WORK_DIR/actual-files"
diff -u "$WORK_DIR/expected-files" "$WORK_DIR/actual-files"
[ -z "$(find "$PACKAGE_DIR" -type l -print -quit)" ] || {
  printf '%s\n' '发布包不得包含符号链接' >&2
  exit 1
}

(cd "$PACKAGE_DIR" && sha256sum -c SHA256SUMS)
(cd "$PACKAGE_DIR" && awk '{print $2}' SHA256SUMS | LC_ALL=C sort) >"$WORK_DIR/checksum-files"
grep -Fvx './SHA256SUMS' "$WORK_DIR/expected-files" >"$WORK_DIR/expected-checksum-files"
diff -u "$WORK_DIR/expected-checksum-files" "$WORK_DIR/checksum-files"
(cd "$(dirname "$PACKAGE_ARCHIVE")" && sha256sum -c "$(basename "$PACKAGE_ARCHIVE").sha256")
file "$PACKAGE_DIR/bin/ai-gdm-server-linux-amd64" | grep -F 'ELF 64-bit' >/dev/null
file "$PACKAGE_DIR/bin/ai-gdm-server-windows-amd64.exe" | grep -F 'PE32+' >/dev/null
PACKAGE_NAME=$(basename "$PACKAGE_DIR")
tar -tzf "$PACKAGE_ARCHIVE" | sed -n '/\/$/!p' | sed "s#^$PACKAGE_NAME/#./#" | LC_ALL=C sort >"$WORK_DIR/archive-files"
diff -u "$WORK_DIR/expected-files" "$WORK_DIR/archive-files"

GO_IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm@sha256:6ef6e30f0ea5c384f6d111cf856e024e3086bbdcb1779da3f3b3fbba0aea53d2}
for target in linux windows healthcheck releasecheck; do
  name="ai-gdm-package-$RUN_ID-$target"
  VALIDATION_CONTAINERS="$VALIDATION_CONTAINERS $name"
done
run_active_command docker run --rm --name "ai-gdm-package-$RUN_ID-linux" \
  --label "ai.gdm.package.validation.run=$RUN_ID" -v "$PACKAGE_DIR:/package:ro" "$GO_IMAGE" \
  go version -m /package/bin/ai-gdm-server-linux-amd64 >"$WORK_DIR/linux-build-info"
run_active_command docker run --rm --name "ai-gdm-package-$RUN_ID-windows" \
  --label "ai.gdm.package.validation.run=$RUN_ID" -v "$PACKAGE_DIR:/package:ro" "$GO_IMAGE" \
  go version -m /package/bin/ai-gdm-server-windows-amd64.exe >"$WORK_DIR/windows-build-info"
run_active_command docker run --rm --name "ai-gdm-package-$RUN_ID-healthcheck" \
  --label "ai.gdm.package.validation.run=$RUN_ID" -v "$PACKAGE_DIR:/package:ro" "$GO_IMAGE" \
  go version -m /package/bin/ai-gdm-healthcheck-linux-amd64 >"$WORK_DIR/healthcheck-build-info"
run_active_command docker run --rm --name "ai-gdm-package-$RUN_ID-releasecheck" \
  --label "ai.gdm.package.validation.run=$RUN_ID" -v "$ROOT:/src:ro" -v "$PACKAGE_DIR:/package:ro" \
  -w /src "$GO_IMAGE" go run ./cmd/releasecheck /package
grep -F 'github.com/Requim/AI-GDM/cmd/server' "$WORK_DIR/linux-build-info" >/dev/null
grep -F 'github.com/Requim/AI-GDM/cmd/server' "$WORK_DIR/windows-build-info" >/dev/null
grep -F 'github.com/Requim/AI-GDM/cmd/healthcheck' "$WORK_DIR/healthcheck-build-info" >/dev/null
for file in "$WORK_DIR/linux-build-info" "$WORK_DIR/windows-build-info" "$WORK_DIR/healthcheck-build-info"; do
  grep -F 'go1.26.7' "$file" >/dev/null
  grep -F 'GOARCH=amd64' "$file" >/dev/null
  grep -F 'CGO_ENABLED=0' "$file" >/dev/null
done
grep -F 'GOOS=linux' "$WORK_DIR/linux-build-info" >/dev/null
grep -F 'GOOS=windows' "$WORK_DIR/windows-build-info" >/dev/null
grep -F 'GOOS=linux' "$WORK_DIR/healthcheck-build-info" >/dev/null
[ "$("$PACKAGE_DIR/bin/ai-gdm-server-linux-amd64" --version)" = "$PACKAGE_VERSION" ] || {
  printf '%s\n' 'Linux 服务二进制版本不匹配' >&2
  exit 1
}
strings "$PACKAGE_DIR/bin/ai-gdm-server-windows-amd64.exe" | grep -Fx "$PACKAGE_VERSION" >/dev/null || {
  printf '%s\n' 'Windows 服务二进制未嵌入发布版本' >&2
  exit 1
}

for value in "$PACKAGE_TREE"; do
  case "$value" in ''|*[!0-9a-f]*) printf '%s\n' '发布源码 SHA 无效' >&2; exit 1 ;; esac
  [ "${#value}" -eq 40 ] || { printf '%s\n' '发布源码 SHA 长度无效' >&2; exit 1; }
done
if [ "$PACKAGE_SOURCE_COMMIT" != unknown ]; then
  case "$PACKAGE_SOURCE_COMMIT" in ''|*[!0-9a-f]*) printf '%s\n' '发布 commit SHA 无效' >&2; exit 1 ;; esac
  [ "${#PACKAGE_SOURCE_COMMIT}" -eq 40 ] || { printf '%s\n' '发布 commit SHA 长度无效' >&2; exit 1; }
fi
case "$PACKAGE_SOURCE_SHA256" in ''|*[!0-9a-f]*) printf '%s\n' '发布 source SHA-256 无效' >&2; exit 1 ;; esac
[ "${#PACKAGE_SOURCE_SHA256}" -eq 64 ] || { printf '%s\n' '发布 source SHA-256 长度无效' >&2; exit 1; }
grep -F "\"createdAt\": \"$PACKAGE_CREATED_AT\"" "$PACKAGE_DIR/manifest.json" >/dev/null
grep -F "\"sourceCommit\": \"$PACKAGE_SOURCE_COMMIT\"" "$PACKAGE_DIR/manifest.json" >/dev/null
grep -F "\"sourceTree\": \"$PACKAGE_TREE\"" "$PACKAGE_DIR/manifest.json" >/dev/null
grep -F "\"sourceSha256\": \"$PACKAGE_SOURCE_SHA256\"" "$PACKAGE_DIR/manifest.json" >/dev/null
grep -F '"platform": "linux/amd64"' "$PACKAGE_DIR/manifest.json" >/dev/null
grep -F '"artifacts": [' "$PACKAGE_DIR/manifest.json" >/dev/null

APP_TAG=$(sed -n 's/^AI_GDM_IMAGE=//p' "$PACKAGE_DIR/deploy/release-images.env")
POSTGIS_TAG=$(sed -n 's/^AI_GDM_POSTGIS_IMAGE=//p' "$PACKAGE_DIR/deploy/release-images.env")
REDIS_TAG=$(sed -n 's/^AI_GDM_REDIS_IMAGE=//p' "$PACKAGE_DIR/deploy/release-images.env")
for reference in "$APP_TAG" "$POSTGIS_TAG" "$REDIS_TAG"; do
  [ -n "$reference" ] || { printf '%s\n' '离线镜像标签缺失' >&2; exit 1; }
  ! docker image inspect "$reference" >/dev/null 2>&1 || {
    printf '%s\n' "打包后遗留临时镜像标签: $reference" >&2
    exit 1
  }
done

for name in $VALIDATION_CONTAINERS; do
  ! docker inspect "$name" >/dev/null 2>&1 || {
    printf '%s\n' "打包门禁遗留验证容器: $name" >&2
    exit 1
  }
done

printf '%s\n' "P10.2 打包门禁通过：package=$(basename "$PACKAGE_ARCHIVE")"
