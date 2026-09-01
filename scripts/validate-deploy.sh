#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
AI_GDM_SECURITY_GATE_LIBRARY=source
export AI_GDM_SECURITY_GATE_LIBRARY
. "$ROOT/scripts/security-gate.lib.sh"

DIND_IMAGE=${DEPLOY_DIND_IMAGE:-docker:29-dind@sha256:12e683a161823b2a839aeea999b9d960e6e1f9a97b1679ad6b441982e2d9cf07}
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-deploy-validation.XXXXXX")
SNAPSHOT_DIR="$WORK_DIR/source"
OUTPUT_DIR="$WORK_DIR/dist"
PACKAGE_LOG="$WORK_DIR/package.log"
DEPLOY_LOG="$WORK_DIR/deploy.log"
RUNTIME_ENV_FILE="$WORK_DIR/runtime.env"
ACTIVE_CHILD_PID=
RUN_ID=
DIND_CONTAINER=
DIND_VOLUME=
INNER_DOCKER_HOST=
PACKAGE_DIR=
PACKAGE_ARCHIVE=
PACKAGE_ARCHIVE_INPUT=${DEPLOY_PACKAGE_ARCHIVE:-}
EXPECTED_SOURCE_COMMIT=${DEPLOY_EXPECTED_SOURCE_COMMIT:-}
PACKAGE_VERSION=
PACKAGE_TREE=
PACKAGE_SOURCE_SHA256=
PACKAGE_REVISION=
EXTRACT_ROOT="$WORK_DIR/extracted"
PROJECT=
POSTGRES_PASSWORD_KEY=POSTGRES_PASS"WORD"
REDIS_PASSWORD_KEY=REDIS_PASS"WORD"

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

manifest_value() {
  python3 -c 'import json,sys; value=json.load(open(sys.argv[1], encoding="utf-8"))[sys.argv[2]]; print(value)' \
    "$PACKAGE_DIR/manifest.json" "$1"
}

inner_docker() {
  DOCKER_HOST="$INNER_DOCKER_HOST" docker "$@"
}

inner_compose() {
  AI_GDM_RUNTIME_ENV_FILE="$RUNTIME_ENV_FILE" DOCKER_HOST="$INNER_DOCKER_HOST" docker compose \
    --project-name "$PROJECT" --project-directory "$PACKAGE_DIR" --env-file "$RUNTIME_ENV_FILE" \
    --env-file "$PACKAGE_DIR/deploy/release-images.env" \
    -f "$PACKAGE_DIR/compose.yaml" -f "$PACKAGE_DIR/deploy/compose.offline.yaml" "$@"
}

remove_owned_dind() {
  [ -n "$DIND_CONTAINER" ] && [ -n "$RUN_ID" ] || return 0
  owner=$(docker inspect -f '{{ index .Config.Labels "ai.gdm.deploy.validation.run" }}' "$DIND_CONTAINER" 2>/dev/null || true)
  [ "$owner" = "$RUN_ID" ] || return 0
  docker rm -fv "$DIND_CONTAINER" >/dev/null 2>&1 || true
}

remove_owned_volume() {
  [ -n "$DIND_VOLUME" ] && [ -n "$RUN_ID" ] || return 0
  owner=$(docker volume inspect -f '{{ index .Labels "ai.gdm.deploy.validation.run" }}' "$DIND_VOLUME" 2>/dev/null || true)
  [ "$owner" = "$RUN_ID" ] || return 0
  docker volume rm "$DIND_VOLUME" >/dev/null 2>&1 || true
}

cleanup() {
  stop_active_child
  if [ -n "$INNER_DOCKER_HOST" ] && [ -n "$PACKAGE_DIR" ] && [ -s "$RUNTIME_ENV_FILE" ]; then
    inner_compose down -v --remove-orphans --timeout 15 >/dev/null 2>&1 || true
  fi
  remove_owned_dind
  remove_owned_volume
  chmod -R u+w "$WORK_DIR" 2>/dev/null || true
  rm -rf "$WORK_DIR"
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

choose_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

wait_for_dind() {
  attempts=0
  while [ "$attempts" -lt 60 ]; do
    inner_docker info >/dev/null 2>&1 && return 0
    attempts=$((attempts + 1))
    sleep 1
  done
  security_fail '隔离 Docker 守护进程未就绪'
}

parse_package_output() {
  if [ -n "$PACKAGE_ARCHIVE_INPUT" ]; then
    case "$PACKAGE_ARCHIVE_INPUT" in /*) ;; *) security_fail 'DEPLOY_PACKAGE_ARCHIVE 必须是绝对路径' ;; esac
    [ -f "$PACKAGE_ARCHIVE_INPUT" ] && [ ! -L "$PACKAGE_ARCHIVE_INPUT" ] || \
      security_fail '待验部署归档必须是普通文件'
    PACKAGE_ARCHIVE=$PACKAGE_ARCHIVE_INPUT
  else
    PACKAGE_ARCHIVE=$(sed -n 's/^PACKAGE_ARCHIVE=//p' "$PACKAGE_LOG")
  fi
  [ -f "$PACKAGE_ARCHIVE" ] && [ ! -L "$PACKAGE_ARCHIVE" ] && \
    [ -f "$PACKAGE_ARCHIVE.sha256" ] && [ ! -L "$PACKAGE_ARCHIVE.sha256" ] || \
    security_fail 'P10.3 未生成发布归档及外层校验文件'
  archive_name=$(basename -- "$PACKAGE_ARCHIVE")
  package_name=${archive_name%.tar.gz}
  [ "$package_name" != "$archive_name" ] || security_fail '发布归档名称无效'
  python3 "$ROOT/scripts/validate-release-archive.py" \
    --archive "$PACKAGE_ARCHIVE" --sidecar "$PACKAGE_ARCHIVE.sha256" \
    --expected-root "$package_name" --extract-root "$EXTRACT_ROOT"
  PACKAGE_DIR="$EXTRACT_ROOT/$package_name"
  [ -d "$PACKAGE_DIR" ] && [ -x "$PACKAGE_DIR/deploy/deploy.sh" ] || \
    security_fail '解包后 Shell 一键部署入口无效'
  (cd "$PACKAGE_DIR" && sha256sum --strict -c SHA256SUMS) >/dev/null || \
    security_fail '解包后包内 SHA-256 无效'
  PACKAGE_VERSION=$(manifest_value version)
  [ "$archive_name" = "ai-gdm-$PACKAGE_VERSION-linux-amd64.tar.gz" ] || \
    security_fail '部署归档名称与 manifest 版本不一致'
  PACKAGE_TREE=$(manifest_value sourceTree)
  PACKAGE_SOURCE_SHA256=$(manifest_value sourceSha256)
  source_commit=$(manifest_value sourceCommit)
  [ "$PACKAGE_TREE" = "$SECURITY_TREE_SHA" ] || security_fail '部署归档 sourceTree 与当前固定源码不一致'
  PACKAGE_REVISION=$PACKAGE_TREE
  [ "$source_commit" = unknown ] || PACKAGE_REVISION=$source_commit
  if [ -n "$PACKAGE_ARCHIVE_INPUT" ]; then
    security_validate_sha "$EXPECTED_SOURCE_COMMIT" 40 'DEPLOY_EXPECTED_SOURCE_COMMIT'
    [ "$source_commit" = "$EXPECTED_SOURCE_COMMIT" ] || security_fail '部署归档 sourceCommit 与预期不一致'
    expected_tree=$(git -c "safe.directory=$ROOT" -C "$ROOT" rev-parse "$EXPECTED_SOURCE_COMMIT^{tree}" 2>/dev/null || true)
    [ "$PACKAGE_TREE" = "$expected_tree" ] || security_fail '部署归档 sourceTree 与预期提交不一致'
    expected_source_sha256=$(security_tree_source_sha256 "$ROOT" "$expected_tree")
    [ "$PACKAGE_SOURCE_SHA256" = "$expected_source_sha256" ] || \
      security_fail '部署归档 sourceSha256 与预期提交不一致'
  fi
  assert_equal "$(sed -n 's/^AI_GDM_IMAGE=//p' "$PACKAGE_DIR/deploy/release-images.env")" \
    "ai-gdm/server:$PACKAGE_VERSION" '应用镜像发布标签'
  assert_equal "$(sed -n 's/^AI_GDM_POSTGIS_IMAGE=//p' "$PACKAGE_DIR/deploy/release-images.env")" \
    "ai-gdm/postgis:17-3.5-$PACKAGE_VERSION" 'PostGIS 镜像发布标签'
  assert_equal "$(sed -n 's/^AI_GDM_REDIS_IMAGE=//p' "$PACKAGE_DIR/deploy/release-images.env")" \
    "ai-gdm/redis:7.4.10-$PACKAGE_VERSION" 'Redis 镜像发布标签'
}

run_deployment() {
  if [ -f "$RUNTIME_ENV_FILE" ]; then
    env "$POSTGRES_PASSWORD_KEY=ambient-invalid" "$REDIS_PASSWORD_KEY=ambient-invalid" \
      COMPOSE_PROJECT_NAME=ambient-invalid AI_GDM_PROJECT_NAME=ambient-invalid \
      AI_GDM_BIND_ADDRESS=127.0.0.2 AI_GDM_HTTP_PORT=1 \
      DOCKER_HOST="$INNER_DOCKER_HOST" AI_GDM_RUNTIME_ENV_FILE="$RUNTIME_ENV_FILE" \
      AI_GDM_DEPLOY_WAIT_SECONDS=300 sh "$PACKAGE_DIR/deploy/deploy.sh" >>"$DEPLOY_LOG" 2>&1
    return
  fi
  DOCKER_HOST="$INNER_DOCKER_HOST" AI_GDM_RUNTIME_ENV_FILE="$RUNTIME_ENV_FILE" \
    AI_GDM_PROJECT_NAME="$PROJECT" AI_GDM_BIND_ADDRESS=0.0.0.0 AI_GDM_HTTP_PORT="$PUBLIC_PORT" \
    REFRESH_ENABLED=false AI_GDM_DEPLOY_WAIT_SECONDS=300 \
    sh "$PACKAGE_DIR/deploy/deploy.sh" >>"$DEPLOY_LOG" 2>&1
}

assert_equal() {
  actual=$1
  expected=$2
  label=$3
  [ "$actual" = "$expected" ] || security_fail "$label 无效: actual=$actual expected=$expected"
}

service_id() {
  value=$(inner_compose ps -q "$1")
  case "$value" in ''|*[!0-9a-f]*) security_fail "$1 容器 ID 无效" ;; esac
  printf '%s\n' "$value"
}

verify_images() {
  assert_equal "$(inner_docker image ls -q | sort -u | wc -l | tr -d '[:space:]')" 3 '空缓存加载后的镜像数量'
  actual=$(inner_docker image ls --format '{{.Repository}}:{{.Tag}}' | grep '^ai-gdm/' | LC_ALL=C sort)
  expected=$(sed -n 's/^AI_GDM_[A-Z_]*=//p' "$PACKAGE_DIR/deploy/release-images.env" | LC_ALL=C sort)
  assert_equal "$actual" "$expected" '离线镜像标签集合'
}

verify_runtime_contracts() {
  app_id=$(service_id app)
  postgres_id=$(service_id postgres)
  redis_id=$(service_id redis)
  assert_equal "$(inner_docker exec "$app_id" id -u)" 10001 '应用 UID'
  assert_equal "$(inner_docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' "$app_id")" true '应用只读根文件系统'
  assert_equal "$(inner_docker inspect -f '{{ index .Config.Labels "org.opencontainers.image.version" }}' "$app_id")" \
    "$PACKAGE_VERSION" '应用 OCI 版本'
  assert_equal "$(inner_docker inspect -f '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$app_id")" \
    "$PACKAGE_REVISION" '应用 OCI revision'
  inner_docker exec "$app_id" /usr/bin/gdal --version | grep -F 'GDAL 3.13.3' >/dev/null || security_fail 'GDAL 版本无效'
  [ -z "$(inner_docker port "$postgres_id")" ] || security_fail 'PostgreSQL 不得映射端口'
  [ -z "$(inner_docker port "$redis_id")" ] || security_fail 'Redis 不得映射端口'
  inner_compose exec -T postgres psql -U ai_gdm -d ai_gdm -Atc \
    "SELECT extversion FROM pg_extension WHERE extname='postgis'" | grep -E '^3\.5([.]|$)' >/dev/null || security_fail 'PostGIS 版本无效'
  assert_equal "$(inner_compose exec -T postgres psql -U ai_gdm -d ai_gdm -Atc 'SELECT count(*) FROM schema_migrations')" 11 '数据库迁移数量'
  assert_equal "$(inner_compose exec -T redis sh -c 'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli ping')" PONG 'Redis 探针'
}

write_markers() {
  inner_compose exec -T postgres psql -U ai_gdm -d ai_gdm -v ON_ERROR_STOP=1 -c \
    "CREATE TABLE p103_validation(value text PRIMARY KEY); INSERT INTO p103_validation(value) VALUES ('$RUN_ID');" >/dev/null
  inner_compose exec -T -e "P10_RUN_ID=$RUN_ID" redis \
    sh -c 'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli SET p103_validation "$P10_RUN_ID"' >/dev/null
  inner_compose exec -T -e "P10_RUN_ID=$RUN_ID" app \
    sh -c 'printf "%s" "$P10_RUN_ID" > /var/lib/ai-gdm/lhasa/p103-validation' >/dev/null
}

verify_markers() {
  assert_equal "$(inner_compose exec -T postgres psql -U ai_gdm -d ai_gdm -Atc 'SELECT value FROM p103_validation')" "$RUN_ID" 'PostgreSQL 持久化'
  assert_equal "$(inner_compose exec -T redis sh -c 'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli GET p103_validation')" "$RUN_ID" 'Redis 持久化'
  assert_equal "$(inner_compose exec -T app cat /var/lib/ai-gdm/lhasa/p103-validation)" "$RUN_ID" 'LHASA 持久化'
}

verify_http() {
  curl -fsS --max-time 5 "http://127.0.0.1:$PUBLIC_PORT/healthz" | grep -F '"status":"ok"' >/dev/null || security_fail '部署存活探针失败'
  curl -fsS --max-time 5 "http://127.0.0.1:$PUBLIC_PORT/readyz" | grep -F '"status":"ready"' >/dev/null || security_fail '部署就绪探针失败'
  curl -fsS --max-time 5 "http://127.0.0.1:$PUBLIC_PORT/" | grep -F 'AI-GDM 地质灾害辅助研判控制台' >/dev/null || security_fail '部署 Web 控制台失败'
}

verify_secret_logs() {
  logs=$(inner_compose logs --no-color 2>&1)
  deployment_logs=$(cat "$DEPLOY_LOG")
  for key in POSTGRES_PASSWORD REDIS_PASSWORD APP_ADMIN_TOKEN; do
    secret=$(sed -n "s/^$key=//p" "$RUNTIME_ENV_FILE")
    [ -n "$secret" ] || security_fail "$key 缺失"
    printf '%s' "$logs$deployment_logs" | grep -F "$secret" >/dev/null && security_fail '部署日志泄露运行密钥'
  done
  return 0
}

verify_no_owned_resources() {
  [ -z "$(docker ps -aq --filter "label=ai.gdm.deploy.validation.run=$RUN_ID")" ] || security_fail 'P10.3 留下 DIND 容器'
  [ -z "$(docker volume ls -q --filter "label=ai.gdm.deploy.validation.run=$RUN_ID")" ] || security_fail 'P10.3 留下 DIND 数据卷'
}

security_validate_image_reference "$DIND_IMAGE" DEPLOY_DIND_IMAGE
security_prepare_snapshot "$ROOT" "$SNAPSHOT_DIR"
RUN_ID=$(security_random_token)
PROJECT="ai-gdm-p103-$RUN_ID"
DIND_CONTAINER="ai-gdm-p103-$RUN_ID-dind"
DIND_VOLUME="ai-gdm-p103-$RUN_ID-docker"
PUBLIC_PORT=$(choose_port)
DAEMON_PORT=$(choose_port)
[ "$PUBLIC_PORT" != "$DAEMON_PORT" ] || security_fail 'P10.3 随机端口冲突'

if [ -z "$PACKAGE_ARCHIVE_INPUT" ]; then
  PACKAGE_OUTPUT_DIR="$OUTPUT_DIR" PACKAGE_VERSION=v0.1.1 PACKAGE_PULL_IMAGES=1 \
    PACKAGE_REQUIRE_SOURCE_COMMIT=0 sh "$SNAPSHOT_DIR/scripts/package-release.sh" >"$PACKAGE_LOG" 2>&1 || {
      cat "$PACKAGE_LOG"
      exit 1
    }
  cat "$PACKAGE_LOG"
else
  [ -n "$EXPECTED_SOURCE_COMMIT" ] || security_fail '正式归档部署复核必须传入 DEPLOY_EXPECTED_SOURCE_COMMIT'
fi
parse_package_output

docker pull "$DIND_IMAGE" >/dev/null
docker volume create --label "ai.gdm.deploy.validation.run=$RUN_ID" "$DIND_VOLUME" >/dev/null
docker run -d --privileged --name "$DIND_CONTAINER" \
  --label "ai.gdm.deploy.validation.run=$RUN_ID" \
  -e DOCKER_TLS_CERTDIR= -v "$DIND_VOLUME:/var/lib/docker" \
  -p "127.0.0.1:$DAEMON_PORT:2375" -p "127.0.0.1:$PUBLIC_PORT:$PUBLIC_PORT" \
  "$DIND_IMAGE" --host=tcp://0.0.0.0:2375 --host=unix:///var/run/docker.sock >/dev/null
INNER_DOCKER_HOST="tcp://127.0.0.1:$DAEMON_PORT"
wait_for_dind
[ -z "$(inner_docker image ls -aq)" ] || security_fail '隔离 Docker 守护进程不是空镜像缓存'

run_active_command run_deployment || { cat "$DEPLOY_LOG"; exit 1; }
cat "$DEPLOY_LOG"
verify_images
verify_http
verify_runtime_contracts
write_markers
runtime_digest=$(security_hash_file "$RUNTIME_ENV_FILE")
run_active_command run_deployment || { cat "$DEPLOY_LOG"; exit 1; }
assert_equal "$(security_hash_file "$RUNTIME_ENV_FILE")" "$runtime_digest" '重复部署运行配置'
verify_markers
inner_compose down --remove-orphans --timeout 30 >/dev/null
run_active_command run_deployment || { cat "$DEPLOY_LOG"; exit 1; }
assert_equal "$(security_hash_file "$RUNTIME_ENV_FILE")" "$runtime_digest" '停启后的运行配置'
verify_http
verify_markers
verify_secret_logs
security_verify_stability "$ROOT" "$SNAPSHOT_DIR"
FINAL_PACKAGE=$(basename "$PACKAGE_DIR")
cleanup
verify_no_owned_resources
trap - EXIT HUP INT TERM

printf '%s\n' "P10.3 一键部署门禁通过：package=$FINAL_PACKAGE tree=$SECURITY_TREE_SHA source_sha256=$SECURITY_SOURCE_SHA256"
