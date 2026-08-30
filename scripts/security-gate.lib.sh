#!/usr/bin/env sh

[ "${AI_GDM_SECURITY_GATE_LIBRARY:-}" = source ] || {
  printf '%s\n' '安全门禁库只能由验证脚本 source' >&2
  exit 64
}

security_fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

security_hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    value=$(sha256sum "$1") || security_fail "无法读取受审计文件: $1"
    printf '%s\n' "${value%% *}"
    return
  fi
  command -v openssl >/dev/null 2>&1 || security_fail '安全门禁要求 sha256sum 或 openssl'
  value=$(openssl dgst -sha256 "$1") || security_fail "无法读取受审计文件: $1"
  printf '%s\n' "${value##* }"
}

security_source_files() (
  base=$1
  cd "$base"
  [ -z "$(find . -path './.git' -prune -o -type l -print -quit)" ] || \
    security_fail '安全门禁源码不得包含符号链接'
  find . -path './.git' -prune -o -type f -print | sed 's#^\./##' | LC_ALL=C sort
)

security_source_sha256() (
  base=$1
  work=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-security-source.XXXXXX")
  trap 'rm -rf "$work"' EXIT INT TERM
  security_source_files "$base" >"$work/files"
  [ -s "$work/files" ] || security_fail '安全门禁源码清单为空'
  printf '%s\n' 'ai-gdm-security-source-v1' >"$work/records"
  while IFS= read -r path; do
    case "$path" in ''|*..*|/*|./*|*[!A-Za-z0-9_./-]*) security_fail "安全门禁源码路径无效: $path" ;; esac
    printf '%s  %s\n' "$(security_hash_file "$base/$path")" "$path" >>"$work/records"
  done <"$work/files"
  security_hash_file "$work/records"
)

security_is_git_root() {
  root=$1
  command -v git >/dev/null 2>&1 || return 1
  top=$(git -c "safe.directory=$root" -C "$root" rev-parse --show-toplevel 2>/dev/null) || return 1
  top=$(CDPATH= cd -- "$top" && pwd -P)
  [ "$top" = "$root" ]
}

security_current_tree() (
  root=$1
  work=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-security-index.XXXXXX")
  trap 'rm -rf "$work"' EXIT INT TERM
  index=$(git -c "safe.directory=$root" -C "$root" rev-parse --git-path index)
  case "$index" in /*) ;; *) index="$root/$index" ;; esac
  cp "$index" "$work/index"
  GIT_INDEX_FILE="$work/index" git -c "safe.directory=$root" -C "$root" write-tree
)

security_archive_tree() (
  root=$1
  command -v git >/dev/null 2>&1 || security_fail '安全归档 tree 校验要求 git'
  work=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-security-tree.XXXXXX")
  trap 'rm -rf "$work"' EXIT INT TERM
  git init -q "$work/repository"
  git --git-dir="$work/repository/.git" --work-tree="$root" add -f -A -- .
  git --git-dir="$work/repository/.git" write-tree
)

security_tree_source_sha256() (
  root=$1
  tree=$2
  work=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-security-tree-source.XXXXXX")
  trap 'rm -rf "$work"' EXIT INT TERM
  git -c "safe.directory=$root" -C "$root" archive --format=tar "$tree" | tar -xf - -C "$work"
  security_source_sha256 "$work"
)

security_validate_sha() {
  value=$1
  length=$2
  label=$3
  case "$value" in ''|*[!0-9a-f]*) security_fail "$label 必须是 $length 位小写十六进制" ;; esac
  [ "${#value}" -eq "$length" ] || security_fail "$label 必须是 $length 位小写十六进制"
}

security_validate_image_reference() {
  value=$1
  label=$2
  case "$value" in
    *@sha256:*) digest=${value##*@sha256:} ;;
    *) security_fail "$label 必须使用 sha256 镜像摘要" ;;
  esac
  security_validate_sha "$digest" 64 "$label 摘要"
}

security_verify_git_candidate() {
  root=$1
  git -c "safe.directory=$root" -C "$root" diff --quiet -- || security_fail '安全门禁拒绝未暂存源码漂移'
  [ -z "$(git -c "safe.directory=$root" -C "$root" ls-files --others --exclude-standard)" ] || \
    security_fail '安全门禁拒绝未跟踪源码漂移'
  security_reject_hidden_index "$root"
}

security_reject_hidden_index() {
  root=$1
  git -c "safe.directory=$root" -C "$root" ls-files -v | while IFS= read -r record; do
    flag=${record%% *}
    case "$flag" in S|[a-z]) security_fail '安全门禁拒绝 Git 隐藏索引标记' ;; esac
  done
}

security_copy_archive() {
  source=$1
  destination=$2
  mkdir -p "$destination"
  (cd "$source" && tar --exclude=.git -cf - .) | (cd "$destination" && tar -xf -)
}

security_materialize_repository() {
  snapshot=$1
  expected=$2
  git init -q "$snapshot"
  git -c "safe.directory=$snapshot" -C "$snapshot" add -f -A -- .
  actual=$(git -c "safe.directory=$snapshot" -C "$snapshot" write-tree)
  [ "$actual" = "$expected" ] || \
    security_fail "安全门禁快照 tree 不一致: expected=$expected actual=$actual"
}

security_validate_archive_inputs() {
  [ -n "${SECURITY_E2E_TREE_SHA:-}" ] || security_fail '归档模式必须传入 SECURITY_E2E_TREE_SHA'
  [ -n "${SECURITY_E2E_SOURCE_SHA256:-}" ] || security_fail '归档模式必须传入 SECURITY_E2E_SOURCE_SHA256'
  security_validate_sha "$SECURITY_E2E_TREE_SHA" 40 'SECURITY_E2E_TREE_SHA'
  security_validate_sha "$SECURITY_E2E_SOURCE_SHA256" 64 'SECURITY_E2E_SOURCE_SHA256'
}

security_prepare_snapshot() {
  root=$1
  snapshot=$2
  if security_is_git_root "$root"; then
    security_prepare_git_snapshot "$root" "$snapshot"
  else
    security_prepare_archive_snapshot "$root" "$snapshot"
  fi
  security_finish_snapshot "$snapshot"
}

security_prepare_git_snapshot() {
  root=$1
  snapshot=$2
  SECURITY_SOURCE_MODE=git
  security_verify_git_candidate "$root"
  SECURITY_TREE_SHA=$(security_current_tree "$root")
  security_validate_optional_tree
  mkdir -p "$snapshot"
  git -c "safe.directory=$root" -C "$root" archive --format=tar "$SECURITY_TREE_SHA" | tar -xf - -C "$snapshot"
}

security_prepare_archive_snapshot() {
  root=$1
  snapshot=$2
  SECURITY_SOURCE_MODE=archive
  [ ! -e "$root/.git" ] || security_fail '安全归档包含不可用的 Git 元数据'
  security_validate_archive_inputs
  SECURITY_TREE_SHA=$(security_archive_tree "$root")
  [ "$SECURITY_E2E_TREE_SHA" = "$SECURITY_TREE_SHA" ] || \
    security_fail "SECURITY_E2E_TREE_SHA 与实际 tree 不一致: requested=$SECURITY_E2E_TREE_SHA actual=$SECURITY_TREE_SHA"
  security_copy_archive "$root" "$snapshot"
}

security_validate_optional_tree() {
  security_validate_sha "$SECURITY_TREE_SHA" 40 '安全门禁 tree SHA'
  if [ -n "${SECURITY_E2E_TREE_SHA:-}" ]; then
    security_validate_sha "$SECURITY_E2E_TREE_SHA" 40 'SECURITY_E2E_TREE_SHA'
    [ "$SECURITY_E2E_TREE_SHA" = "$SECURITY_TREE_SHA" ] || \
      security_fail "SECURITY_E2E_TREE_SHA 与实际 tree 不一致: requested=$SECURITY_E2E_TREE_SHA actual=$SECURITY_TREE_SHA"
  fi
}

security_finish_snapshot() {
  snapshot=$1
  security_validate_sha "$SECURITY_TREE_SHA" 40 '安全门禁 tree SHA'
  SECURITY_SOURCE_SHA256=$(security_source_sha256 "$snapshot")
  security_validate_sha "$SECURITY_SOURCE_SHA256" 64 '安全门禁 source SHA-256'
  if [ -n "${SECURITY_E2E_SOURCE_SHA256:-}" ]; then
    security_validate_sha "$SECURITY_E2E_SOURCE_SHA256" 64 'SECURITY_E2E_SOURCE_SHA256'
    [ "$SECURITY_E2E_SOURCE_SHA256" = "$SECURITY_SOURCE_SHA256" ] || \
      security_fail 'SECURITY_E2E_SOURCE_SHA256 与实际受审计源码不一致'
  fi
  security_materialize_repository "$snapshot" "$SECURITY_TREE_SHA"
  chmod -R a-w "$snapshot"
  if [ "$SECURITY_SOURCE_MODE" = archive ]; then
    SECURITY_ORIGINAL_SHA256=$(security_source_sha256 "$ROOT")
  fi
}

security_use_prepared_snapshot() {
  root=$1
  [ -n "${SECURITY_E2E_TREE_SHA:-}" ] || security_fail '已准备快照缺少 SECURITY_E2E_TREE_SHA'
  [ -n "${SECURITY_E2E_SOURCE_SHA256:-}" ] || security_fail '已准备快照缺少 SECURITY_E2E_SOURCE_SHA256'
  security_validate_sha "$SECURITY_E2E_TREE_SHA" 40 'SECURITY_E2E_TREE_SHA'
  security_validate_sha "$SECURITY_E2E_SOURCE_SHA256" 64 'SECURITY_E2E_SOURCE_SHA256'
  security_is_git_root "$root" || security_fail '已准备安全快照缺少可验证 Git tree'
  security_verify_git_candidate "$root"
  SECURITY_SOURCE_MODE=prepared
  SECURITY_TREE_SHA=$(security_current_tree "$root")
  SECURITY_SOURCE_SHA256=$(security_source_sha256 "$root")
  [ "$SECURITY_TREE_SHA" = "$SECURITY_E2E_TREE_SHA" ] || security_fail '已准备安全快照 tree 不一致'
  [ "$SECURITY_SOURCE_SHA256" = "$SECURITY_E2E_SOURCE_SHA256" ] || security_fail '已准备安全快照 source 不一致'
  tree_source=$(security_tree_source_sha256 "$root" "$SECURITY_TREE_SHA")
  [ "$tree_source" = "$SECURITY_SOURCE_SHA256" ] || security_fail '已准备安全快照 source 无法由 tree 重建'
}

security_verify_stability() {
  root=$1
  snapshot=$2
  [ "$(security_source_sha256 "$snapshot")" = "$SECURITY_SOURCE_SHA256" ] || \
    security_fail '安全门禁只读快照发生漂移'
  [ "$(security_current_tree "$snapshot")" = "$SECURITY_TREE_SHA" ] || \
    security_fail '安全门禁快照 tree 运行中漂移'
  security_verify_original "$root"
}

security_verify_original() {
  root=$1
  case "$SECURITY_SOURCE_MODE" in
    git)
      security_verify_git_candidate "$root"
      [ "$(security_current_tree "$root")" = "$SECURITY_TREE_SHA" ] || security_fail '安全门禁 Git tree 运行中漂移'
      ;;
    archive)
      [ "$(security_source_sha256 "$root")" = "$SECURITY_ORIGINAL_SHA256" ] || \
        security_fail '安全门禁归档源码运行中漂移'
      [ "$(security_archive_tree "$root")" = "$SECURITY_TREE_SHA" ] || security_fail '安全门禁归档 tree 运行中漂移'
      ;;
    prepared)
      security_verify_git_candidate "$root"
      [ "$(security_tree_source_sha256 "$root" "$SECURITY_TREE_SHA")" = "$SECURITY_SOURCE_SHA256" ] || \
        security_fail '已准备安全快照运行中偏离 tree'
      ;;
    *) security_fail '安全门禁源码模式无效' ;;
  esac
}

security_random_token() {
  raw=$(LC_ALL=C od -An -N16 -tx1 </dev/urandom 2>/dev/null) || security_fail '安全门禁随机源读取失败'
  token=$(printf '%s' "$raw" | tr -d '[:space:]') || security_fail '安全门禁随机令牌生成失败'
  security_validate_sha "$token" 32 '安全门禁随机令牌'
  printf '%s\n' "$token"
}
