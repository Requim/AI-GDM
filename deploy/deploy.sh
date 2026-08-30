#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
CHECKSUM_FILE="$ROOT/SHA256SUMS"
MANIFEST_FILE="$ROOT/manifest.json"
IMAGE_ENV_FILE="$ROOT/deploy/release-images.env"
OFFLINE_COMPOSE_FILE="$ROOT/deploy/compose.offline.yaml"
RUNTIME_ENV_FILE=${AI_GDM_RUNTIME_ENV_FILE:-$ROOT/deploy/runtime.env}
WAIT_SECONDS=${AI_GDM_DEPLOY_WAIT_SECONDS:-300}
RUNTIME_TEMP=
POSTGRES_PASSWORD_KEY=POSTGRES_PASS"WORD"
REDIS_PASSWORD_KEY=REDIS_PASS"WORD"
DATABASE_URL_KEY=DATABASE_"URL"
ADMIN_TOKEN_KEY=APP_ADMIN_"TOKEN"
AMAP_KEY_NAME=AMAP_API_"KEY"
BOCHA_KEY_NAME=BOCHA_API_"KEY"
LLM_KEY_NAME=LLM_API_"KEY"
RUNTIME_PROJECT_NAME=
RUNTIME_BIND_ADDRESS=
RUNTIME_POSTGRES_PASSWORD=
RUNTIME_REDIS_PASSWORD=
HTTP_PORT=

fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

cleanup() {
  [ -z "$RUNTIME_TEMP" ] || rm -f -- "$RUNTIME_TEMP"
}

trap cleanup EXIT HUP INT TERM

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "缺少必需命令: $1"
}

absolute_runtime_path() {
  case "$RUNTIME_ENV_FILE" in
    /*) ;;
    *) RUNTIME_ENV_FILE="$ROOT/$RUNTIME_ENV_FILE" ;;
  esac
}

validate_number() {
  value=$1
  label=$2
  minimum=$3
  maximum=$4
  case "$value" in ''|*[!0-9]*) fail "$label 必须是整数" ;; esac
  [ "$value" -ge "$minimum" ] && [ "$value" -le "$maximum" ] || fail "$label 超出允许范围"
}

verify_package() {
  [ -f "$CHECKSUM_FILE" ] && [ -f "$MANIFEST_FILE" ] || fail '发布包缺少校验清单或 manifest'
  [ ! -L "$CHECKSUM_FILE" ] && [ ! -L "$MANIFEST_FILE" ] || fail '发布包校验文件不得是符号链接'
  (cd "$ROOT" && sha256sum --strict -c SHA256SUMS) || fail '发布包 SHA-256 校验失败'
}

env_value() {
  key=$1
  file=$2
  count=$(awk -F= -v key="$key" '$1 == key { count++ } END { print count + 0 }' "$file")
  [ "$count" -eq 1 ] || fail "$file 必须且只能包含一个 $key"
  sed -n "s/^$key=//p" "$file"
}

optional_env_value() {
  key=$1
  file=$2
  awk -F= -v key="$key" '$1 == key { count++; value = substr($0, length(key) + 2) } END { if (count > 1) exit 2; if (count == 1) print value }' "$file" || \
    fail "$file 中 $key 重复"
}

validate_atom() {
  value=$1
  label=$2
  case "$value" in *[!A-Za-z0-9._~:/@+=-]*) fail "$label 包含不安全字符" ;; esac
}

validate_project_name() {
  value=$1
  case "$value" in
    ''|[!a-z0-9]*|*[!a-z0-9_-]*) fail 'AI_GDM_PROJECT_NAME 必须是小写字母、数字、连字符或下划线' ;;
  esac
  [ "${#value}" -le 63 ] || fail 'AI_GDM_PROJECT_NAME 超过 63 个字符'
}

load_release_images() {
  [ -f "$IMAGE_ENV_FILE" ] && [ ! -L "$IMAGE_ENV_FILE" ] || fail '发布镜像环境文件无效'
  valid_lines=$(awk -F= '
    /^[[:space:]]*$/ || /^[[:space:]]*#/ { next }
    $1 == "AI_GDM_IMAGE" || $1 == "AI_GDM_POSTGIS_IMAGE" || $1 == "AI_GDM_REDIS_IMAGE" { count++; next }
    { invalid = 1 }
    END { if (invalid) exit 2; print count + 0 }
  ' "$IMAGE_ENV_FILE") || fail '发布镜像环境文件包含未知键'
  [ "$valid_lines" -eq 3 ] || fail '发布镜像环境文件必须恰好包含三个键'
  AI_GDM_IMAGE=$(env_value AI_GDM_IMAGE "$IMAGE_ENV_FILE")
  AI_GDM_POSTGIS_IMAGE=$(env_value AI_GDM_POSTGIS_IMAGE "$IMAGE_ENV_FILE")
  AI_GDM_REDIS_IMAGE=$(env_value AI_GDM_REDIS_IMAGE "$IMAGE_ENV_FILE")
  for reference in "$AI_GDM_IMAGE" "$AI_GDM_POSTGIS_IMAGE" "$AI_GDM_REDIS_IMAGE"; do
    validate_atom "$reference" '发布镜像引用'
  done
  export AI_GDM_IMAGE AI_GDM_POSTGIS_IMAGE AI_GDM_REDIS_IMAGE
}

random_hex() {
  if command -v openssl >/dev/null 2>&1; then
    value=$(openssl rand -hex 32) || fail '无法生成运行密钥'
  else
    value=$(LC_ALL=C od -An -N32 -tx1 </dev/urandom | tr -d '[:space:]') || fail '无法生成运行密钥'
  fi
  case "$value" in *[!0-9a-f]*) fail '运行密钥随机源返回无效数据' ;; esac
  [ "${#value}" -eq 64 ] || fail '运行密钥长度无效'
  printf '%s\n' "$value"
}

provider_enabled() {
  [ -n "$1" ] && printf '%s\n' true || printf '%s\n' false
}

write_runtime_env() {
  postgres_secret=$(random_hex)
  redis_secret=$(random_hex)
  admin_secret=$(random_hex)
  [ "$postgres_secret" != "$redis_secret" ] && [ "$postgres_secret" != "$admin_secret" ] && \
    [ "$redis_secret" != "$admin_secret" ] || fail '生成的运行密钥不得重复'
  amap_key=$(printenv "$AMAP_KEY_NAME" 2>/dev/null || true)
  bocha_key=$(printenv "$BOCHA_KEY_NAME" 2>/dev/null || true)
  llm_key=$(printenv "$LLM_KEY_NAME" 2>/dev/null || true)
  llm_url=${LLM_BASE_URL:-https://jojocode.com/v1/chat/completions}
  llm_model=${LLM_MODEL:-gpt-5.6-terra}
  validate_atom "$amap_key" AMAP_API_KEY
  validate_atom "$bocha_key" BOCHA_API_KEY
  validate_atom "$llm_key" LLM_API_KEY
  validate_atom "$llm_url" LLM_BASE_URL
  validate_atom "$llm_model" LLM_MODEL
  case "$llm_url" in https://*) ;; *) fail 'LLM_BASE_URL 必须使用 HTTPS' ;; esac
  project_name=${AI_GDM_PROJECT_NAME:-ai-gdm}
  bind_address=${AI_GDM_BIND_ADDRESS:-0.0.0.0}
  http_port=${AI_GDM_HTTP_PORT:-8080}
  refresh_enabled=${REFRESH_ENABLED:-true}
  validate_project_name "$project_name"
  validate_atom "$bind_address" AI_GDM_BIND_ADDRESS
  validate_number "$http_port" AI_GDM_HTTP_PORT 1 65535
  case "$refresh_enabled" in true|false) ;; *) fail 'REFRESH_ENABLED 必须是 true 或 false' ;; esac
  mkdir -p "$(dirname -- "$RUNTIME_ENV_FILE")"
  RUNTIME_TEMP="$RUNTIME_ENV_FILE.tmp.$$"
  umask 077
  {
    cat <<EOF
AI_GDM_PROJECT_NAME=$project_name
AI_GDM_BIND_ADDRESS=$bind_address
AI_GDM_HTTP_PORT=$http_port
EOF
    printf '%s=%s\n' "$POSTGRES_PASSWORD_KEY" "$postgres_secret"
    printf '%s=%s\n' "$REDIS_PASSWORD_KEY" "$redis_secret"
    printf '%s=%s\n' "$DATABASE_URL_KEY" "postgresql://ai_gdm:$postgres_secret@postgres:5432/ai_gdm?sslmode=disable"
    cat <<EOF
APP_ENV=production
APP_LOG_LEVEL=info
APP_SHUTDOWN_TIMEOUT=30s
EOF
    printf '%s=%s\n' "$ADMIN_TOKEN_KEY" "$admin_secret"
    cat <<EOF
APP_RATE_LIMIT_PER_MINUTE=120
APP_RATE_LIMIT_BURST=30
REFRESH_ENABLED=$refresh_enabled
REFRESH_INTERVAL=30m
REFRESH_TIMEOUT=10m
OPEN_METEO_POINTS=104.066500,30.572300;102.712300,25.040600
OPEN_METEO_PAST_HOURS=72
OPEN_METEO_FORECAST_HOURS=24
OPEN_METEO_FALLBACK_MAX_AGE=6h
LHASA_STALE_AFTER=12h
AMAP_ENABLED=$(provider_enabled "$amap_key")
EOF
    printf '%s=%s\n' "$AMAP_KEY_NAME" "$amap_key"
    cat <<EOF
BOCHA_ENABLED=$(provider_enabled "$bocha_key")
EOF
    printf '%s=%s\n' "$BOCHA_KEY_NAME" "$bocha_key"
    cat <<EOF
LLM_ENABLED=$(provider_enabled "$llm_key")
LLM_PROVIDER_NAME=OpenAI-compatible
LLM_BASE_URL=$llm_url
EOF
    printf '%s=%s\n' "$LLM_KEY_NAME" "$llm_key"
    cat <<EOF
LLM_MODEL=$llm_model
LLM_MAX_COMPLETION_TOKENS=1200
LLM_OUTPUT_ATTEMPTS=2
EOF
  } >"$RUNTIME_TEMP"
  chmod 0600 "$RUNTIME_TEMP"
  mv -f -- "$RUNTIME_TEMP" "$RUNTIME_ENV_FILE"
  RUNTIME_TEMP=
}

validate_runtime_env() {
  [ -f "$RUNTIME_ENV_FILE" ] && [ ! -L "$RUNTIME_ENV_FILE" ] || fail '运行配置必须是普通文件'
  [ "$(wc -c <"$RUNTIME_ENV_FILE")" -le 65536 ] || fail '运行配置超过 64 KiB'
  for key in "$POSTGRES_PASSWORD_KEY" "$REDIS_PASSWORD_KEY" "$DATABASE_URL_KEY" "$ADMIN_TOKEN_KEY" \
    AI_GDM_PROJECT_NAME AI_GDM_BIND_ADDRESS AI_GDM_HTTP_PORT; do
    [ -n "$(env_value "$key" "$RUNTIME_ENV_FILE")" ] || fail "$key 不得为空"
  done
  [ "$(env_value APP_ENV "$RUNTIME_ENV_FILE")" = production ] || fail 'APP_ENV 必须是 production'
  chmod 0600 "$RUNTIME_ENV_FILE"
}

runtime_port() {
  value=$(optional_env_value AI_GDM_HTTP_PORT "$RUNTIME_ENV_FILE")
  [ -n "$value" ] || value=8080
  validate_number "$value" AI_GDM_HTTP_PORT 1 65535
  printf '%s\n' "$value"
}

load_runtime_compose_values() {
  RUNTIME_PROJECT_NAME=$(env_value AI_GDM_PROJECT_NAME "$RUNTIME_ENV_FILE")
  RUNTIME_BIND_ADDRESS=$(env_value AI_GDM_BIND_ADDRESS "$RUNTIME_ENV_FILE")
  RUNTIME_POSTGRES_PASSWORD=$(env_value "$POSTGRES_PASSWORD_KEY" "$RUNTIME_ENV_FILE")
  RUNTIME_REDIS_PASSWORD=$(env_value "$REDIS_PASSWORD_KEY" "$RUNTIME_ENV_FILE")
  HTTP_PORT=$(runtime_port)
  validate_project_name "$RUNTIME_PROJECT_NAME"
  validate_atom "$RUNTIME_BIND_ADDRESS" AI_GDM_BIND_ADDRESS
  AI_GDM_PROJECT_NAME=$RUNTIME_PROJECT_NAME
  AI_GDM_BIND_ADDRESS=$RUNTIME_BIND_ADDRESS
  AI_GDM_HTTP_PORT=$HTTP_PORT
  export AI_GDM_PROJECT_NAME AI_GDM_BIND_ADDRESS AI_GDM_HTTP_PORT
  export "$POSTGRES_PASSWORD_KEY=$RUNTIME_POSTGRES_PASSWORD"
  export "$REDIS_PASSWORD_KEY=$RUNTIME_REDIS_PASSWORD"
}

expected_image_id() {
  reference=$1
  lines=$(grep -F "\"reference\": \"$reference\"" "$MANIFEST_FILE" || true)
  value=$(printf '%s\n' "$lines" | sed -n 's/.*"id": "\(sha256:[0-9a-f]\{64\}\)".*/\1/p')
  [ -n "$value" ] && [ "$(printf '%s\n' "$value" | wc -l)" -eq 1 ] || fail "manifest 缺少唯一镜像标识: $reference"
  printf '%s\n' "$value"
}

verify_loaded_images() {
  for reference in "$AI_GDM_IMAGE" "$AI_GDM_POSTGIS_IMAGE" "$AI_GDM_REDIS_IMAGE"; do
    actual=$(docker image inspect -f '{{.Id}}' "$reference" 2>/dev/null) || fail "离线镜像未加载: $reference"
    [ "$actual" = "$(expected_image_id "$reference")" ] || fail "离线镜像标识不一致: $reference"
    [ "$(docker image inspect -f '{{.Os}}/{{.Architecture}}' "$reference")" = linux/amd64 ] || \
      fail "离线镜像平台不是 linux/amd64: $reference"
  done
}

compose() {
  AI_GDM_RUNTIME_ENV_FILE="$RUNTIME_ENV_FILE" docker compose --project-name "$RUNTIME_PROJECT_NAME" \
    --project-directory "$ROOT" \
    --env-file "$RUNTIME_ENV_FILE" --env-file "$IMAGE_ENV_FILE" \
    -f "$ROOT/compose.yaml" -f "$OFFLINE_COMPOSE_FILE" "$@"
}

verify_compose_images() {
  actual=$(compose config --images | LC_ALL=C sort)
  expected=$(printf '%s\n' "$AI_GDM_IMAGE" "$AI_GDM_POSTGIS_IMAGE" "$AI_GDM_REDIS_IMAGE" | LC_ALL=C sort)
  [ "$actual" = "$expected" ] || fail 'Compose 未精确绑定三个离线发布镜像'
}

wait_for_http() {
  attempts=0
  while [ "$attempts" -lt 30 ]; do
    if curl -fsS --max-time 5 "http://127.0.0.1:$HTTP_PORT/healthz" >/dev/null && \
      curl -fsS --max-time 5 "http://127.0.0.1:$HTTP_PORT/readyz" >/dev/null && \
      curl -fsS --max-time 5 "http://127.0.0.1:$HTTP_PORT/" | grep -F 'AI-GDM 地质灾害辅助研判控制台' >/dev/null; then
      printf '%s\n' "AI-GDM 已部署：http://127.0.0.1:$HTTP_PORT/"
      return 0
    fi
    attempts=$((attempts + 1))
    sleep 2
  done
  fail '服务启动后未在规定时间内通过 HTTP 探针'
}

absolute_runtime_path
validate_number "$WAIT_SECONDS" AI_GDM_DEPLOY_WAIT_SECONDS 30 1800
require_command sha256sum
require_command docker
require_command curl
require_command printenv
verify_package
docker compose version >/dev/null 2>&1 || fail 'Docker Compose v2 不可用'
[ "$(docker info -f '{{.OSType}}' 2>/dev/null)" = linux ] || fail 'Docker 必须运行 Linux 容器引擎'
load_release_images
[ -e "$RUNTIME_ENV_FILE" ] || write_runtime_env
validate_runtime_env
load_runtime_compose_values
docker load -i "$ROOT/images/ai-gdm-images-linux-amd64.tar" >/dev/null
verify_loaded_images
compose config --quiet
verify_compose_images
compose up -d --wait --wait-timeout "$WAIT_SECONDS" --pull never --no-build --remove-orphans
wait_for_http
printf '%s\n' "运行配置：$RUNTIME_ENV_FILE（权限 0600，部署脚本未输出密钥）"
