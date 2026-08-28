#!/usr/bin/env sh
set -eu

if [ "$(id -u)" -eq 0 ]; then
  printf '%s\n' '请以具有 sudo 权限的普通用户运行本回归脚本' >&2
  exit 1
fi

REPOSITORY=${VALIDATION_REPOSITORY:-https://github.com/Requim/AI-GDM.git}
REF=${VALIDATION_REF:-main}
WORKDIR=$(mktemp -d "${TMPDIR:-/tmp}/ai-gdm-ownership.XXXXXX")
CHECKOUT="$WORKDIR/repository"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT INT TERM

git clone --quiet "$REPOSITORY" "$CHECKOUT"
git -C "$CHECKOUT" checkout --quiet "$REF"

OWNER_ID=$(stat -c '%u' "$CHECKOUT")
if [ "$OWNER_ID" -ne "$(id -u)" ]; then
  printf '%s\n' '回归仓库不是当前普通用户所有' >&2
  exit 1
fi

sudo -n sh "$CHECKOUT/scripts/validate-map-ui.sh"
printf '%s\n' "普通用户工作区 + sudo 容器验证通过：$REF"
