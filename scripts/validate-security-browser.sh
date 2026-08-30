#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
AI_GDM_SECURITY_GATE_LIBRARY=source
export AI_GDM_SECURITY_GATE_LIBRARY
. "$ROOT/scripts/security-gate.lib.sh"

GO_IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm@sha256:6ef6e30f0ea5c384f6d111cf856e024e3086bbdcb1779da3f3b3fbba0aea53d2}
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')
IMAGE_REPOSITORY=${SECURITY_BROWSER_IMAGE_REPOSITORY:-ai-gdm-security-browser}
WORK_DIR=
RUNTIME_DIR=
SNAPSHOT_DIR=
FIXTURE_NAME=
BROWSER_NAME=
BROWSER_IMAGE=
FIXTURE_CIDFILE=
BROWSER_CIDFILE=
IMAGE_IIDFILE=
BROWSER_IMAGE_ID=
FIXTURE_ID=
ACTIVE_CHILD_PID=

cleanup() {
  stop_active_child
  remove_owned_container "$BROWSER_CIDFILE" "$BROWSER_NAME"
  remove_owned_container "$FIXTURE_CIDFILE" "$FIXTURE_NAME"
  remove_owned_image
  [ -n "$WORK_DIR" ] && chmod -R u+w "$WORK_DIR" 2>/dev/null || true
  [ -n "$WORK_DIR" ] && rm -rf "$WORK_DIR"
  [ -n "$RUNTIME_DIR" ] && rm -rf "$RUNTIME_DIR"
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

stop_active_child() {
  [ -n "$ACTIVE_CHILD_PID" ] || return 0
  kill -TERM "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  kill -KILL "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  wait "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  ACTIVE_CHILD_PID=
}

remove_owned_container() {
  cidfile=$1
  expected_name=$2
  [ -n "$expected_name" ] && [ -s "$cidfile" ] || return 0
  cid=$(tr -d '\r\n' <"$cidfile" 2>/dev/null || true)
  security_validate_object_id "$cid" || return 0
  metadata=$(docker inspect -f '{{.Name}}|{{index .Config.Labels "ai.gdm.security.run"}}' "$cid" 2>/dev/null || true)
  [ "$metadata" = "/$expected_name|$RUN_ID" ] || return 0
  docker rm -f "$cid" >/dev/null 2>&1 || true
}

owned_container_id() {
  cidfile=$1
  expected_name=$2
  cid=$(tr -d '\r\n' <"$cidfile" 2>/dev/null || true)
  security_validate_object_id "$cid" || security_fail '安全验证容器 CID 无效'
  metadata=$(docker inspect -f '{{.Name}}|{{index .Config.Labels "ai.gdm.security.run"}}' "$cid" 2>/dev/null || true)
  [ "$metadata" = "/$expected_name|$RUN_ID" ] || security_fail '安全验证容器所有权无效'
  printf '%s\n' "$cid"
}

remove_owned_image() {
  [ -n "$BROWSER_IMAGE" ] || return 0
  image_id=$BROWSER_IMAGE_ID
  if ! security_validate_object_id "$image_id"; then
    image_id=$(browser_image_iid_from_file) || return 0
  fi
  metadata=$(browser_image_metadata "$BROWSER_IMAGE")
  expected="$image_id|$RUN_ID|$SECURITY_TREE_SHA|$SECURITY_SOURCE_SHA256"
  [ "$metadata" = "$expected" ] || return 0
  docker image rm "$BROWSER_IMAGE" >/dev/null 2>&1 || true
}

browser_image_iid_from_file() {
  [ -n "$IMAGE_IIDFILE" ] && [ -s "$IMAGE_IIDFILE" ] || return 1
  image_id=$(tr -d '\r\n' <"$IMAGE_IIDFILE" 2>/dev/null || true)
  security_validate_object_id "$image_id" || return 1
  printf '%s\n' "$image_id"
}

security_validate_object_id() {
  case "$1" in sha256:*) value=${1#sha256:} ;; *) value=$1 ;; esac
  case "$value" in ''|*[!0-9a-f]*) return 1 ;; esac
  [ "${#value}" -eq 64 ]
}

browser_image_metadata() {
  docker image inspect -f '{{.Id}}|{{index .Config.Labels "ai.gdm.security.run"}}|{{index .Config.Labels "ai.gdm.security.tree"}}|{{index .Config.Labels "ai.gdm.security.source"}}' "$1" 2>/dev/null || true
}

ensure_docker_names_available() {
  docker container inspect "$FIXTURE_NAME" >/dev/null 2>&1 && security_fail '安全 fixture 容器名称已被占用'
  docker container inspect "$BROWSER_NAME" >/dev/null 2>&1 && security_fail '安全浏览器容器名称已被占用'
  docker image inspect "$BROWSER_IMAGE" >/dev/null 2>&1 && security_fail '安全浏览器镜像 tag 已被占用'
  return 0
}

run_active_command() {
  "$@" &
  ACTIVE_CHILD_PID=$!
  status=0
  wait "$ACTIVE_CHILD_PID" || status=$?
  ACTIVE_CHILD_PID=
  return "$status"
}

build_browser_image() {
  run_active_command docker build --pull=false --iidfile "$IMAGE_IIDFILE" \
    --label "ai.gdm.security.run=$RUN_ID" --label "ai.gdm.security.tree=$SECURITY_TREE_SHA" \
    --label "ai.gdm.security.source=$SECURITY_SOURCE_SHA256" -t "$BROWSER_IMAGE" \
    -f "$SNAPSHOT_DIR/tests/security-e2e/Dockerfile" "$SNAPSHOT_DIR/tests/security-e2e"
  BROWSER_IMAGE_ID=$(tr -d '\r\n' <"$IMAGE_IIDFILE" 2>/dev/null || true)
  security_validate_object_id "$BROWSER_IMAGE_ID" || security_fail '安全浏览器镜像 IID 无效'
  expected="$BROWSER_IMAGE_ID|$RUN_ID|$SECURITY_TREE_SHA|$SECURITY_SOURCE_SHA256"
  [ "$(browser_image_metadata "$BROWSER_IMAGE")" = "$expected" ] || security_fail '安全浏览器镜像所有权无效'
}

run_owned_container() {
  run_active_command sh "$SNAPSHOT_DIR/scripts/run-validation-container.sh" "$@"
}

prepare_browser_source() {
  if [ -n "${SECURITY_GATE_PREPARED_ROOT:-}" ]; then
    prepared=$(CDPATH= cd -- "$SECURITY_GATE_PREPARED_ROOT" && pwd -P)
    [ "$prepared" = "$ROOT" ] || security_fail '已准备安全快照路径与浏览器脚本不一致'
    SNAPSHOT_DIR=$ROOT
    security_use_prepared_snapshot "$SNAPSHOT_DIR"
    return
  fi
  WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-security-browser.XXXXXX")
  SNAPSHOT_DIR="$WORK_DIR/source"
  security_prepare_snapshot "$ROOT" "$SNAPSHOT_DIR"
}

validate_image_repository() {
  case "$IMAGE_REPOSITORY" in
    ''|*[!A-Za-z0-9._/-]*) security_fail '安全浏览器镜像仓库名无效' ;;
  esac
}

choose_port() {
  port=${SECURITY_E2E_PORT:-$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')}
  case "$port" in ''|*[!0-9]*) security_fail 'SECURITY_E2E_PORT 必须是 1 到 65535 的整数' ;; esac
  [ "$port" -ge 1 ] && [ "$port" -le 65535 ] || security_fail 'SECURITY_E2E_PORT 超出有效范围'
  printf '%s\n' "$port"
}

wait_for_fixture() {
  attempt=0
  until curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 60 ]; then
      docker logs "$FIXTURE_ID" || true
      security_fail 'P9.1 浏览器安全 fixture 启动超时'
    fi
    running=$(docker inspect -f '{{.State.Running}}' "$FIXTURE_ID" 2>/dev/null || true)
    [ "$running" = true ] || security_fail 'P9.1 浏览器安全 fixture 提前退出'
    sleep 1
  done
}

prepare_browser_source
validate_image_repository
security_validate_image_reference "$GO_IMAGE" 'GO_VALIDATION_IMAGE'
RUN_ID=$(security_random_token)
ADMIN_TOKEN=$(security_random_token)
PORT=$(choose_port)
RUNTIME_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-security-runtime.XXXXXX")
FIXTURE_NAME="ai-gdm-security-fixture-$RUN_ID"
BROWSER_NAME="ai-gdm-security-playwright-$RUN_ID"
BROWSER_IMAGE="$IMAGE_REPOSITORY:$SECURITY_TREE_SHA-$RUN_ID"
FIXTURE_CIDFILE="$RUNTIME_DIR/fixture.cid"
BROWSER_CIDFILE="$RUNTIME_DIR/browser.cid"
IMAGE_IIDFILE="$RUNTIME_DIR/browser.iid"

ensure_docker_names_available
build_browser_image

run_owned_container -d --name "$FIXTURE_NAME" --cidfile "$FIXTURE_CIDFILE" \
  --label "ai.gdm.security.run=$RUN_ID" --network host \
  -v "$SNAPSHOT_DIR:/src:ro" -w /src -e "GOPROXY=$GO_PROXY" -e "E2E_ADDR=127.0.0.1:$PORT" \
  -e "APP_ADMIN_TOKEN=$ADMIN_TOKEN" "$GO_IMAGE" sh -c 'go run ./tests/security-e2e/fixture'

FIXTURE_ID=$(owned_container_id "$FIXTURE_CIDFILE" "$FIXTURE_NAME")
wait_for_fixture
run_owned_container --name "$BROWSER_NAME" --cidfile "$BROWSER_CIDFILE" \
  --label "ai.gdm.security.run=$RUN_ID" --network host --shm-size=1g \
  -e "E2E_BASE_URL=http://127.0.0.1:$PORT" -e "SECURITY_TEST_ADMIN_TOKEN=$ADMIN_TOKEN" \
  "$BROWSER_IMAGE_ID"

security_verify_stability "$ROOT" "$SNAPSHOT_DIR"
printf '%s\n' \
  "P9.1 安全浏览器回归通过：mode=$SECURITY_SOURCE_MODE tree=$SECURITY_TREE_SHA source_sha256=$SECURITY_SOURCE_SHA256 image=$BROWSER_IMAGE_ID"
