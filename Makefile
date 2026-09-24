# aladdin —— 前后端与命令行一体仓库
#
# 所有代码生成都经由 `make gen`：不允许多个入口各自生成，
# 否则生成产物会漂移（见 docs/ssot-registry.md）。

MODULE      := github.com/poetlife/aladdin
BIN_DIR     := bin
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)

# 生成代码所需的工具。用 `go install` 固定版本，避免"我这能跑"。
TOOLS := \
	google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12 \
	google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest \
	connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.21.0 \
	github.com/bufbuild/buf/cmd/buf@v1.73.0

export PATH := $(shell go env GOPATH)/bin:$(PATH)

.DEFAULT_GOAL := help
.PHONY: help
help: ## 显示本帮助
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## ---------------------------------------------------------------- 依赖与生成

.PHONY: tools
tools: ## 安装代码生成工具
	@for t in $(TOOLS); do echo ">>> go install $$t"; go install $$t || exit 1; done

.PHONY: gen
gen: ## 由 proto 与权限目录生成两端代码（唯一生成入口）
	buf generate
	go run ./internal/tools/permissiongen

.PHONY: check-gen
check-gen: ## 校验生成产物与源定义同步（CI 用）
	buf generate
	go run ./internal/tools/permissiongen -check
	@# buf 生成是幂等的：重新生成后若 api/gen 或 web/src/gen 出现改动，
	@# 说明提交进去的生成产物与 proto 不同步（有人手改过，或忘了 make gen）。
	@if git rev-parse --git-dir >/dev/null 2>&1; then \
		if ! git diff --quiet -- api/gen web/src/gen; then \
			echo "生成产物与 proto 不同步，请运行 make gen 并提交结果："; \
			git diff --stat -- api/gen web/src/gen; \
			exit 1; \
		fi; \
		echo "生成产物与 proto 同步"; \
	fi

.PHONY: tidy
tidy: ## 整理依赖
	go mod tidy

## ---------------------------------------------------------------- 构建

.PHONY: build
build: ## 构建服务端与 CLI
	@mkdir -p $(BIN_DIR)
	go build -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/aladdin-server ./cmd/aladdin-server
	go build -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/aladdin ./cmd/aladdin

.PHONY: install
install: ## 安装到 GOPATH/bin
	go install -ldflags '$(LDFLAGS)' ./cmd/aladdin ./cmd/aladdin-server

.PHONY: web-install
web-install: ## 安装前端依赖
	cd web && npm install

.PHONY: web-build
web-build: ## 构建前端
	cd web && npm run build

## ---------------------------------------------------------------- 测试

.PHONY: test
test: ## 运行全部 Go 测试（含竞态检测与生成同步校验）
	go test -race ./...

.PHONY: test-web
test-web: ## 运行前端测试
	cd web && npm run test

.PHONY: test-e2e
test-e2e: ## 运行端到端测试（测试自带服务端，随机端口，可并行）
	go test -race -tags e2e ./test/e2e/...

.PHONY: cover
cover: ## 生成覆盖率报告
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: cover-html
cover-html: cover ## 生成可在浏览器查看的覆盖率报告
	go tool cover -html=coverage.out -o coverage.html
	@echo "已生成 coverage.html"

.PHONY: lint
lint: ## 静态检查
	@echo ">>> gofmt"
	@unformatted=$$(gofmt -l ./cmd ./internal ./pkg ./test | grep -v '^api/gen/' || true); \
		if [ -n "$$unformatted" ]; then echo "以下文件未格式化："; echo "$$unformatted"; exit 1; fi
	@echo ">>> go vet"; go vet ./...
	@echo ">>> buf lint"; buf lint
	@echo ">>> golangci-lint"; \
		if command -v golangci-lint >/dev/null; then golangci-lint run; \
		else echo "（未安装 golangci-lint，跳过；安装：brew install golangci-lint）"; fi

## ---------------------------------------------------------------- 运行

.PHONY: run
run: ## 启动服务端
	go run ./cmd/aladdin-server

.PHONY: dev
dev: ## 以开发种子数据启动服务端（本地联调用）
	ALADDIN_DEV_SEED=1 \
	ALADDIN_DEV_TOKEN=dev-token \
	ALADDIN_DEV_SUBJECT=dev-user \
	ALADDIN_DEV_ROLE=system.admin \
	ALADDIN_DEV_SCOPE=tenant/acme \
	ALADDIN_LOG_LEVEL=debug \
	go run ./cmd/aladdin-server

.PHONY: web-dev
web-dev: ## 启动前端开发服务器
	cd web && npm run dev

.PHONY: clean
clean: ## 清理构建产物
	rm -rf $(BIN_DIR) coverage.out coverage.html web/dist
