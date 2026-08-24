#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

if [ -x "$ROOT/.tools/go/bin/go" ]; then
  GO="$ROOT/.tools/go/bin/go"
  GOFMT="$ROOT/.tools/go/bin/gofmt"
else
  GO="${GO:-go}"
  GOFMT="${GOFMT:-gofmt}"
fi

go_files=$(find . -path './.tools' -prune -o -path './.git' -prune -o -name '*.go' -type f -print)
unformatted=$($GOFMT -l $go_files)
if [ -n "$unformatted" ]; then
  printf '发现未格式化的 Go 文件：\n%s\n' "$unformatted" >&2
  exit 1
fi

mkdir -p .tools/build
$GO build -o .tools/build/ ./...
$GO test ./...
$GO vet ./...
