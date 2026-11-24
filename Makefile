# Build the AIServeWeave images on this machine and ship them to a server
# that is too small to build them itself (see deploy/README.md「小内存主机」).
#
# 在本机构建 AIServeWeave 镜像，再送到自己构建不动的服务器上（见 deploy/README.md
# 「小内存主机」）。
#
#   make images                              # 为服务器平台构建五个镜像（默认 linux/amd64）
#   make push SERVER=user@host               # 经 ssh 把镜像 load 到服务器
#   make deploy SERVER=user@host             # 构建 + 送达 + 在服务器上 upgrade.sh --no-build
#   make publish                             # 构建 amd64 + arm64 镜像并推送到 Docker Hub
#   make release VERSION=v0.1.1 [SERVER=...] # 同一版本发 GitHub Release 与镜像，可选部署服务器
#   make help
#
# Images are tagged $(REPO)/aiserveweave-<service>:$(TAG), which with the
# defaults is exactly what deploy/docker-compose.yaml starts when the server's
# .env leaves AISW_IMAGE_TAG empty.
#
# 镜像标为 $(REPO)/aiserveweave-<服务>:$(TAG)；取默认值时，正是服务器 .env 中
# AISW_IMAGE_TAG 留空时 deploy/docker-compose.yaml 启动的镜像。

# macOS ships GNU Make 3.81, which ignores .SHELLFLAGS (added in 3.82), and
# bash 3.2, which reads multibyte characters right after $name as part of the
# name. So recipes with pipes set pipefail themselves, and every variable
# followed by Chinese text is written ${name}.
#
# macOS 自带 GNU Make 3.81，它不认 .SHELLFLAGS（3.82 才有）；自带的 bash 3.2 会把紧跟在
# $name 后面的多字节字符读成变量名的一部分。所以带管道的命令自己 set -o pipefail，后面紧跟
# 中文的变量一律写成 ${name}。
SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help

PLATFORM        ?= linux/amd64
REPO            ?= ghcr.io/dongmu101
TAG             ?= local
# Same command as source_version in deploy/lib.sh, so a version tag names the
# same commit on both sides; follow mode pins AISW_IMAGE_TAG to it.
# 与 deploy/lib.sh 的 source_version 同一条命令，版本 tag 在两边指向同一个提交；跟随模式把 AISW_IMAGE_TAG 固定为它。
VERSION         ?= $(shell git describe --tags --always --dirty --abbrev=12 2>/dev/null || echo dev)
# Evaluate it once: ?= is recursive, so every recipe would rerun git describe,
# and a commit made while publish runs would tag images and print a version
# that differ from what was pushed.
# 只求值一次：?= 是递归展开，每个 recipe 都会重跑 git describe；publish 运行期间提交代码，
# 各镜像的 tag 与最后打印的版本就会与实际推送的不一致。
VERSION         := $(VERSION)
SERVICES        ?= registry controlplane gateway console studio
AGENT_PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
# Go services are compiled on this machine by scripts/build-go.sh; the root
# Dockerfile only copies the result. PREBUILT names a build-release.sh output
# directory to take the binaries from instead of compiling (make release sets
# it to dist, so the images carry the GitHub Release's own bytes).
# Go 服务由 scripts/build-go.sh 在本机编译，根目录 Dockerfile 只拷贝产物。PREBUILT 指向
# build-release.sh 的产物目录，改为从中取二进制、不再编译（make release 设为 dist，使镜像里
# 的正是 GitHub Release 的字节）。
PREBUILT        ?=
GOPROXY         ?=
SERVER          ?=
REMOTE_DIR      ?= AIServeWeave
SSH             ?= ssh
OUT_DIR         ?= dist/images
UPGRADE_ARGS    ?=
FORCE           ?= 0

# Docker Hub publishing / 推送到 Docker Hub
HUB_REPO          ?= docker.io/dongmu101
PUBLISH_PLATFORMS ?= linux/amd64,linux/arm64
# latest lets a server pull with deploy/upgrade.sh --image-tag latest.
# latest 让服务器可以用 deploy/upgrade.sh --image-tag latest 拉取。
PUBLISH_TAGS      ?= $(VERSION) latest
BUILDER           ?= aisw-multiarch
SOURCE_URL        ?= https://github.com/dongmu101/AIServeWeave

ARCH            := $(lastword $(subst /, ,$(PLATFORM)))
NATIVE_PLATFORM := linux/$(shell uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
IMAGES          := $(foreach s,$(SERVICES),$(REPO)/aiserveweave-$(s):$(TAG))
comma           := ,
LABEL_ARGS      := --label org.opencontainers.image.source=$(SOURCE_URL) \
                   --label org.opencontainers.image.revision=$(shell git rev-parse HEAD 2>/dev/null) \
                   --label org.opencontainers.image.version=$(VERSION) \
                   --label org.opencontainers.image.licenses=Apache-2.0
hub_tags         = $(foreach t,$(PUBLISH_TAGS),-t $(HUB_REPO)/aiserveweave-$(1):$(t))
# go_build compiles service $(2) for the space-separated platforms $(1) into
# .image-build/, which the root Dockerfile copies.
# go_build 为以空格分隔的平台 $(1) 编译服务 $(2)，产物放进 .image-build/ 供根目录 Dockerfile 拷贝。
go_build         = VERSION="$(VERSION)" PLATFORMS="$(1)" AGENT_PLATFORMS="$(AGENT_PLATFORMS)" \
                   PREBUILT="$(PREBUILT)" $(if $(GOPROXY),GOPROXY="$(GOPROXY)") scripts/build-go.sh $(2)

SERVICE_registry     := aiServeWeaveRegistry
SERVICE_controlplane := aiServeWeaveControlPlane
SERVICE_gateway      := aiServeWeaveGateway

.PHONY: help images local push save deploy publish tag-latest release release-check release-tag release-github release-server builder builder-reset check-server check-git clean

help: ## 列出可用目标与变量
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "  变量（括号内为默认值）:"
	@echo "    SERVER=user@host       push / deploy 的目标服务器（必填）"
	@echo "    REMOTE_DIR=...         服务器上仓库的路径，相对 ssh 登录目录（$(REMOTE_DIR)）"
	@echo "    PLATFORM=...           镜像平台，须与服务器一致（$(PLATFORM)）"
	@echo "    SERVICES=\"...\"         只处理这些服务（$(SERVICES)）"
	@echo "    AGENT_PLATFORMS=\"...\"  打进 Gateway 的 Agent 平台，少则快（默认六个）"
	@echo "    GOPROXY=URL            本机编译 Go 服务时的模块代理，如 https://goproxy.cn,direct"
	@echo "    PREBUILT=DIR           镜像直接用 build-release.sh 在 DIR 的产物，不再编译（make release 自动设为 dist）"
	@echo "    UPGRADE_ARGS=\"...\"     deploy 时附加给服务器 upgrade.sh 的参数，如 --no-backup"
	@echo "    FORCE=1                deploy / publish 时跳过「工作区干净且已推送」检查"
	@echo "    HUB_REPO=...           publish 推送到的仓库（$(HUB_REPO)）"
	@echo "    PUBLISH_TAGS=\"...\"     publish 的 tag，如 \"v0.2.0 latest\"（默认当前版本与 latest）"
	@echo "    PUBLISH_PLATFORMS=...  publish 的平台（$(PUBLISH_PLATFORMS)）"
	@echo "    当前版本: $(VERSION)"

images: $(addprefix image-,$(SERVICES)) ## 为 PLATFORM 构建镜像并载入本机 Docker
	@echo "==> 已构建 $(PLATFORM)（版本 $(VERSION)）:"; printf '    %s\n' $(IMAGES)

local: ## 为本机平台构建，供本地 docker compose up 试跑
	@$(MAKE) --no-print-directory images PLATFORM=$(NATIVE_PLATFORM)

# The Console and Studio are compiled here with pnpm (scripts/build-web.sh); their
# Dockerfiles only copy the standalone output, which is plain JavaScript, so one
# build serves every platform. They have their own contexts and take no build args.
# Console 与 Studio 在本机用 pnpm 编译（scripts/build-web.sh）；它们的 Dockerfile 只拷贝
# standalone 产物，产物是纯 JavaScript，一次构建适用于所有平台。二者有各自的构建上下文，
# 不接受构建参数。
WEB_DIR_console := service/aiServeWeaveConsole
WEB_DIR_studio  := service/aiServeWeaveStudio

web-%:
	@test -n "$(WEB_DIR_$*)" || { echo "未知前端: $*（可选: console studio）" >&2; exit 1; }
	scripts/build-web.sh $(WEB_DIR_$*)

image-console: web-console
	@echo "==> 构建 console（$(PLATFORM)）"
	docker buildx build --platform $(PLATFORM) --load \
	  -t $(REPO)/aiserveweave-console:$(TAG) service/aiServeWeaveConsole

image-studio: web-studio
	@echo "==> 构建 studio（$(PLATFORM)）"
	docker buildx build --platform $(PLATFORM) --load \
	  -t $(REPO)/aiserveweave-studio:$(TAG) service/aiServeWeaveStudio

image-%:
	@test -n "$(SERVICE_$*)" || { echo "未知服务: $*（可选: registry controlplane gateway console studio）" >&2; exit 1; }
	$(call go_build,$(PLATFORM),$(SERVICE_$*))
	@echo "==> 构建 $*（$(PLATFORM)，版本 $(VERSION)）"
	docker buildx build --platform $(PLATFORM) --load \
	  --build-arg SERVICE=$(SERVICE_$*) \
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
	@remote="$$($(SSH) -o ConnectTimeout=10 $(SERVER) uname -m)" || { \
	  echo "无法 ssh 到 $(SERVER)。SERVER 是部署服务器的 ssh 地址（如 root@1.2.3.4），先确认 ssh $(SERVER) 能登录" >&2; exit 1; }; \
	  remote="$$(echo "$${remote}" | sed 's/x86_64/amd64/;s/aarch64/arm64/')"; \
	  [[ "$${remote}" == "$(ARCH)" ]] || { echo "服务器是 $${remote}，PLATFORM 是 $(PLATFORM)：用 PLATFORM=linux/$${remote} 重新构建" >&2; exit 1; }; \
	  $(SSH) $(SERVER) 'docker info >/dev/null 2>&1' || { \
	  echo "$(SERVER) 上无法执行 docker：确认已安装 Docker，且该用户在 docker 组中" >&2; exit 1; }
	@for image in $(IMAGES); do \
	  arch="$$(docker image inspect -f '{{.Architecture}}' "$${image}" 2>/dev/null)" || { echo "本机没有 $${image}，先 make images" >&2; exit 1; }; \
	  [[ "$${arch}" == "$(ARCH)" ]] || { echo "$${image} 是 $${arch} 架构，不是 $(ARCH)：先 make images PLATFORM=$(PLATFORM)" >&2; exit 1; }; \
	done
	@echo "==> 传送到 $(SERVER)"
	set -o pipefail; docker save $(IMAGES) | gzip -1 | $(SSH) $(SERVER) 'gunzip | docker load'

save: ## 把镜像存成压缩包，供无法直连 ssh 时手工拷贝
	@mkdir -p $(OUT_DIR)
	@set -o pipefail; file="$(OUT_DIR)/aiserveweave-$(VERSION)-$(ARCH).tar.gz"; \
	  echo "==> 写入 $${file}"; \
	  docker save $(IMAGES) | gzip > "$${file}" || { rm -f "$${file}"; exit 1; }; \
	  echo "    拷到服务器后: gunzip -c $$(basename "$${file}") | docker load"; \
	  echo "    再在服务器上: deploy/upgrade.sh --no-build"

# The server's upgrade.sh fast-forwards to the upstream commit, so the images
# must come from that same commit: a clean, pushed checkout.
#
# 服务器上的 upgrade.sh 会快进到上游提交，因此镜像必须出自同一个提交：工作区干净且已推送。
check-git:
	@if [[ -n "$$(git status --porcelain --untracked-files=no)" ]]; then \
	  echo "工作区有未提交的改动，镜像与服务器拉到的代码不一致（FORCE=1 跳过）" >&2; [[ "$(FORCE)" == 1 ]]; fi
	@git fetch --quiet 2>/dev/null || echo "警告: git fetch 失败，按本地记录的上游分支比较，可能已过期" >&2
	@head="$$(git rev-parse HEAD)" up="$$(git rev-parse '@{u}' 2>/dev/null)"; \
	if [[ "$$head" != "$$up" ]]; then \
	  if [[ -n "$$up" ]] && git merge-base --is-ancestor HEAD "$$up"; then \
	    echo "上游分支有本地没有的新提交（$$(git describe --tags --always --abbrev=12 "$$up")），服务器会快进到那个提交，镜像应出自同一提交，先 git pull（FORCE=1 跳过）" >&2; \
	  else \
	    echo "HEAD 与上游分支不一致，先 git push（FORCE=1 跳过；服务器不想拉代码时加 UPGRADE_ARGS=--no-pull）" >&2; \
	  fi; \
	  [[ "$(FORCE)" == 1 ]]; fi

deploy: check-server check-git images push ## 构建 + 送达 + 在服务器上 upgrade.sh --no-build
	@echo "==> 在 $(SERVER) 上重新部署"
	$(SSH) -t $(SERVER) 'cd $(REMOTE_DIR) && deploy/upgrade.sh --no-build $(UPGRADE_ARGS)'

# publish builds every image for PUBLISH_PLATFORMS and pushes it straight to
# Docker Hub. A multi-platform push needs a docker-container builder, created
# here once under its own name so the default builder stays untouched. The
# tag is git describe's, which names the commit; the server checks its
# compose file against that commit (check_image_tag in deploy/lib.sh).
#
# publish 为 PUBLISH_PLATFORMS 构建全部镜像并直接推送到 Docker Hub。多平台推送需要
# docker-container 驱动的 builder，这里以独立名字创建一次，不改动默认 builder。tag 取
# git describe，指明了提交；服务器据此核对自己的 compose 文件（deploy/lib.sh 的
# check_image_tag）。
publish: check-git builder $(addprefix publish-,$(SERVICES)) ## 构建 amd64 + arm64 镜像并推送到 Docker Hub
	@echo "==> 已推送到 $(HUB_REPO)（$(PUBLISH_PLATFORMS)）:"
	@for s in $(SERVICES); do for t in $(PUBLISH_TAGS); do echo "    $(HUB_REPO)/aiserveweave-$${s}:$${t}"; done; done
	@echo
	@echo "  服务器上部署:"
	@echo "    deploy/upgrade.sh（默认拉取 :latest；曾用 --image-tag 或 --build 的部署先 deploy/upgrade.sh --follow 一次）"

# tag-latest points latest at an already published VERSION without building
# anything: the registry copies the manifest, so it takes seconds.
#
# tag-latest 把 latest 指向一个已发布的 VERSION，不构建任何东西：仓库端复制清单，几秒完成。
tag-latest: ## 把已发布的 VERSION 标为 latest（不构建），如 make tag-latest VERSION=v0.1.0-169-g397ddc0bc8c3
	@for s in $(SERVICES); do \
	  echo "==> $(HUB_REPO)/aiserveweave-$${s}:$(VERSION) -> latest"; \
	  docker buildx imagetools create -t $(HUB_REPO)/aiserveweave-$${s}:latest $(HUB_REPO)/aiserveweave-$${s}:$(VERSION); \
	done

# release ships one official version to both sides: the GitHub Release
# (binaries and install scripts, what `install.sh --source github` downloads)
# and the Docker Hub images, whose Gateway bakes in the same version's Agent
# (what the platform serves under /agent/download/). The Gateway only names
# the GitHub repository for a vX.Y.Z version, so both sides must carry the
# same tag on the same commit. With SERVER set, the server then pulls the
# :latest images just pushed (follow mode).
#
# release 把同一个正式版本发到两边：GitHub Release（二进制与安装脚本，即
# `install.sh --source github` 下载的内容）与 Docker Hub 镜像（其中 Gateway 内置同版本的
# Agent，即平台在 /agent/download/ 下提供的内容）。Gateway 只在版本为 vX.Y.Z 时给出
# GitHub 仓库，因此两边必须是同一提交上的同一个 tag。设置 SERVER 时，再让服务器拉取刚推送的
# :latest 镜像（跟随模式）。
release: release-check ## 同一正式版本发 GitHub Release + Docker Hub 镜像，如 make release VERSION=v0.1.1 [SERVER=user@host]
	@$(MAKE) --no-print-directory release-tag
	@$(MAKE) --no-print-directory release-github
	@$(MAKE) --no-print-directory publish PUBLISH_TAGS="$(VERSION) latest" PREBUILT=dist
	@if [[ -n "$(SERVER)" ]]; then $(MAKE) --no-print-directory release-server; else \
	  echo; echo "  服务器上部署: deploy/upgrade.sh（或 make release-server SERVER=user@host）"; fi
	@echo
	@echo "==> $(VERSION) 已发布。部署后确认: curl -fsS https://<平台域名>/agent/v1/manifest 的 version 为 $(VERSION) 且带 github 字段"

release-check:
	@[[ "$(VERSION)" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$$ ]] || { \
	  echo "release 需要正式版本号 vX.Y.Z，如 make release VERSION=v0.1.1（当前: $(VERSION)）" >&2; exit 1; }
	@command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1 || { echo "需要 GitHub CLI 并已 gh auth login" >&2; exit 1; }
	@if gh release view "$(VERSION)" >/dev/null 2>&1; then \
	  echo "GitHub 上已有 $(VERSION) 的 Release：换一个版本号，或先 gh release delete $(VERSION) --cleanup-tag" >&2; exit 1; fi
	@$(MAKE) --no-print-directory check-git

# The tag goes on HEAD, which check-git has confirmed is pushed, so the
# server's upgrade.sh fast-forwards to the very commit the images came from.
# tag 打在 HEAD 上，check-git 已确认它已推送，服务器 upgrade.sh 快进到的正是镜像所出自的提交。
release-tag:
	@if git rev-parse -q --verify "refs/tags/$(VERSION)" >/dev/null; then \
	  [[ "$$(git rev-list -n1 "$(VERSION)")" == "$$(git rev-parse HEAD)" ]] || { \
	    echo "tag $(VERSION) 已存在但不指向 HEAD：换一个版本号，或确认后 git tag -d $(VERSION)" >&2; exit 1; }; \
	else echo "==> 打 tag $(VERSION)"; git tag -a "$(VERSION)" -m "$(VERSION)"; fi
	git push origin "refs/tags/$(VERSION)"

# Notes come from the CHANGELOG section when there is one, like release.yml;
# otherwise GitHub generates them. Uploads exactly what SHA256SUMS.txt lists.
# release notes 有 CHANGELOG 小节时取自该小节（与 release.yml 相同），否则由 GitHub 生成。
# 上传的文件正是 SHA256SUMS.txt 列出的那些。
release-github:
	@echo "==> 编译 $(VERSION) 二进制"
	VERSION="$(VERSION)" scripts/build-release.sh
	@notes="$$(mktemp)"; trap 'rm -f "$${notes}"' EXIT; \
	  awk -v ver="## [$(VERSION:v%=%)]" ' \
	    index($$0, ver) == 1 { found=1; next } \
	    found && (/^## \[/ || /^\[.*\]: http/) { exit } \
	    found { print }' CHANGELOG.md > "$${notes}"; \
	  if [[ -s "$${notes}" ]]; then note_args=(--notes-file "$${notes}"); else \
	    echo "CHANGELOG.md 没有 [$(VERSION:v%=%)] 小节，改用 GitHub 自动生成的 release notes" >&2; note_args=(--generate-notes); fi; \
	  files=(dist/SHA256SUMS.txt); while read -r _ name; do files+=("dist/$${name}"); done < dist/SHA256SUMS.txt; \
	  echo "==> 创建 GitHub Release $(VERSION)（$${#files[@]} 个文件）"; \
	  gh release create "$(VERSION)" --verify-tag --title "$(VERSION)" "$${note_args[@]}" "$${files[@]}"

release-server: check-server ## 让 SERVER 拉取最新发布的 :latest 镜像并重启（deploy/upgrade.sh --follow）
	@echo "==> 在 $(SERVER) 上部署最新发布的镜像"
	$(SSH) -t $(SERVER) 'cd $(REMOTE_DIR) && deploy/upgrade.sh --follow $(UPGRADE_ARGS)'

builder:
	@docker info >/dev/null 2>&1 || { echo "无法连接 Docker：先启动 Docker Desktop" >&2; exit 1; }
	@docker buildx inspect $(BUILDER) >/dev/null 2>&1 || { \
	  echo "==> 创建多平台 builder $(BUILDER)"; \
	  docker buildx create --name $(BUILDER) --driver docker-container >/dev/null; }

builder-reset: ## 删除并重建多平台 builder（会清空它的构建缓存）
	-docker buildx rm $(BUILDER)
	@$(MAKE) --no-print-directory builder

# Each per-service target also prepares the builder, so running one alone
# (make publish-console) works the same way publish does.
# 每个单服务目标也准备 builder，单独运行（如 make publish-console）与 publish 行为一致。
publish-console: builder web-console
	@echo "==> 构建并推送 console（$(PUBLISH_PLATFORMS)）"
	docker buildx build --builder $(BUILDER) --platform $(PUBLISH_PLATFORMS) --push \
	  $(LABEL_ARGS) $(call hub_tags,console) service/aiServeWeaveConsole

# Studio, like the Console, is compiled here and has its own context and Dockerfile.
# Studio 与 Console 一样在本机编译，有自己的构建上下文与 Dockerfile。
publish-studio: builder web-studio
	@echo "==> 构建并推送 studio（$(PUBLISH_PLATFORMS)）"
	docker buildx build --builder $(BUILDER) --platform $(PUBLISH_PLATFORMS) --push \
	  $(LABEL_ARGS) $(call hub_tags,studio) service/aiServeWeaveStudio

publish-%: builder
	@test -n "$(SERVICE_$*)" || { echo "未知服务: $*（可选: registry controlplane gateway console studio）" >&2; exit 1; }
	$(call go_build,$(subst $(comma), ,$(PUBLISH_PLATFORMS)),$(SERVICE_$*))
	@echo "==> 构建并推送 $*（$(PUBLISH_PLATFORMS)，版本 $(VERSION)）"
	docker buildx build --builder $(BUILDER) --platform $(PUBLISH_PLATFORMS) --push \
	  --build-arg SERVICE=$(SERVICE_$*) \
	  $(LABEL_ARGS) $(call hub_tags,$*) .

clean: ## 删除 save 产生的压缩包与 scripts/build-go.sh 的产物
	rm -rf $(OUT_DIR) .image-build

