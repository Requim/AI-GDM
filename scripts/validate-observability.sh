#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
AI_GDM_SECURITY_GATE_LIBRARY=source
export AI_GDM_SECURITY_GATE_LIBRARY
. "$ROOT/scripts/security-gate.lib.sh"

GO_IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm@sha256:6ef6e30f0ea5c384f6d111cf856e024e3086bbdcb1779da3f3b3fbba0aea53d2}
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-observability.XXXXXX")
SNAPSHOT_DIR="$WORK_DIR/source"
CIDFILE="$WORK_DIR/go.cid"
RUN_ID=
CONTAINER_NAME=
ACTIVE_CHILD_PID=

stop_validation_child() {
  [ -n "$ACTIVE_CHILD_PID" ] || return 0
  kill -TERM "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  kill -KILL "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  wait "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  ACTIVE_CHILD_PID=
}

remove_validation_container() {
  [ -n "$CONTAINER_NAME" ] && [ -s "$CIDFILE" ] || return 0
  cid=$(tr -d '\r\n' <"$CIDFILE" 2>/dev/null || true)
  case "$cid" in ''|*[!0-9a-f]*) return 0 ;; esac
  [ "${#cid}" -eq 64 ] || return 0
  metadata=$(docker inspect -f '{{.Name}}|{{index .Config.Labels "ai.gdm.observability.run"}}' \
    "$cid" 2>/dev/null || true)
  [ "$metadata" = "/$CONTAINER_NAME|$RUN_ID" ] || return 0
  docker rm -f "$cid" >/dev/null 2>&1 || true
}

cleanup() {
  stop_validation_child
  remove_validation_container
  chmod -R u+w "$WORK_DIR" 2>/dev/null || true
  rm -rf "$WORK_DIR"
}

terminate() {
  status=$1
  trap - EXIT HUP INT TERM
  cleanup
  exit "$status"
}

run_validation_container() {
  sh "$SNAPSHOT_DIR/scripts/run-validation-container.sh" "$@" &
  ACTIVE_CHILD_PID=$!
  child_status=0
  wait "$ACTIVE_CHILD_PID" || child_status=$?
  ACTIVE_CHILD_PID=
  return "$child_status"
}

prepare_observability_snapshot() {
  if ! security_is_git_root "$ROOT"; then
    [ -n "${OBSERVABILITY_GATE_TREE_SHA:-}" ] || \
      security_fail '归档模式必须传入 OBSERVABILITY_GATE_TREE_SHA'
    [ -n "${OBSERVABILITY_GATE_SOURCE_SHA256:-}" ] || \
      security_fail '归档模式必须传入 OBSERVABILITY_GATE_SOURCE_SHA256'
  fi
  SECURITY_E2E_TREE_SHA=${OBSERVABILITY_GATE_TREE_SHA:-}
  SECURITY_E2E_SOURCE_SHA256=${OBSERVABILITY_GATE_SOURCE_SHA256:-}
  export SECURITY_E2E_TREE_SHA SECURITY_E2E_SOURCE_SHA256
  security_prepare_snapshot "$ROOT" "$SNAPSHOT_DIR"
}

trap cleanup EXIT
trap 'terminate 129' HUP
trap 'terminate 130' INT
trap 'terminate 143' TERM

prepare_observability_snapshot
security_validate_image_reference "$GO_IMAGE" 'GO_VALIDATION_IMAGE'
RUN_ID=$(security_random_token)
CONTAINER_NAME="ai-gdm-observability-go-$RUN_ID"

run_validation_container --name "$CONTAINER_NAME" --cidfile "$CIDFILE" \
  --label "ai.gdm.observability.run=$RUN_ID" \
  -v "$SNAPSHOT_DIR:/src:ro" -w /src -e "GOPROXY=$GO_PROXY" "$GO_IMAGE" sh -c '
    go mod verify &&
    go test -race \
      ./internal/platform/observability \
      ./internal/platform/httpserver \
      ./internal/application/dashboard \
      ./internal/adapters/http/webui \
      ./cmd/server -count=20 &&
    go vet ./... &&
    go build ./...
  '
security_verify_stability "$ROOT" "$SNAPSHOT_DIR"

printf '%s\n' \
  "P9.2 观测门禁通过：mode=$SECURITY_SOURCE_MODE tree=$SECURITY_TREE_SHA source_sha256=$SECURITY_SOURCE_SHA256"
