#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
GO_IMAGE=${GO_VALIDATION_IMAGE:-golang:1.26.7-bookworm}
GO_PROXY=$(printenv GOPROXY 2>/dev/null || printf '%s' 'https://goproxy.cn,direct')
NPM_REGISTRY=${NPM_REGISTRY:-https://registry.npmjs.org}
REQUESTED_PORT=${ASSESSMENT_E2E_PORT:-0}
SOURCE_MANIFEST_REL=tests/assessment-e2e/source-files.txt
WORK_DIR=
RUNTIME_DIR=
FIXTURE_NAME=
BROWSER_NAME=
AUDIT_NAME=

cleanup() {
  for name in "$AUDIT_NAME" "$BROWSER_NAME" "$FIXTURE_NAME"; do
    [ -n "$name" ] && docker rm -f "$name" >/dev/null 2>&1 || true
  done
  [ -n "$WORK_DIR" ] && chmod -R u+w "$WORK_DIR" 2>/dev/null || true
  [ -n "$WORK_DIR" ] && rm -rf "$WORK_DIR"
  [ -n "$RUNTIME_DIR" ] && rm -rf "$RUNTIME_DIR"
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

is_git_root() {
  command -v git >/dev/null 2>&1 || return 1
  top=$(git -c "safe.directory=$ROOT" -C "$ROOT" rev-parse --show-toplevel 2>/dev/null) || return 1
  top=$(CDPATH= cd -- "$top" && pwd -P)
  [ "$top" = "$ROOT" ]
}

collect_source_files() (
  base=$1
  manifest="$base/$SOURCE_MANIFEST_REL"
  [ -f "$manifest" ] || fail '评估界面源码清单不存在'
  cd "$base"
  while IFS= read -r entry || [ -n "$entry" ]; do
    entry=${entry%"$(printf '\r')"}
    case "$entry" in
      ''|'#'*) continue ;;
      *..*|/*|./*|*[!A-Za-z0-9_./-]*) fail "评估界面源码清单路径无效: $entry" ;;
    esac
    if [ "${entry%/}" != "$entry" ]; then
      directory=${entry%/}
      [ -d "$directory" ] || fail "评估界面源码清单目录不存在: $entry"
      [ -z "$(find "$directory" -type l -print -quit)" ] || \
        fail "评估界面源码清单目录包含符号链接: $entry"
      find "$directory" -type f -print
      continue
    fi
    [ -f "$entry" ] && [ ! -L "$entry" ] || fail "评估界面源码清单文件无效: $entry"
    printf '%s\n' "$entry"
  done <"$manifest"
)

write_source_file_list() (
  base=$1
  output=$2
  collect_source_files "$base" >"$output.unsorted"
  LC_ALL=C sort "$output.unsorted" >"$output"
  [ -s "$output" ] || fail '评估界面源码清单为空'
  uniq -d "$output" >"$output.duplicates"
  [ ! -s "$output.duplicates" ] || fail '评估界面源码清单展开后包含重复文件'
  while IFS= read -r path; do
    case "$path" in
      ''|*..*|/*|./*|*[!A-Za-z0-9_./-]*) fail "评估界面源码路径无效: $path" ;;
    esac
  done <"$output"
)

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    value=$(sha256sum "$1") || fail "评估界面源码无法读取: $1"
    printf '%s\n' "${value%% *}"
    return
  fi
  if command -v openssl >/dev/null 2>&1; then
    value=$(openssl dgst -sha256 "$1") || fail "评估界面源码无法读取: $1"
    printf '%s\n' "${value##* }"
    return
  fi
  fail '评估界面源码摘要要求 sha256sum 或 openssl'
}

calculate_source_sha256() (
  base=$1
  work=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-assessment-source.XXXXXX")
  trap 'rm -rf "$work"' EXIT INT TERM
  write_source_file_list "$base" "$work/files"
  printf '%s\n' 'assessment-e2e-source-v1' >"$work/records"
  while IFS= read -r path; do
    value=$(hash_file "$base/$path")
    printf '%s  %s\n' "$value" "$path" >>"$work/records"
  done <"$work/files"
  hash_file "$work/records"
)

current_tree_sha() (
  cd "$ROOT"
  work=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-assessment-index.XXXXXX")
  trap 'rm -rf "$work"' EXIT INT TERM
  index=$(git -c "safe.directory=$ROOT" rev-parse --git-path index)
  cp "$index" "$work/index"
  GIT_INDEX_FILE="$work/index" git -c "safe.directory=$ROOT" write-tree
)

archive_tree_sha() (
  command -v git >/dev/null 2>&1 || fail '归档模式校验 Git tree 要求 git'
  work=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-assessment-tree.XXXXXX")
  trap 'rm -rf "$work"' EXIT INT TERM
  git init -q "$work/repository"
  git --git-dir="$work/repository/.git" --work-tree="$ROOT" add -f -A -- .
  git --git-dir="$work/repository/.git" write-tree
)

validate_archive_inputs() {
  [ "${ASSESSMENT_E2E_TREE_SHA+x}" = x ] && [ -n "$ASSESSMENT_E2E_TREE_SHA" ] || \
    fail '归档模式必须传入 ASSESSMENT_E2E_TREE_SHA'
  case "$ASSESSMENT_E2E_TREE_SHA" in
    *[!0-9a-f]*) fail '归档模式 ASSESSMENT_E2E_TREE_SHA 必须是 40 位小写十六进制' ;;
  esac
  [ "${#ASSESSMENT_E2E_TREE_SHA}" -eq 40 ] || \
    fail '归档模式 ASSESSMENT_E2E_TREE_SHA 必须是 40 位小写十六进制'
  [ "${ASSESSMENT_E2E_SOURCE_SHA256+x}" = x ] && [ -n "$ASSESSMENT_E2E_SOURCE_SHA256" ] || \
    fail '归档模式必须传入 ASSESSMENT_E2E_SOURCE_SHA256'
  case "$ASSESSMENT_E2E_SOURCE_SHA256" in
    *[!0-9a-f]*) fail '归档模式 ASSESSMENT_E2E_SOURCE_SHA256 必须是 64 位小写十六进制' ;;
  esac
  [ "${#ASSESSMENT_E2E_SOURCE_SHA256}" -eq 64 ] || \
    fail '归档模式 ASSESSMENT_E2E_SOURCE_SHA256 必须是 64 位小写十六进制'
}

generate_fixture_token() {
  raw=$(LC_ALL=C od -An -N16 -tx1 </dev/urandom 2>/dev/null) || \
    fail '评估界面随机源读取失败'
  token=$(printf '%s' "$raw" | tr -d '[:space:]') || fail '评估界面随机令牌生成失败'
  case "$token" in
    *[!0-9a-f]*) fail '评估界面随机令牌必须是 32 位小写十六进制' ;;
  esac
  [ "${#token}" -eq 32 ] || fail '评估界面随机令牌必须是 32 位小写十六进制'
  printf '%s\n' "$token"
}

copy_audited_source() (
  source=$1
  destination=$2
  list=$(mktemp "${TMPDIR:-/tmp}/ai-gdm-assessment-files.XXXXXX")
  trap 'rm -f "$list"' EXIT INT TERM
  write_source_file_list "$source" "$list"
  mkdir -p "$destination"
  (cd "$source" && tar -cf - -T "$list") | (cd "$destination" && tar -xf -)
)

verify_git_source() (
  snapshot=$1
  work=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-assessment-git.XXXXXX")
  trap 'rm -rf "$work"' EXIT INT TERM
  write_source_file_list "$ROOT" "$work/worktree"
  write_source_file_list "$snapshot" "$work/snapshot"
  cmp -s "$work/worktree" "$work/snapshot" || \
    fail '评估界面浏览器门禁拒绝 tree 外源码（含被忽略文件）'
  while IFS= read -r path; do
    marker=$(git -c "safe.directory=$ROOT" -C "$ROOT" ls-files -v -- "$path")
    [ -n "$marker" ] || fail "评估界面受审计源码未被 Git tree 跟踪: $path"
    flag=${marker%% *}
    case "$flag" in
      S|[a-z]) fail "评估界面受审计源码包含 Git 隐藏标记: $path" ;;
    esac
    [ "$(hash_file "$ROOT/$path")" = "$(hash_file "$snapshot/$path")" ] || \
      fail "评估界面受审计源码与 Git tree 不一致: $path"
  done <"$work/worktree"
)

verify_source_stability() {
  [ "$(calculate_source_sha256 "$SNAPSHOT_DIR")" = "$SOURCE_SHA256" ] || \
    fail '评估界面不可变源码快照在运行期间发生变化'
  if [ "$SOURCE_MODE" = git ]; then
    [ "$(current_tree_sha)" = "$TREE_SHA" ] || fail '评估界面 Git tree 在运行期间发生变化'
    verify_git_source "$TREE_SNAPSHOT_DIR"
    return
  fi
  [ "$(calculate_source_sha256 "$ROOT")" = "$SOURCE_SHA256" ] || \
    fail '评估界面归档源码在运行期间发生变化'
  [ "$(archive_tree_sha)" = "$TREE_SHA" ] || fail '评估界面归档 tree 在运行期间发生变化'
}

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-assessment-work.XXXXXX")
RUNTIME_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-assessment-runtime.XXXXXX")
TREE_SNAPSHOT_DIR="$WORK_DIR/tree"
SNAPSHOT_DIR="$WORK_DIR/source"
RUNTIME_FILE="$RUNTIME_DIR/address"
RESULT_FILE="$RUNTIME_DIR/playwright-results.json"

if is_git_root; then
  SOURCE_MODE=git
  TREE_SHA=$(current_tree_sha)
  mkdir -p "$TREE_SNAPSHOT_DIR"
  git -c "safe.directory=$ROOT" -C "$ROOT" archive --format=tar "$TREE_SHA" | \
    tar -xf - -C "$TREE_SNAPSHOT_DIR"
  verify_git_source "$TREE_SNAPSHOT_DIR"
  copy_audited_source "$TREE_SNAPSHOT_DIR" "$SNAPSHOT_DIR"
else
  SOURCE_MODE=archive
  [ ! -e "$ROOT/.git" ] || fail '评估界面 Git 元数据存在但工作区不可用'
  validate_archive_inputs
  TREE_SHA=$(archive_tree_sha)
  [ "$ASSESSMENT_E2E_TREE_SHA" = "$TREE_SHA" ] || \
    fail "ASSESSMENT_E2E_TREE_SHA 与实际 Git tree 不一致: requested=$ASSESSMENT_E2E_TREE_SHA actual=$TREE_SHA"
  copy_audited_source "$ROOT" "$SNAPSHOT_DIR"
  TREE_SNAPSHOT_DIR=$SNAPSHOT_DIR
fi

case "$TREE_SHA" in
  ''|*[!0-9a-f]*) fail '实际 Git tree 不是合法 SHA-1' ;;
esac
[ "${#TREE_SHA}" -eq 40 ] || fail '实际 Git tree 不是 40 位 SHA-1'
if [ "${ASSESSMENT_E2E_TREE_SHA+x}" = x ] && [ "$ASSESSMENT_E2E_TREE_SHA" != "$TREE_SHA" ]; then
  fail "ASSESSMENT_E2E_TREE_SHA 与实际 Git tree 不一致: requested=$ASSESSMENT_E2E_TREE_SHA actual=$TREE_SHA"
fi

SOURCE_SHA256=$(calculate_source_sha256 "$SNAPSHOT_DIR")
case "$SOURCE_SHA256" in
  ''|*[!0-9a-f]*) fail '评估界面源码摘要不是合法 SHA-256' ;;
esac
[ "${#SOURCE_SHA256}" -eq 64 ] || fail '评估界面源码摘要不是 64 位 SHA-256'
if [ "${ASSESSMENT_E2E_SOURCE_SHA256+x}" = x ] && \
  [ "$ASSESSMENT_E2E_SOURCE_SHA256" != "$SOURCE_SHA256" ]; then
  fail 'ASSESSMENT_E2E_SOURCE_SHA256 与实际受审计源码不一致'
fi
chmod -R a-w "$WORK_DIR"

case "$REQUESTED_PORT" in
  ''|*[!0-9]*) fail 'ASSESSMENT_E2E_PORT 必须是 0（自动）或 1 到 65535 的整数' ;;
esac
if [ "$REQUESTED_PORT" -lt 0 ] || [ "$REQUESTED_PORT" -gt 65535 ]; then
  fail 'ASSESSMENT_E2E_PORT 必须是 0（自动）或 1 到 65535 的整数'
fi

FIXTURE_TOKEN=$(generate_fixture_token)
RUN_ID=$FIXTURE_TOKEN
FIXTURE_NAME="ai-gdm-assessment-fixture-$RUN_ID"
BROWSER_NAME="ai-gdm-assessment-playwright-$RUN_ID"
AUDIT_NAME="ai-gdm-assessment-audit-$RUN_ID"
BROWSER_IMAGE=${ASSESSMENT_BROWSER_IMAGE:-ai-gdm-assessment-browser:$TREE_SHA}

docker build --pull=false --build-arg "NPM_REGISTRY=$NPM_REGISTRY" \
  --build-arg "SOURCE_TREE_SHA=$TREE_SHA" -t "$BROWSER_IMAGE" \
  -f "$SNAPSHOT_DIR/tests/assessment-e2e/Dockerfile" "$SNAPSHOT_DIR/tests/assessment-e2e"

sh "$SNAPSHOT_DIR/scripts/run-validation-container.sh" -d --name "$FIXTURE_NAME" --network host \
  -v "$SNAPSHOT_DIR:/src:ro" -v "$RUNTIME_DIR:/runtime" -w /src -e "GOPROXY=$GO_PROXY" \
  -e "E2E_ADDR=127.0.0.1:$REQUESTED_PORT" -e "E2E_FIXTURE_TOKEN=$FIXTURE_TOKEN" \
  -e "E2E_TREE_SHA=$TREE_SHA" -e "E2E_RUNTIME_FILE=/runtime/address" \
  "$GO_IMAGE" sh -c 'go run ./tests/assessment-e2e/fixture'

attempt=0
until [ -s "$RUNTIME_FILE" ]; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    docker logs "$FIXTURE_NAME" || true
    fail '评估界面浏览器 fixture 未发布独占端口'
  fi
  if [ "$(docker inspect -f '{{.State.Running}}' "$FIXTURE_NAME" 2>/dev/null || true)" != true ]; then
    docker logs "$FIXTURE_NAME" || true
    fail '评估界面浏览器 fixture 启动失败'
  fi
  sleep 1
done

FIXTURE_ADDRESS=$(tr -d '\r\n' <"$RUNTIME_FILE")
case "$FIXTURE_ADDRESS" in
  127.0.0.1:[0-9]*) PORT=${FIXTURE_ADDRESS##*:} ;;
  *) fail '评估界面浏览器 fixture 地址无效' ;;
esac
case "$PORT" in
  ''|*[!0-9]*) fail '评估界面浏览器 fixture 端口无效' ;;
esac

EXPECTED_HEALTH="ok:$FIXTURE_TOKEN:$TREE_SHA"
attempt=0
while :; do
  ACTUAL_HEALTH=$(curl -fsS "http://127.0.0.1:$PORT/__fixture/health" 2>/dev/null || true)
  [ "$ACTUAL_HEALTH" = "$EXPECTED_HEALTH" ] && break
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    docker logs "$FIXTURE_NAME" || true
    fail '评估界面浏览器 fixture 身份校验失败'
  fi
  sleep 1
done

BROWSER_STATUS=0
sh "$SNAPSHOT_DIR/scripts/run-validation-container.sh" --name "$BROWSER_NAME" \
  --network host --shm-size=1g -v "$RUNTIME_DIR:/runtime" \
  -e "E2E_BASE_URL=http://127.0.0.1:$PORT" -e E2E_CHROMIUM_PATH=/usr/bin/chromium \
  -e PLAYWRIGHT_JSON_OUTPUT_FILE=/runtime/playwright-results.json \
  "$BROWSER_IMAGE" npx playwright test --reporter=line,json || BROWSER_STATUS=$?

REPORT_STATUS=0
if [ ! -s "$RESULT_FILE" ]; then
  printf '%s\n' '评估界面 Playwright 未生成机器可读结果' >&2
  REPORT_STATUS=1
else
  sh "$SNAPSHOT_DIR/scripts/run-validation-container.sh" --name "$AUDIT_NAME" --network none \
    -v "$SNAPSHOT_DIR/tests/assessment-e2e:/audit:ro" -v "$RUNTIME_DIR:/runtime:ro" \
    "$BROWSER_IMAGE" node /audit/validate-results.mjs \
    /audit/expected-tests.json /runtime/playwright-results.json || REPORT_STATUS=$?
fi

verify_source_stability
if [ "$BROWSER_STATUS" -ne 0 ] || [ "$REPORT_STATUS" -ne 0 ]; then
  fail "评估界面 Chromium 门禁失败：playwright=$BROWSER_STATUS audit=$REPORT_STATUS"
fi

printf '%s\n' \
  "评估界面 Chromium 回归通过：mode=$SOURCE_MODE tree=$TREE_SHA source_sha256=$SOURCE_SHA256 port=$PORT"
