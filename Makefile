# Build the AIServeWeave images on this machine and ship them to a server
# that is too small to build them itself (see deploy/README.md「小内存主机」).
#
# 在本机构建 AIServeWeave 镜像，再送到自己构建不动的服务器上（见 deploy/README.md
# 「小内存主机」）。
#
#   make images                              # 为服务器平台构建四个镜像（默认 linux/amd64）
#   make push SERVER=user@host               # 经 ssh 把镜像 load 到服务器
#   make deploy SERVER=user@host             # 构建 + 送达 + 在服务器上 upgrade.sh --no-build
#   make help
#
# Images are tagged $(REPO)/aiserveweave-<service>:$(TAG), which with the
# defaults is exactly what deploy/docker-compose.yaml starts when the server's
# .env leaves AISW_IMAGE_TAG empty.
#
# 镜像标为 $(REPO)/aiserveweave-<服务>:$(TAG)；取默认值时，正是服务器 .env 中
# AISW_IMAGE_TAG 留空时 deploy/docker-compose.yaml 启动的镜像。

SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help

PLATFORM        ?= linux/amd64
REPO            ?= ghcr.io/dongmu101
TAG             ?= local
VERSION         ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
SERVICES        ?= registry controlplane gateway console
AGENT_PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
GOPROXY         ?=
SERVER          ?=
REMOTE_DIR      ?= AIServeWeave
SSH             ?= ssh
OUT_DIR         ?= dist/images
UPGRADE_ARGS    ?=
FORCE           ?= 0

ARCH            := $(lastword $(subst /, ,$(PLATFORM)))
NATIVE_PLATFORM := linux/$(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
IMAGES          := $(foreach s,$(SERVICES),$(REPO)/aiserveweave-$(s):$(TAG))
GOPROXY_ARG     := $(if $(GOPROXY),--build-arg GOPROXY=$(GOPROXY))

SERVICE_registry     := aiServeWeaveRegistry
SERVICE_controlplane := aiServeWeaveControlPlane
SERVICE_gateway      := aiServeWeaveGateway

.PHONY: help images local push save deploy check-server check-git clean

help: ## 列出可用目标与变量
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "  变量（括号内为默认值）:"
	@echo "    SERVER=user@host       push / deploy 的目标服务器（必填）"
	@echo "    REMOTE_DIR=...         服务器上仓库的路径，相对 ssh 登录目录（$(REMOTE_DIR)）"
	@echo "    PLATFORM=...           镜像平台，须与服务器一致（$(PLATFORM)）"
	@echo "    SERVICES=\"...\"         只处理这些服务（$(SERVICES)）"
	@echo "    AGENT_PLATFORMS=\"...\"  打进 Gateway 的 Agent 平台，少则快（默认六个）"
	@echo "    GOPROXY=URL            Go 模块代理，如 https://goproxy.cn,direct"
	@echo "    UPGRADE_ARGS=\"...\"     deploy 时附加给服务器 upgrade.sh 的参数，如 --no-backup"
	@echo "    FORCE=1                deploy 时跳过「工作区干净且已推送」检查"
	@echo "    当前版本: $(VERSION)"

images: $(addprefix image-,$(SERVICES)) ## 为 PLATFORM 构建镜像并载入本机 Docker
	@echo "==> 已构建 $(PLATFORM)（版本 $(VERSION)）:"; printf '    %s\n' $(IMAGES)

local: ## 为本机平台构建，供本地 docker compose up 试跑
	@$(MAKE) --no-print-directory images PLATFORM=$(NATIVE_PLATFORM)

# The Console has its own context and Dockerfile and takes no build args.
# Console 有自己的构建上下文与 Dockerfile，不接受构建参数。
image-console:
	@echo "==> 构建 console（$(PLATFORM)）"
	docker buildx build --platform $(PLATFORM) --load \
	  -t $(REPO)/aiserveweave-console:$(TAG) service/aiServeWeaveConsole

image-%:
	@test -n "$(SERVICE_$*)" || { echo "未知服务: $*（可选: registry controlplane gateway console）" >&2; exit 1; }
	@echo "==> 构建 $*（$(PLATFORM)，版本 $(VERSION)）"
	docker buildx build --platform $(PLATFORM) --load \
	  --build-arg SERVICE=$(SERVICE_$*) --build-arg VERSION=$(VERSION) $(GOPROXY_ARG) \
	  $(if $(filter gateway,$*),--build-arg "AGENT_PLATFORMS=$(AGENT_PLATFORMS)") \
	  -t $(REPO)/aiserveweave-$*:$(TAG) .

check-server:
	@test -n "$(SERVER)" || { echo "需要 SERVER=user@host" >&2; exit 1; }

# push refuses images built for another architecture: a native arm64 build
# loaded onto an amd64 server fails only at container start, with
# "exec format error".
#
# push 拒绝为其他架构构建的镜像：本机 arm64 构建的镜像 load 到 amd64 服务器上，要到容器
# 启动时才以 "exec format error" 失败。
push: check-server ## 经 ssh 把镜像 load 到 SERVER（不重启服务）
	@remote="$$($(SSH) $(SERVER) uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"; \
	  [[ "$$remote" == "$(ARCH)" ]] || { echo "服务器是 $$remote，PLATFORM 是 $(PLATFORM)：用 PLATFORM=linux/$$remote 重新构建" >&2; exit 1; }
	@for image in $(IMAGES); do \
	  arch="$$(docker image inspect -f '{{.Architecture}}' "$$image" 2>/dev/null)" || { echo "本机没有 $$image，先 make images" >&2; exit 1; }; \
	  [[ "$$arch" == "$(ARCH)" ]] || { echo "$$image 是 $$arch 架构，不是 $(ARCH)：先 make images PLATFORM=$(PLATFORM)" >&2; exit 1; }; \
	done
	@echo "==> 传送到 $(SERVER)"
	docker save $(IMAGES) | gzip -1 | $(SSH) $(SERVER) 'gunzip | docker load'

save: ## 把镜像存成压缩包，供无法直连 ssh 时手工拷贝
	@mkdir -p $(OUT_DIR)
	@file="$(OUT_DIR)/aiserveweave-$(VERSION)-$(ARCH).tar.gz"; \
	  echo "==> 写入 $$file"; \
	  docker save $(IMAGES) | gzip > "$$file"; \
	  echo "    拷到服务器后: gunzip -c $$(basename "$$file") | docker load"; \
	  echo "    再在服务器上: deploy/upgrade.sh --no-build"

# The server's upgrade.sh fast-forwards to the upstream commit, so the images
# must come from that same commit: a clean, pushed checkout.
#
# 服务器上的 upgrade.sh 会快进到上游提交，因此镜像必须出自同一个提交：工作区干净且已推送。
check-git:
	@if [[ -n "$$(git status --porcelain --untracked-files=no)" ]]; then \
	  echo "工作区有未提交的改动，镜像与服务器拉到的代码不一致（FORCE=1 跳过）" >&2; [[ "$(FORCE)" == 1 ]]; fi
	@if [[ "$$(git rev-parse HEAD)" != "$$(git rev-parse '@{u}' 2>/dev/null)" ]]; then \
	  echo "HEAD 与上游分支不一致，先 git push（FORCE=1 跳过；服务器不想拉代码时加 UPGRADE_ARGS=--no-pull）" >&2; [[ "$(FORCE)" == 1 ]]; fi

deploy: check-server check-git images push ## 构建 + 送达 + 在服务器上 upgrade.sh --no-build
	@echo "==> 在 $(SERVER) 上重新部署"
	$(SSH) -t $(SERVER) 'cd $(REMOTE_DIR) && deploy/upgrade.sh --no-build $(UPGRADE_ARGS)'

clean: ## 删除 save 产生的压缩包
	rm -rf $(OUT_DIR)
