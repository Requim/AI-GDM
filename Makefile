GO ?= go

.PHONY: fmt-check build test vet check

fmt-check:
	@test -z "$$($$(dirname $$(command -v $(GO)))/gofmt -l $$(find . -path './.tools' -prune -o -path './.git' -prune -o -name '*.go' -type f -print))"

build:
	mkdir -p .tools/build
	$(GO) build -o .tools/build/ ./...

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

check: fmt-check build test vet
