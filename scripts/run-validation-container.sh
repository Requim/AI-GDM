#!/usr/bin/env sh
set -eu

# 仅对当前容器进程声明只读源码挂载点可信，不修改宿主机或镜像内 Git 配置。
exec docker run --rm \
  -e GIT_CONFIG_COUNT=1 \
  -e GIT_CONFIG_KEY_0=safe.directory \
  -e GIT_CONFIG_VALUE_0=/src \
  "$@"
