#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
AI_GDM_SECURITY_GATE_LIBRARY=source
export AI_GDM_SECURITY_GATE_LIBRARY
. "$ROOT/scripts/security-gate.lib.sh"

NODE_IMAGE=${NODE_VALIDATION_IMAGE:-node:22-bookworm}
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-system.XXXXXX")
SNAPSHOT_DIR="$WORK_DIR/source"
RESULT_LINES="$WORK_DIR/results.jsonl"
REPORT_FILE="$WORK_DIR/report.json"
LOG_DIR="$WORK_DIR/logs"
SYSTEM_FAILED=0
LLM_OUTCOME=disabled

cleanup() {
  chmod -R u+w "$WORK_DIR" 2>/dev/null || true
  rm -rf "$WORK_DIR"
}

terminate() {
  status=$1
  trap - EXIT HUP INT TERM
  cleanup
  exit "$status"
}

validate_live_flag() {
  name=$1
  value=$2
  case "$value" in
    0|1) ;;
    *) security_fail "$name 必须是 0 或 1" ;;
  esac
}

prepare_system_snapshot() {
  if ! security_is_git_root "$ROOT"; then
    [ -n "${SYSTEM_GATE_TREE_SHA:-}" ] || security_fail '归档模式必须传入 SYSTEM_GATE_TREE_SHA'
    [ -n "${SYSTEM_GATE_SOURCE_SHA256:-}" ] || security_fail '归档模式必须传入 SYSTEM_GATE_SOURCE_SHA256'
  fi
  SECURITY_E2E_TREE_SHA=${SYSTEM_GATE_TREE_SHA:-}
  SECURITY_E2E_SOURCE_SHA256=${SYSTEM_GATE_SOURCE_SHA256:-}
  export SECURITY_E2E_TREE_SHA SECURITY_E2E_SOURCE_SHA256
  security_prepare_snapshot "$ROOT" "$SNAPSHOT_DIR"
}

assessment_source_sha256() (
  base=$1
  manifest="$base/tests/assessment-e2e/source-files.txt"
  work=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-system-assessment.XXXXXX")
  trap 'rm -rf "$work"' EXIT HUP INT TERM
  : >"$work/files"
  while IFS= read -r entry || [ -n "$entry" ]; do
    entry=${entry%"$(printf '\r')"}
    case "$entry" in ''|'#'*) continue ;; esac
    if [ "${entry%/}" != "$entry" ]; then
      (cd "$base" && find "${entry%/}" -type f -print) >>"$work/files"
    else
      printf '%s\n' "$entry" >>"$work/files"
    fi
  done <"$manifest"
  LC_ALL=C sort -u "$work/files" -o "$work/files"
  printf '%s\n' 'assessment-e2e-source-v1' >"$work/records"
  while IFS= read -r path; do
    printf '%s  %s\n' "$(security_hash_file "$base/$path")" "$path" >>"$work/records"
  done <"$work/files"
  security_hash_file "$work/records"
)

record_gate() {
  id=$1
  script=$2
  mode=$3
  live_env=$4
  live_enabled=$5
  outcome=$6
  exit_code=$7
  pass_count=$8
  audit_marker=$9
  sentinel_verified=${10}
  log_sha256=${11}
  duration_ms=${12}
  printf '{"id":"%s","script":"%s","mode":"%s","liveEnv":"%s","liveEnabled":%s,"outcome":"%s","exitCode":%s,"passCount":%s,"auditMarker":"%s","sentinelVerified":%s,"logSha256":"%s","durationMs":%s,"treeSha":"%s","sourceSha256":"%s"}\n' \
    "$id" "$script" "$mode" "$live_env" "$live_enabled" "$outcome" "$exit_code" \
    "$pass_count" "$audit_marker" "$sentinel_verified" "$log_sha256" "$duration_ms" \
    "$SECURITY_TREE_SHA" "$SECURITY_SOURCE_SHA256" >>"$RESULT_LINES"
}

run_gate() {
  id=$1
  script=$2
  mode=$3
  live_env=$4
  live_enabled=$5
  pass_count=$6
  audit_marker=$7
  shift 7
  printf '%s\n' "运行系统门禁: $id"
  log_file="$LOG_DIR/$id.log"
  started=$(date +%s)
  status=0
  "$@" >"$log_file" 2>&1 || status=$?
  finished=$(date +%s)
  duration_ms=$(((finished - started) * 1000))
  cat "$log_file"
  sentinel_verified=false
  if [ "$status" -eq 0 ] && [ "$pass_count" != null ]; then
    if verify_gate_sentinel "$log_file" "$pass_count" "$audit_marker"; then
      sentinel_verified=true
    else
      status=1
    fi
  fi
  log_sha256=$(security_hash_file "$log_file")
  if [ "$status" -eq 0 ]; then
    record_gate "$id" "$script" "$mode" "$live_env" "$live_enabled" passed 0 \
      "$pass_count" "$audit_marker" "$sentinel_verified" "$log_sha256" "$duration_ms"
  else
    record_gate "$id" "$script" "$mode" "$live_env" "$live_enabled" failed "$status" \
      "$pass_count" "$audit_marker" "$sentinel_verified" "$log_sha256" "$duration_ms"
    SYSTEM_FAILED=1
  fi
  security_verify_stability "$ROOT" "$SNAPSHOT_DIR"
}

verify_gate_sentinel() {
  log_file=$1
  pass_count=$2
  audit_marker=$3
  matches=$(grep -E -c "(^|[^0-9])${pass_count} passed([^0-9]|$)" "$log_file" || true)
  if [ "$matches" -ne 1 ]; then
    printf '%s\n' "系统门禁通过数 sentinel 无效: expected=$pass_count matches=$matches" >&2
    return 1
  fi
  if ! grep -F -x -- "$audit_marker" "$log_file" >/dev/null; then
    printf '%s\n' "系统门禁审计标记缺失: $audit_marker" >&2
    return 1
  fi
}

run_live_gate() {
  id=$1
  script=$2
  live_env=$3
  enabled=$4
  degraded_code=${5-}
  degraded_marker=${6-}
  if [ "$enabled" -eq 0 ]; then
    log_file="$LOG_DIR/$id.log"
    printf '%s\n' "disabled:$live_env" >"$log_file"
    log_sha256=$(security_hash_file "$log_file")
    record_gate "$id" "$script" live "$live_env" false disabled null \
      null '' false "$log_sha256" 0
    [ "$id" != live-llm ] || LLM_OUTCOME=disabled
    return
  fi
  printf '%s\n' "运行系统门禁: $id"
  log_file="$LOG_DIR/$id.log"
  started=$(date +%s)
  status=0
  sh "$SNAPSHOT_DIR/$script" >"$log_file" 2>&1 || status=$?
  finished=$(date +%s)
  duration_ms=$(((finished - started) * 1000))
  cat "$log_file"
  log_sha256=$(security_hash_file "$log_file")
  if [ "$status" -eq 0 ]; then
    record_gate "$id" "$script" live "$live_env" true passed 0 \
      null '' false "$log_sha256" "$duration_ms"
    [ "$id" != live-llm ] || LLM_OUTCOME=generated
  elif [ -n "$degraded_code" ] && [ "$status" -eq "$degraded_code" ] && \
    grep -F -x -- "$degraded_marker" "$log_file" >/dev/null; then
    record_gate "$id" "$script" live "$live_env" true degraded "$status" \
      null "$degraded_marker" true "$log_sha256" "$duration_ms"
    [ "$id" != live-llm ] || LLM_OUTCOME=degraded
  else
    record_gate "$id" "$script" live "$live_env" true failed "$status" \
      null '' false "$log_sha256" "$duration_ms"
    SYSTEM_FAILED=1
    [ "$id" != live-llm ] || LLM_OUTCOME=failed
  fi
  security_verify_stability "$ROOT" "$SNAPSHOT_DIR"
}

count_ai_gdm_containers() {
  names=$(docker ps -a --format '{{.Names}}') || security_fail '无法审计 Docker 容器清理状态'
  printf '%s\n' "$names" | awk 'BEGIN { count=0 } /^ai-gdm/ { count++ } END { print count }'
}

count_ai_gdm_networks() {
  names=$(docker network ls --format '{{.Name}}') || security_fail '无法审计 Docker 网络清理状态'
  printf '%s\n' "$names" | awk 'BEGIN { count=0 } /^ai-gdm/ { count++ } END { print count }'
}

write_report() {
  containers=$1
  networks=$2
  {
    printf '{"version":1,"treeSha":"%s","sourceSha256":"%s","gates":[' \
      "$SECURITY_TREE_SHA" "$SECURITY_SOURCE_SHA256"
    separator=
    while IFS= read -r line; do
      printf '%s%s' "$separator" "$line"
      separator=,
    done <"$RESULT_LINES"
    printf '],"limitations":['
    printf '{"id":"amap-route","state":"degraded","reasonCode":"candidate_routes_do_not_confirm_road_open"}'
    if [ "$LLM_OUTCOME" = degraded ]; then
      printf ',{"id":"llm-provider","state":"degraded","reasonCode":"provider_upstream_error"}'
    fi
    printf '],'
    printf '"cleanup":{"containers":%s,"networks":%s}}\n' "$containers" "$networks"
  } >"$REPORT_FILE"
}

run_result_audit() {
  status=0
  sh "$SNAPSHOT_DIR/scripts/run-validation-container.sh" --network none \
    -v "$SNAPSHOT_DIR/tests/system-gate:/audit:ro" -v "$WORK_DIR:/runtime:ro" \
    "$NODE_IMAGE" sh -c '
      node /audit/audit-results.test.mjs &&
      exec node /audit/audit-results.mjs "$1" "$2" "$3" "$4"
    ' system-audit /audit/manifest.json /runtime/report.json \
      "$SECURITY_TREE_SHA" "$SECURITY_SOURCE_SHA256" || status=$?
  return "$status"
}

trap cleanup EXIT
trap 'terminate 129' HUP
trap 'terminate 130' INT
trap 'terminate 143' TERM

LIVE_EXPOSURE=${AI_GDM_SYSTEM_LIVE_EXPOSURE:-0}
LIVE_EARTHDATA=${AI_GDM_SYSTEM_LIVE_EARTHDATA:-0}
LIVE_WEATHER=${AI_GDM_SYSTEM_LIVE_WEATHER:-0}
LIVE_AMAP=${AI_GDM_SYSTEM_LIVE_AMAP:-0}
LIVE_LLM=${AI_GDM_SYSTEM_LIVE_LLM:-0}
validate_live_flag AI_GDM_SYSTEM_LIVE_EXPOSURE "$LIVE_EXPOSURE"
validate_live_flag AI_GDM_SYSTEM_LIVE_EARTHDATA "$LIVE_EARTHDATA"
validate_live_flag AI_GDM_SYSTEM_LIVE_WEATHER "$LIVE_WEATHER"
validate_live_flag AI_GDM_SYSTEM_LIVE_AMAP "$LIVE_AMAP"
validate_live_flag AI_GDM_SYSTEM_LIVE_LLM "$LIVE_LLM"

prepare_system_snapshot
: >"$RESULT_LINES"
mkdir -p "$LOG_DIR"

run_gate observability scripts/validate-observability.sh required '' false null '' \
  sh "$SNAPSHOT_DIR/scripts/validate-observability.sh"
AI_GDM_LIVE_EXPOSURE=$LIVE_EXPOSURE
export AI_GDM_LIVE_EXPOSURE
run_gate exposure-providers scripts/validate-exposure-providers.sh required \
  AI_GDM_SYSTEM_LIVE_EXPOSURE "$([ "$LIVE_EXPOSURE" -eq 1 ] && printf true || printf false)" \
  null '' \
  sh "$SNAPSHOT_DIR/scripts/validate-exposure-providers.sh"
unset AI_GDM_LIVE_EXPOSURE
run_gate loss-api scripts/validate-loss-api.sh required '' false null '' \
  sh "$SNAPSHOT_DIR/scripts/validate-loss-api.sh"
run_gate risk-map-go scripts/validate-map-ui.sh required '' false null '' \
  sh "$SNAPSHOT_DIR/scripts/validate-map-ui.sh"
run_gate risk-map-chromium scripts/validate-risk-map-browser.sh required '' false 17 \
  '风险地图 fail-closed 浏览器回归通过' \
  sh "$SNAPSHOT_DIR/scripts/validate-risk-map-browser.sh"
run_gate evacuation-ui scripts/validate-evacuation-ui.sh required '' false null '' \
  sh "$SNAPSHOT_DIR/scripts/validate-evacuation-ui.sh"
run_gate evacuation-postgis scripts/validate-evacuation.sh required '' false null '' \
  sh "$SNAPSHOT_DIR/scripts/validate-evacuation.sh"
run_gate evacuation-chromium scripts/validate-evacuation-browser.sh required '' false 42 \
  '疏散工作台 fail-closed 浏览器回归通过' \
  sh "$SNAPSHOT_DIR/scripts/validate-evacuation-browser.sh"
run_gate assessment-ui scripts/validate-assessment-ui.sh required '' false null '' \
  sh "$SNAPSHOT_DIR/scripts/validate-assessment-ui.sh"
ASSESSMENT_E2E_TREE_SHA=$SECURITY_TREE_SHA
ASSESSMENT_E2E_SOURCE_SHA256=$(assessment_source_sha256 "$SNAPSHOT_DIR")
export ASSESSMENT_E2E_TREE_SHA ASSESSMENT_E2E_SOURCE_SHA256
run_gate assessment-chromium scripts/validate-assessment-browser.sh required '' false 119 \
  '评估界面 Playwright 结果审计通过: passed=119 failed=0 skipped=0' \
  sh "$SNAPSHOT_DIR/scripts/validate-assessment-browser.sh"
unset ASSESSMENT_E2E_TREE_SHA ASSESSMENT_E2E_SOURCE_SHA256
run_gate validate-go scripts/validate-go.sh required '' false null '' \
  sh "$SNAPSHOT_DIR/scripts/validate-go.sh"
run_live_gate live-earthdata scripts/validate-earthdata.sh AI_GDM_SYSTEM_LIVE_EARTHDATA "$LIVE_EARTHDATA"
run_live_gate live-weather scripts/validate-weather.sh AI_GDM_SYSTEM_LIVE_WEATHER "$LIVE_WEATHER"
run_live_gate live-amap scripts/validate-amap-live.sh AI_GDM_SYSTEM_LIVE_AMAP "$LIVE_AMAP"
run_live_gate live-llm scripts/validate-llm-live.sh AI_GDM_SYSTEM_LIVE_LLM "$LIVE_LLM" 75 \
  '真实 LLM 上游暂不可用，模型目录与系统降级合同验证通过'

CONTAINER_COUNT=$(count_ai_gdm_containers)
NETWORK_COUNT=$(count_ai_gdm_networks)
write_report "$CONTAINER_COUNT" "$NETWORK_COUNT"
AUDIT_STATUS=0
run_result_audit || AUDIT_STATUS=$?
security_verify_stability "$ROOT" "$SNAPSHOT_DIR"
[ "$(count_ai_gdm_containers)" -eq 0 ] || security_fail '系统门禁结束后仍有 ai-gdm 容器遗留'
[ "$(count_ai_gdm_networks)" -eq 0 ] || security_fail '系统门禁结束后仍有 ai-gdm 网络遗留'
if [ "$SYSTEM_FAILED" -ne 0 ] || [ "$AUDIT_STATUS" -ne 0 ]; then
  security_fail "P9.3 系统门禁失败: gates=$SYSTEM_FAILED audit=$AUDIT_STATUS"
fi

printf '%s\n' \
  "P9.3 系统门禁通过：mode=$SECURITY_SOURCE_MODE tree=$SECURITY_TREE_SHA source_sha256=$SECURITY_SOURCE_SHA256 live_exposure=$LIVE_EXPOSURE live_earthdata=$LIVE_EARTHDATA live_weather=$LIVE_WEATHER live_amap=$LIVE_AMAP live_llm=$LIVE_LLM llm_outcome=$LLM_OUTCOME"
