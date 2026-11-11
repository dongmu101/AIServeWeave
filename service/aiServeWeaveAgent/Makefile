# 本机开发用：构建 Agent，并在 macOS 上作为当前用户的 launchd LaunchAgent 常驻
# （登录后自动启动，异常退出 15 秒后自动拉起，正常退出不拉起）。
#
# 与 deploy/agent/agent.sh 安装的正式服务（com.aiserveweave.agent）使用不同的
# Label、二进制目录和日志目录，互不覆盖；正式部署仍用 deploy/agent/agent.sh。
# 两者同时运行时注意 config 里的 metrics_addr / 设置页端口不要冲突。
#
# 常用：make install（构建并常驻）、make status、make logs、make uninstall。

LABEL    ?= com.aiserveweave.agent.dev
CONFIG   ?= $(CURDIR)/config.yaml
override CONFIG := $(abspath $(CONFIG))
# 后台进程不读 shell 启动文件，claude/codex 等 CLI 只能从这里的 PATH 找到；默认取执行 make 时的 PATH。
AGENT_PATH ?= $(PATH)
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

DEV_ROOT := $(HOME)/Library/Application Support/AIServeWeave/AgentDev
BIN      := $(DEV_ROOT)/aiserveweave-agent
ENV_FILE := $(DEV_ROOT)/environment.json
LOG_DIR  := $(HOME)/Library/Logs/AIServeWeave/AgentDev
LOG_FILE := $(LOG_DIR)/agent.log
PLIST    := $(HOME)/Library/LaunchAgents/$(LABEL).plist
DOMAIN   := gui/$(shell id -u)
# reload/status 跟随已安装服务实际使用的配置（plist 中 -config 的值），未安装时用 CONFIG。
SVC_CONFIG = $(or $(shell test -f "$(PLIST)" && /usr/libexec/PlistBuddy -c 'Print :ProgramArguments:2' "$(PLIST)" 2>/dev/null),$(CONFIG))

.DEFAULT_GOAL := help
.PHONY: help build run check-config plist install uninstall start stop restart reload status logs

help: ## 列出可用目标
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-13s %s\n", $$1, $$2}'

build: ## 构建到 $(DEV_ROOT)（不改动仓库里的二进制）
	@mkdir -p "$(DEV_ROOT)"
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o "$(BIN)" .

run: build ## 前台运行（调试用，Ctrl-C 退出）
	"$(BIN)" -config "$(CONFIG)"

check-config: build ## 校验 CONFIG，不联网
	"$(BIN)" -config "$(CONFIG)" -check-config

plist: ## 生成 LaunchAgent 定义
	@test "$$(uname -s)" = Darwin || { echo "launchd 仅支持 macOS" >&2; exit 1; }
	@mkdir -p "$(dir $(PLIST))" "$(LOG_DIR)"
	@test -f "$(ENV_FILE)" || { mkdir -p "$(DEV_ROOT)"; printf '{}\n' > "$(ENV_FILE)"; chmod 600 "$(ENV_FILE)"; }
	@{ \
	  echo '<?xml version="1.0" encoding="UTF-8"?>'; \
	  echo '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">'; \
	  echo '<plist version="1.0"><dict>'; \
	  echo '<key>Label</key><string>$(LABEL)</string>'; \
	  echo '<key>ProgramArguments</key><array>'; \
	  for a in "$(BIN)" -config "$(CONFIG)" -env-file "$(ENV_FILE)" -log-file "$(LOG_FILE)"; do \
	    printf '<string>%s</string>\n' "$$a"; \
	  done; \
	  echo '</array>'; \
	  echo '<key>WorkingDirectory</key><string>$(CURDIR)</string>'; \
	  echo '<key>EnvironmentVariables</key><dict>'; \
	  echo '<key>HOME</key><string>$(HOME)</string><key>PATH</key><string>$(AGENT_PATH)</string>'; \
	  echo '</dict>'; \
	  echo '<key>StandardErrorPath</key><string>$(LOG_DIR)/stderr.log</string>'; \
	  echo '<key>RunAtLoad</key><true/>'; \
	  echo '<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>'; \
	  echo '<key>ThrottleInterval</key><integer>15</integer>'; \
	  echo '<key>ExitTimeOut</key><integer>25</integer>'; \
	  echo '<key>ProcessType</key><string>Background</string>'; \
	  echo '</dict></plist>'; \
	} > "$(PLIST)"
	@plutil -lint "$(PLIST)" >/dev/null
	@echo "已写入 $(PLIST)"

install: build plist ## 构建、生成定义并（重新）加载常驻服务；改代码后重跑即可
	-@launchctl bootout "$(DOMAIN)/$(LABEL)" 2>/dev/null
	@# bootout 异步返回，旧进程最多 ExitTimeOut(25s) 才退出；未卸载完就 bootstrap 会报 "5: Input/output error"。
	@i=0; while launchctl print "$(DOMAIN)/$(LABEL)" >/dev/null 2>&1; do \
	  i=$$((i+1)); test $$i -le 60 || { echo "等待 $(LABEL) 卸载超时" >&2; exit 1; }; sleep 0.5; \
	done
	launchctl bootstrap "$(DOMAIN)" "$(PLIST)"
	@echo "已启动 $(LABEL)，日志：$(LOG_FILE)"

uninstall: ## 停止并删除 LaunchAgent（保留二进制、environment.json 与日志）
	-@launchctl bootout "$(DOMAIN)/$(LABEL)" 2>/dev/null
	rm -f "$(PLIST)"

start: ## 加载并启动（需已 install）
	@test -f "$(PLIST)" || { echo "未安装，先 make install" >&2; exit 1; }
	launchctl bootstrap "$(DOMAIN)" "$(PLIST)"

stop: ## 卸载出 launchd（停止且不会被拉起，登录后也不会自启，直到 start）
	launchctl bootout "$(DOMAIN)/$(LABEL)"

restart: ## 重启进程（不重新构建）
	launchctl kickstart -k "$(DOMAIN)/$(LABEL)"

reload: ## 热重载 runtimes 声明（等同 SIGHUP / 设置页保存）
	"$(BIN)" -config "$(SVC_CONFIG)" -control reload

status: ## launchd 状态、进程树 CPU/内存与 Agent 自报状态
	@launchctl print "$(DOMAIN)/$(LABEL)" 2>/dev/null | grep -E '^\s*(state|pid|last exit code|runs) =' || echo "$(LABEL) 未加载"
	@# 资源：Agent 主进程及其全部子孙进程（claude/codex 等 CLI）；%CPU 为 ps 的近期均值，按单核计可超过 100%。
	@pid=$$(launchctl print "$(DOMAIN)/$(LABEL)" 2>/dev/null | awk '$$1=="pid"{print $$3; exit}'); \
	if [ -n "$$pid" ]; then \
	  ps -axo pid=,ppid=,%cpu=,rss=,etime=,comm= | awk -v root="$$pid" ' \
	    { n++; p[n]=$$1; pp[n]=$$2; cpu[n]=$$3; rss[n]=$$4; et[n]=$$5; c=$$6; for (j=7; j<=NF; j++) c=c " " $$j; \
	      k=split(c, a, "/"); cmd[n]=a[k] } \
	    END { t[root]=1; do { ch=0; for (i=1; i<=n; i++) if (!(p[i] in t) && (pp[i] in t)) { t[p[i]]=1; ch=1 } } while (ch); \
	      printf "资源:\n  %-7s %-7s %6s %10s %12s  %s\n", "PID", "PPID", "%CPU", "RSS(MB)", "ELAPSED", "COMMAND"; \
	      for (i=1; i<=n; i++) if (p[i] in t) { m++; sc+=cpu[i]; sr+=rss[i]; \
	        printf "  %-7s %-7s %6.1f %10.1f %12s  %s\n", p[i], pp[i], cpu[i], rss[i]/1024, et[i], cmd[i] } \
	      printf "  合计 %d 个进程  CPU %.1f%%  内存 %.1f MB\n", m, sc, sr/1024 }'; \
	else echo "资源: 进程未运行"; fi
	@echo "config: $(SVC_CONFIG)"
	@"$(BIN)" -config "$(SVC_CONFIG)" -control status || true

logs: ## 跟踪日志
	tail -F "$(LOG_FILE)" "$(LOG_DIR)/stderr.log"
