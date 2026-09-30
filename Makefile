# CyberStrikeAI — 开发/CI 入口。
# 此前仓库没有 Makefile、没有 golangci-lint、没有架构约束检查、没有 -race 门禁，
# 解耦工作因此缺少安全网。目标形态的所有阶段都以这里的门禁为验收条件。

GO      ?= go
BIN     := cyberstrike-ai
PKG     := ./...
LDFLAGS ?=

# 固定工具版本：门禁必须可复现，否则"绿"没有意义。
GOLANGCI_VERSION := v2.3.0
ARCHLINT_VERSION := v0.2.35

.PHONY: all
all: build

## ---------------------------------------------------------------------------
## 构建
## ---------------------------------------------------------------------------

.PHONY: build
build:
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/server

.PHONY: build-stdio
build-stdio:
	$(GO) build -o cyberstrike-mcp-stdio ./cmd/mcp-stdio

.PHONY: generate
generate:
	$(GO) generate $(PKG)

## ---------------------------------------------------------------------------
## 测试：-race 是门禁，不是可选项
## ---------------------------------------------------------------------------

.PHONY: test
test:
	$(GO) test -count=1 $(PKG)

.PHONY: test-race
test-race:
	$(GO) test -race -count=1 $(PKG)

## ---------------------------------------------------------------------------
## 静态检查与架构约束
## ---------------------------------------------------------------------------

.PHONY: vet
vet:
	$(GO) vet $(PKG)

.PHONY: fmt
fmt:
	gofmt -w cmd internal

## fmt-check 才是门禁。`gofmt -l cmd internal` 在重构起点上有 29 个上游遗留文件（P6 窄接口那轮
## 顺手把 internal/audit/resource_availability.go 重写规范后降到 28），全仓
## 重排会把无关改动混进解耦 diff，所以这里用 ratchet：既存债务可以留着，但不许再加，
## 降了要收紧。（这个数字是 `gofmt -l cmd internal | wc -l` 的输出，包含 cmd/ 两个。）
.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -l cmd internal | wc -l | tr -d ' '); \
	baseline=28; \
	if [ "$$unformatted" -gt "$$baseline" ]; then \
		echo "gofmt needed (baseline $$baseline, now $$unformatted):"; gofmt -l cmd internal; exit 1; \
	fi; \
	if [ "$$unformatted" -lt "$$baseline" ]; then \
		echo "gofmt debt down to $$unformatted; tighten the fmt-check baseline"; \
	fi; \
	echo "gofmt debt: $$unformatted / baseline $$baseline"

.PHONY: lint
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
	  echo "golangci-lint missing: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)"; exit 1; }
	golangci-lint run

## 架构约束当前为 warn：解耦期间允许违规存在，但不允许新增后无人知晓。
## go-arch-lint 通过后把 check-only 提为 CI 阻断，是 P6 的验收门之一。
.PHONY: arch-lint
arch-lint:
	@command -v go-arch-lint >/dev/null 2>&1 || { \
	  echo "go-arch-lint missing: go install github.com/fe3dback/go-arch-lint@$(ARCHLINT_VERSION)"; exit 1; }
	go-arch-lint check --arch-file .go-arch-lint.yml --exit-code 0 || \
	  echo "WARN: 架构依赖超出 .go-arch-lint.yml 声明的边界（见上表）"

## ---------------------------------------------------------------------------
## 门禁：CI 与本地提交前检查同一套内容
## ---------------------------------------------------------------------------

.PHONY: precommit
precommit: fmt-check vet test-race lint arch-lint

## ---------------------------------------------------------------------------
## 分层门禁：SDK 只能收在越来越少的包里
## ---------------------------------------------------------------------------

## 分层 ratchet：Eino 逐包基线只许降、新引入的包直接红；handler 体量与 setter 数同样只许降。
## 数字来自 `go test -count=1 -v -run TestEinoImportsOnlyShrink ./internal/layering/` 的日志。
.PHONY: layering-check
layering-check:
	$(GO) test -count=1 -run 'TestEinoImportsOnlyShrink|TestHandler|TestConcreteDBFieldsOnlyShrink' ./internal/layering/

.PHONY: wiring-check
wiring-check:
	$(GO) test -count=1 -run 'TestEveryAuditableHandlerIsAuditBound|TestBindAuditReachesTheSetter' ./internal/app/
	$(GO) test -count=1 -run 'TestNarrowedStorage|TestNarrowRejects' ./internal/handler/ ./internal/database/

.PHONY: ci
ci: fmt-check vet test-race lint arch-lint layering-check wiring-check
	$(GO) build $(PKG)

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: clean
clean:
	rm -f $(BIN) cyberstrike-mcp-stdio
