# aladdin —— 前后端与命令行一体仓库
#
# 所有代码生成都经由 `make gen`：不允许多个入口各自生成，
# 否则生成产物会漂移（见 docs/ssot-registry.md）。

MODULE      := github.com/poetlife/aladdin
BIN_DIR     := bin
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)

# 发布产物比本机构建多带一个标记，自更新据此区分两者（见 internal/upgrade）。
# 版本号形态单独作不了判据：在恰好处于某个 tag 的干净工作树上，
# `git describe --tags` 输出的就是一个合法的 vX.Y.Z。
RELEASE_LDFLAGS := $(LDFLAGS) -X main.released=true

# 发布产物的形态（make release-build）。发版时 VERSION 由 tag 传入，
# 与 LDFLAGS 共用同一处版本注入，不另起一份 -X。
DIST_DIR    := dist
PLATFORMS   := linux/amd64 darwin/arm64
GO_BINS     := aladdin-server aladdin
# 校验和工具名在两端不同：Linux 是 sha256sum，macOS 是 shasum -a 256。
SHA256      := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo "shasum -a 256")

# 发布产物的 CLI 里注入的默认目标地址：发布出去的 CLI 开箱就该连官方服务。
#
# 只作用于 CLI 那一个二进制——服务端的监听默认值在任何构建形态下都是回环。
# 值形如 host:port（地址不接受 scheme），由 CI 从仓库变量传入：它是部署实例的
# 值，不进仓库（见 docs/release.md 的"产物"）。空值合法，本机构建的发布产物
# 因此默认连本机——它**不代表**发布出去的那一种。
RELEASE_CLI_ADDRESS ?=
CLI_ADDRESS_LDFLAG  := $(if $(RELEASE_CLI_ADDRESS),-X $(MODULE)/internal/config.injectedCLIDefaultAddress=$(RELEASE_CLI_ADDRESS))

# 生成代码与静态检查所需的工具。用 `go install` 固定版本，避免"我这能跑"。
#
# 五条一律钉到具体版本，不留 @latest：留一个，本机与 CI 就可能装到不同的版本，
# "我这能跑"只是从本机搬到了 CI，而且失败会发生在没人动过 proto 的日子里
# （protoc-gen-go-grpc 的输出会随之改变，make check-gen 因此报不同步）。
#
# 这份清单同时是 CI 工具缓存的失效依据（见 .github/workflows/gate.yml）：
# 改这里会让缓存 key 变化。缓存 key 是派生物，版本仍然只有这一处来源。
#
# golangci-lint 也在这里，而不是"本机装了就用、没装就跳过"：CI 门禁里
# 静默跳过等于没有门禁（见 make lint）。
TOOLS := \
	google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12 \
	google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2 \
	connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.21.0 \
	github.com/sudorandom/protoc-gen-connect-openapi@v0.14.2 \
	github.com/bufbuild/buf/cmd/buf@v1.73.0 \
	github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0

TOOL_BIN := $(shell go env GOPATH)/bin

export PATH := $(TOOL_BIN):$(PATH)

.DEFAULT_GOAL := help
.PHONY: help
help: ## 显示本帮助
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## ---------------------------------------------------------------- 依赖与生成

.PHONY: tools
tools: ## 安装代码生成与静态检查工具（已装且版本一致则跳过）
	@# 逐个工具比对：二进制在、且戳记里的版本与 TOOLS 一致，才跳过。
	@# 两条都要判——只看戳记会让"二进制被人删了"变成静默跳过，
	@# 而门禁里静默跳过等于没有门禁（同上）。
	@# 逐条判断而非整体判断，是为了让"只升了 golangci-lint"不连累 buf 重编 1 分钟。
	@for t in $(TOOLS); do \
		name=$$(printf '%s' "$$t" | sed -e 's/@.*//' -e 's#.*/##'); \
		marker="$(TOOL_BIN)/.tool-$$name"; \
		if [ -x "$(TOOL_BIN)/$$name" ] && [ -f "$$marker" ] \
			&& [ "$$(cat "$$marker")" = "$$t" ]; then \
			echo ">>> 已就绪 $$name（$$t）"; \
			continue; \
		fi; \
		echo ">>> go install $$t"; \
		go install "$$t" || exit 1; \
		printf '%s' "$$t" > "$$marker"; \
	done

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

.PHONY: api-docs
api-docs: ## 生成 OpenAPI 文档（含鉴权扩展），供对外查阅
	@# 两步缺一不可：第一步产出标准 OpenAPI，第二步补上 aladdin 自己的
	@# 鉴权注解（生成器看不见私有注解，见 internal/tools/openapigen）。
	@# 第二步顺带产出一份合并文档供渲染页使用——渲染器只认单份 spec。
	buf generate --template buf.gen.openapi.yaml
	go run ./internal/tools/openapigen -merge-out web/public/api-docs/openapi.yaml

.PHONY: check-api-docs
check-api-docs: ## 校验 OpenAPI 文档与 proto 同步（CI 用）
	@# 与 check-gen 同一个套路，但用 git status 而非 git diff：
	@# 新增一个服务会多出**未跟踪**的文档文件，而 git diff 看不见未跟踪文件，
	@# 那会让"忘了为新服务生成文档"逃过校验。代价是刚 add 还没 commit 时
	@# 这一步也会报错——它校验的正是"已提交的文档与 proto 一致"。
	buf generate --template buf.gen.openapi.yaml
	go run ./internal/tools/openapigen -merge-out web/public/api-docs/openapi.yaml
	@if git rev-parse --git-dir >/dev/null 2>&1; then \
		if [ -n "$$(git status --porcelain -- api/openapi web/public/api-docs/openapi.yaml)" ]; then \
			echo "OpenAPI 文档与已提交版本有差异，请运行 make api-docs 并提交结果："; \
			git status --porcelain -- api/openapi web/public/api-docs/openapi.yaml; \
			exit 1; \
		fi; \
		echo "OpenAPI 文档与 proto 同步"; \
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

.PHONY: web-ci
web-ci: ## 按 lockfile 安装前端依赖（CI 用，与 web-install 的区别是不更新 lockfile）
	cd web && npm ci

.PHONY: web-install
web-install: ## 安装前端依赖
	cd web && npm install

.PHONY: web-build
web-build: ## 构建前端
	cd web && npm run build

.PHONY: release-build
release-build: web-build ## 产出发布产物到 dist/（跨平台二进制、前端包、校验和）
	@rm -rf $(DIST_DIR) && mkdir -p $(DIST_DIR)
	@if [ -z "$(RELEASE_CLI_ADDRESS)" ]; then \
		echo ">>> 注意：未提供 RELEASE_CLI_ADDRESS，本次的 CLI 产物默认连本机（只适合本机验证）"; \
	fi
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		for b in $(GO_BINS); do \
			echo ">>> $$os/$$arch $$b"; \
			ldflags='$(RELEASE_LDFLAGS)'; \
			if [ "$$b" = "aladdin" ]; then ldflags="$$ldflags $(CLI_ADDRESS_LDFLAG)"; fi; \
			GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
				go build -trimpath -ldflags "$$ldflags" -o $(DIST_DIR)/$$b ./cmd/$$b || exit 1; \
			if [ "$$b" = "aladdin" ] && [ -n "$(RELEASE_CLI_ADDRESS)" ]; then \
				echo "$(RELEASE_CLI_ADDRESS)" | grep -Eq '^[^:]+:[0-9]+$$' || { \
					echo "RELEASE_CLI_ADDRESS 必须形如 host:port（地址不接受 scheme 与路径），得到 $(RELEASE_CLI_ADDRESS)" >&2; \
					exit 1; }; \
				grep -aqF -- "$(RELEASE_CLI_ADDRESS)" $(DIST_DIR)/$$b || { \
					echo "注入失败：$(DIST_DIR)/$$b 中找不到 $(RELEASE_CLI_ADDRESS)" >&2; \
					echo "  链接器 -X 在符号名写错或符号不可达时会静默失效，产物会悄悄回落到本机默认值" >&2; \
					exit 1; }; \
			fi; \
			tar -czf $(DIST_DIR)/$${b}_$(VERSION)_$${os}_$${arch}.tar.gz -C $(DIST_DIR) $$b || exit 1; \
			rm $(DIST_DIR)/$$b; \
		done; \
	done
	tar -czf $(DIST_DIR)/aladdin-web_$(VERSION).tar.gz -C web dist
	cd $(DIST_DIR) && $(SHA256) *.tar.gz > SHA256SUMS
	@echo ">>> 已产出（VERSION=$(VERSION)）：" && ls -1 $(DIST_DIR)

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

# 开发环境的两条命令各自只有一处来源：单独起用 dev-server / web-dev，
# 一起起用 dev，三者引用的都是下面这两个变量，不存在第二份拷贝。
#
# 起服务端前先加载 .env.local（在 .gitignore 里，不进版本库）：本机的密钥
# ——对象存储的两项密钥——只能来自环境变量，配置文件会进版本库、进镜像，
# 凭证不可以（见 docs/design/config/README.md）。它是生产端
# /opt/aladdin/secrets.env 在开发机上的对应物；文件不存在时照常起，只是没有
# 对象存储可用。非密钥的本地改动（如 cos_bucket_url）仍写 config.local.yml。
DEV_ENV_LOAD   := set -a; [ ! -f .env.local ] || . ./.env.local; set +a;

DEV_SERVER_CMD := $(DEV_ENV_LOAD) ALADDIN_DEV_SEED=1 ALADDIN_DEV_TOKEN=dev-token ALADDIN_DEV_SUBJECT=dev-user ALADDIN_DEV_ROLE=system.admin ALADDIN_DEV_SCOPE=tenant/acme ALADDIN_LOG_LEVEL=debug go run ./cmd/aladdin-server
WEB_DEV_CMD    := cd web && npm run dev

.PHONY: dev-server
dev-server: ## 以开发种子数据启动服务端（本地联调用）
	$(DEV_SERVER_CMD)

.PHONY: web-dev
web-dev: ## 启动前端开发服务器
	$(WEB_DEV_CMD)

.PHONY: dev
dev: ## 一键拉起开发环境（服务端 + 前端），Ctrl-C 一并停止
	@# 这个目标只负责"同时起、一起停"，两边的命令都由上面的变量给出。
	@#
	@# 两处进程组处理都不能删，删掉任何一处都会留下一堆还在跑的服务：
	@# 1) 服务端用 set -m 起，让它独占一个进程组。收尾时按组杀，才能连
	@#    go run -> aladdin-server 一起收掉；只 kill go run 的 pid 会留下真正
	@#    在监听 9090 的那个进程，下次 make dev 直接撞端口。
	@# 2) 前端必须回到本 shell 的进程组（set +m）。终端 Ctrl-C 只发给前台
	@#    进程组，job control 若把 vite 单独分出去，它就收不到 Ctrl-C，而配方
	@#    shell 会一直等这个永不结束的前台 job——连下面的 trap 都轮不到执行。
	@#
	@# 130 是 Ctrl-C 下 shell 的常规退出码，折算成 0：中断是这里唯一的正常
	@# 收场方式，留着会让 make 每次都打一行 "*** [dev] Error 130" 像崩了。
	@# 其它非零码原样传出去——前端真起不来时必须让它看起来就是失败。
	@set -m; \
		$(DEV_SERVER_CMD) & \
		server=$$!; \
		set +m; \
		trap 'kill -TERM -$$server 2>/dev/null; wait $$server 2>/dev/null' EXIT INT TERM; \
		$(WEB_DEV_CMD); \
		status=$$?; \
		[ "$$status" = 130 ] && status=0; \
		exit $$status

.PHONY: clean
clean: ## 清理构建产物
	rm -rf $(BIN_DIR) $(DIST_DIR) coverage.out coverage.html web/dist
