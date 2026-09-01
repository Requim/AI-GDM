#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
AI_GDM_SECURITY_GATE_LIBRARY=source
export AI_GDM_SECURITY_GATE_LIBRARY
. "$ROOT/scripts/security-gate.lib.sh"

GO_IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm@sha256:6ef6e30f0ea5c384f6d111cf856e024e3086bbdcb1779da3f3b3fbba0aea53d2}
NODE_IMAGE=${NODE_VALIDATION_IMAGE:-node:22-bookworm@sha256:8a34c4ab3ea2c5cd194f07e317b2a8f09461d3c8b05c4e34c8ccd56d56024c4d}
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-security-main.XXXXXX")
SNAPSHOT_DIR="$WORK_DIR/source"
RUN_ID=
GO_CONTAINER_NAME=
NODE_CONTAINER_NAME=
GO_CIDFILE="$WORK_DIR/go.cid"
NODE_CIDFILE="$WORK_DIR/node.cid"
ACTIVE_CHILD_PID=

remove_validation_container() {
  cidfile=$1
  expected_name=$2
  [ -n "$expected_name" ] && [ -s "$cidfile" ] || return 0
  cid=$(tr -d '\r\n' <"$cidfile" 2>/dev/null || true)
  case "$cid" in ''|*[!0-9a-f]*) return 0 ;; esac
  [ "${#cid}" -eq 64 ] || return 0
	metadata=$(docker inspect -f '{{.Name}}|{{index .Config.Labels "ai.gdm.security.run"}}' "$cid" 2>/dev/null || true)
	[ "$metadata" = "/$expected_name|$RUN_ID" ] || return 0
  docker rm -f "$cid" >/dev/null 2>&1 || true
}

cleanup() {
	stop_validation_child
	remove_validation_container "$NODE_CIDFILE" "$NODE_CONTAINER_NAME"
	remove_validation_container "$GO_CIDFILE" "$GO_CONTAINER_NAME"
  chmod -R u+w "$WORK_DIR" 2>/dev/null || true
  rm -rf "$WORK_DIR"
}

stop_validation_child() {
  [ -n "$ACTIVE_CHILD_PID" ] || return 0
  kill -TERM "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  kill -KILL "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  wait "$ACTIVE_CHILD_PID" >/dev/null 2>&1 || true
  ACTIVE_CHILD_PID=
}

run_validation_container() {
  sh "$SNAPSHOT_DIR/scripts/run-validation-container.sh" "$@" &
  ACTIVE_CHILD_PID=$!
  child_status=0
  wait "$ACTIVE_CHILD_PID" || child_status=$?
  ACTIVE_CHILD_PID=
  return "$child_status"
}

terminate() {
  status=$1
  trap - EXIT HUP INT TERM
  cleanup
  exit "$status"
}

trap cleanup EXIT
trap 'terminate 129' HUP
trap 'terminate 130' INT
trap 'terminate 143' TERM

security_prepare_snapshot "$ROOT" "$SNAPSHOT_DIR"
security_validate_image_reference "$GO_IMAGE" 'GO_VALIDATION_IMAGE'
security_validate_image_reference "$NODE_IMAGE" 'NODE_VALIDATION_IMAGE'
RUN_ID=$(security_random_token)
GO_CONTAINER_NAME="ai-gdm-security-go-$RUN_ID"
NODE_CONTAINER_NAME="ai-gdm-security-node-$RUN_ID"

run_validation_container --name "$GO_CONTAINER_NAME" --cidfile "$GO_CIDFILE" \
	--label "ai.gdm.security.run=$RUN_ID" \
  -v "$SNAPSHOT_DIR:/src:ro" -w /src \
  -e "GOPROXY=$GO_PROXY" "$GO_IMAGE" sh -c '
    go mod verify &&
    go test -race ./internal/platform/config ./internal/platform/httpserver \
      ./internal/adapters/http/mapapi ./internal/adapters/http/webui ./cmd/server ./scripts \
      -skip '^TestReleaseArchiveValidator' -count=20 &&
    go test ./scripts -run '^TestReleaseArchiveValidator' -count=1 &&
    go vet ./... &&
    go build ./...
  '
security_verify_stability "$ROOT" "$SNAPSHOT_DIR"

run_validation_container --name "$NODE_CONTAINER_NAME" --cidfile "$NODE_CIDFILE" \
	--label "ai.gdm.security.run=$RUN_ID" \
  -v "$SNAPSHOT_DIR:/src:ro" -w /src \
  "$NODE_IMAGE" sh -c '
    node --check internal/adapters/http/webui/static/api.js &&
    node --check tests/security-e2e/specs/security.spec.js &&
    node --check tests/security-e2e/audit-results.mjs &&
    node --check tests/security-e2e/audit-results.test.mjs &&
    node tests/security-e2e/audit-results.test.mjs
  '
security_verify_stability "$ROOT" "$SNAPSHOT_DIR"

SECURITY_GATE_PREPARED_ROOT="$SNAPSHOT_DIR" \
SECURITY_E2E_TREE_SHA="$SECURITY_TREE_SHA" \
SECURITY_E2E_SOURCE_SHA256="$SECURITY_SOURCE_SHA256" \
  sh "$SNAPSHOT_DIR/scripts/validate-security-browser.sh"
security_verify_stability "$ROOT" "$SNAPSHOT_DIR"

printf '%s\n' \
  "P9.1 安全门禁通过：mode=$SECURITY_SOURCE_MODE tree=$SECURITY_TREE_SHA source_sha256=$SECURITY_SOURCE_SHA256"
