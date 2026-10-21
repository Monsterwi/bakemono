.PHONY: build clean test test-race test-integration pressure-app universal all

VERSION?=0.1.0-dev
BIN=razor
DIR_SRC=./
DOCKER_CMD=docker

GO_ENV=CGO_ENABLED=0
BUILD_DATE?=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GO_FLAGS=-ldflags="-X main.Version=$(VERSION) -X main.BuildDate=$(BUILD_DATE) -extldflags -static"
GO=go
# Pin local Make targets and CI to the same maintained patch.
export GOTOOLCHAIN=go$(shell awk '$$1 == "go" { print $$2 }' go.mod)

build:
	@$(GO_ENV) $(GO) build $(GO_FLAGS) -o $(BIN) $(DIR_SRC)

universal:
	@GOOS=darwin GOARCH=amd64 $(GO_ENV) $(GO) build $(GO_FLAGS) -o ${BIN}_amd64 $(DIR_SRC)
	@GOOS=darwin GOARCH=arm64 $(GO_ENV) $(GO) build $(GO_FLAGS) -o ${BIN}_arm64 $(DIR_SRC)
	@lipo -create -output ${BIN} ${BIN}_amd64 ${BIN}_arm64
	@rm -f ${BIN}_amd64 ${BIN}_arm64

test:
	@$(GO) test ./... -count=1 -timeout 120s

test-race:
	@CGO_ENABLED=1 GOFLAGS=-race $(GO) test ./... -count=1 -timeout 180s

test-integration:
	@$(GO) test . -run '^TestBinaryCacheMissThenHit$$' -count=1 -timeout 120s

clean:
	@$(GO) clean ./...
	@rm -f $(BIN)

pressure-app:
	@$(GO_ENV) $(GO) build $(GO_FLAGS) -o pressure-test ./demo-app/pressure/
	@$(GO_ENV) $(GO) build $(GO_FLAGS) -o pressure-test-concurrent ./demo-app/pressure-concurrent-rw/
all: clean build
