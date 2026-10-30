# Agent 独立部署

本目录只安装 Agent，连接已有的同版本 Registry/Gateway。安装机不需要 Go；模型、GPU 驱动、Docker 和 CLI 由节点用户自行安装。配置文件仍使用 [Agent 配置](../../service/aiServeWeaveAgent/README.md) 的 YAML 语义。

代码交付与实机兼容性验收分开记录：[验收矩阵](acceptance.md)。当前不把交叉构建、暂存脚本测试或 CI runner 当作目标系统验收。

## 支持目标与运行身份

| 系统 | 原生 Agent | 启动方式 | 安装权限 |
| --- | --- | --- | --- |
| Ubuntu 22.04 / 24.04 / 26.04 LTS | amd64、arm64 | systemd 系统服务，重启后启动 | 管理员 sudo 安装，指定普通用户运行 |
| macOS 15 / 26 / 27 | Apple Silicon arm64 | 当前用户 LaunchAgent，登录后启动 | 当前用户，无 sudo |
| Windows 11 25H2 / 26H2 | amd64、arm64 | 当前用户登录计划任务 | 当前用户，不提权 |

Windows 26H1 新硬件另列专项验证；较新系统版本未完成实机验收前不自动成为已验证。现有 darwin/amd64 制品和手动运行方式保留，桌面安装脚本仅支持 Apple Silicon。Windows 使用原生 exe，不通过 WSL。CLI 优先使用官方原生版本；Windows ARM64 上的官方 x64 CLI 仿真必须独立验证并记录，不能据此推定 GPU/驱动兼容。

桌面锁屏后继续运行；睡眠/休眠期间不承诺在线，唤醒后沿用隧道重连；注销、关机退出，下次登录恢复。脚本不修改睡眠设置、不创建 Windows SYSTEM 服务或 macOS LaunchDaemon。

## 准备接入材料

平台管理员提供：唯一 node ID、绑定该节点的一次性令牌文件、Registry CA PEM、可达的 Registry 与 Gateway `host:port`。服务器证书 SAN 必须匹配远程地址，不能直接照抄本机 `localhost` 示例，也不能关闭 TLS 校验。管理员凭据不导入节点。注册成功后 Agent 删除导入的令牌，节点证书/私钥保留并自动续期。

从可信的同版本 Release 获取压缩包并解压。包内含原生二进制、脚本、README、仅该二进制的 SHA256SUMS.txt。根 Release 的清单也可使用：安装器只校验所选文件，不要求下载其他服务。SHA256 检测损坏，来源信任由管理员确认；内部首版没有对外签名/公证保证，不绕过 Gatekeeper、SmartScreen 或组织执行策略。

下列 `v0.99.1` 为命令示例，替换成实际已发布并与平台一致的版本。

## Ubuntu

```bash
sudo bash agent.sh install --version v0.99.1 --user inference \
  --file ./aiserveweave-agent_v0.99.1_linux_amd64 --checksums ./SHA256SUMS.txt \
  --node-id gpu-node-01 --registry registry.example.com:9090 \
  --gateway gateway.example.com:9090 --ca-file ./registry-ca.crt \
  --token-file ./node-token.txt \
  --path /home/inference/.local/bin:/home/inference/.npm-global/bin:/usr/local/bin:/usr/bin:/bin
```

arm64 使用对应文件。在线安装将 `--file ... --checksums ...` 换成 `--base-url https://可信发布站/releases/download/v0.99.1`。版本总是显式指定，不支持 `latest`。在线模式需要 curl、HTTPS 信任库；本地模式不下载、不编译。还需要系统自带的 bash、file、awk、getent、runuser、SHA256 工具和 systemd。

- 版本二进制：`/usr/local/lib/aiserveweave-agent/releases/<version>/`；稳定入口：`/usr/local/bin/aiserveweave-agent`。
- 可写配置：`/var/lib/aiserveweave-agent/agent.yaml`；身份：该目录下 `identity/`。普通服务账户必须能创建和替换文件；身份 0600、目录 0700。
- 服务环境：`/etc/aiserveweave-agent/agent.env`，root:root 0600，systemd EnvironmentFile 语法。HOME、PATH 已显式设置。按需添加托管 API 环境变量；不把密钥写入命令行或工单。
- CLI 以 `--user` 对应账户登录；nvm/asdf 等非标准安装目录须显式加入 `--path`。后续修改 `/usr/local/lib/aiserveweave-agent/cli-path` 后重新 install/upgrade，或在 agent.env 覆盖 PATH。不依赖 shell profile。脚本不读取、复制或清理 CLI 登录。
- 日志：`journalctl -u aiserveweave-agent -f`，容量由系统 journald 策略控制。

```bash
sudo bash agent.sh status
sudo bash agent.sh stop
sudo bash agent.sh start
sudo bash agent.sh reload
ssh -L 8899:127.0.0.1:8899 inference@node.example.com
```

浏览器打开 `http://127.0.0.1:8899`。设置页与指标仅监听回环；节点无需公网入站端口。页面保存与 reload 只重载运行时声明；连接、身份、允许列表等配置修改仍需停止后启动。

## macOS

在目标用户登录的 Terminal 中执行，不使用 sudo：

```bash
bash agent.sh install --version v0.99.1 \
  --file ./aiserveweave-agent_v0.99.1_darwin_arm64 --checksums ./SHA256SUMS.txt \
  --node-id mac-node-01 --registry registry.example.com:9090 \
  --gateway gateway.example.com:9090 --ca-file ./registry-ca.crt --token-file ./node-token.txt
bash agent.sh status
```

程序和状态位于 `~/Library/Application Support/AIServeWeave/Agent`，服务定义为 `~/Library/LaunchAgents/com.aiserveweave.agent.plist`。默认捕获安装时 PATH；可用 `--path` 指定，并在安装根目录的 `cli-path` 调整后重新安装以刷新定义。日志在 `~/Library/Logs/AIServeWeave/Agent/agent.log`，每文件最多 10 MiB，保留当前文件和两个备份。`--root` 可指定其他当前用户安装目录；后续维护必须传同一根目录，一个用户只注册一个该名称的 LaunchAgent。

打开本机 `http://127.0.0.1:8899`。`start/stop/status/reload` 与 Ubuntu 同名。HOME、CLI 登录、Keychain 和 account_dir 均属于当前用户；后台进程不会读取交互式 shell 启动文件。托管 API 等环境变量写入安装根目录的私有 `environment.json`（JSON 字符串键值对象，0600），然后 stop/start；不在 plist 或命令行保存密钥。

## Windows

使用 Windows PowerShell 5.1 或更新版本，在目标普通用户的非提权窗口运行：

```powershell
.\agent.ps1 install -Version v0.99.1 `
  -File .\aiserveweave-agent_v0.99.1_windows_amd64.exe -Checksums .\SHA256SUMS.txt `
  -NodeId win-node-01 -Registry registry.example.com:9090 -Gateway gateway.example.com:9090 `
  -CAFile .\registry-ca.crt -TokenFile .\node-token.txt
.\agent.ps1 status
.\agent.ps1 stop
.\agent.ps1 start
.\agent.ps1 reload
```

ARM64 主机选原生 arm64.exe；安装器按原生 OS 架构检查 PE，x64 PowerShell 仿真不会改变制品选择。安装路径默认为 `%LOCALAPPDATA%\AIServeWeave\Agent`，支持 `-Root` 指定本地 NTFS 路径；后续维护使用相同路径。ACL 仅授权当前用户、SYSTEM 和 Administrators；Agent 会检查身份文件 ACL，不以 Windows 的模拟 Unix mode 替代权限检查。拒绝网络目录、非 NTFS 和安装树上的重解析入口。

计划任务名称含当前用户 SID；使用 InteractiveToken、最低权限、不保存密码、只在该用户登录后运行。明确设置无限执行时长、允许电池启动和持续运行、忽略重复实例、失败最多重试三次。任务运行私有 PowerShell 启动器并隐藏控制台，启动器复用 `environment.json` 的用户环境；此 JSON 可补充托管 API 环境变量，修改后重新 start。不得加入自动升级参数。脚本遵循本机执行策略；组织禁止脚本或计划任务时应按组织流程处理。

状态/停止/重载通过仅当前 SID 可用、拒绝远程连接的 named pipe，包含对端身份检查，不新增 TCP 控制端口。停止先确认 Agent 退出，随后才允许替换稳定 exe；CLI 在运行前加入 kill-on-close Job，关闭或崩溃回收后代。官方 `.exe` 可直接使用；常见 npm `.cmd` 包装器被解析为 Node 与已验证的 `node_modules` 脚本入口，不调用 cmd.exe。其他自定义包装器须改为受支持的绝对 exe 入口，不支持执行任意 shell 片段。

HOME/USERPROFILE、AppData、TEMP/TMP、SystemRoot、PATH 等按后台环境与账户隔离规则传递。磁盘余量检查使用 Windows API；尚无 Windows 探测的其他硬件字段保持 unknown，不伪造数值。日志为 `state\logs\agent.log`，10 MiB × 3。设置页仍为本机 `http://127.0.0.1:8899`。

## 升级、回退、卸载

安排短暂维护窗口。先取得目标发布版本与摘要，确认平台协议及配置兼容性。安装器先验证下载、SHA256、架构和内嵌版本，并用新程序检查已有 YAML；成功后才停止、备份配置、切换入口、启动并等待本地状态进入 running（最多两分钟；不替代隧道及请求验收）。下载或校验失败不会停止旧实例。升级不替换运行中的 Windows exe。内建自升级默认关闭，部署定义不传启用参数。

```bash
sudo bash agent.sh upgrade --version v0.99.2 --file ./aiserveweave-agent_v0.99.2_linux_amd64 --checksums ./SHA256SUMS.txt
sudo bash agent.sh rollback --version v0.99.1
sudo bash agent.sh uninstall
```

macOS 去掉 sudo 并选择对应文件；Windows 用 `agent.ps1 upgrade -Version ... -File ... -Checksums ...`、`rollback -Version ...`、`uninstall`。旧版本制品保留，已存在的同版本不同字节会被拒绝。首次接入参数只用于首次配置，重复安装不覆盖现有 YAML、身份或 CLI 登录。Ubuntu 服务账户不允许被重复安装悄悄更换。

配置备份在 `state/backups`（Ubuntu 为 `/var/lib/aiserveweave-agent/backups`）。回退只切换二进制，管理员按发布说明决定是否需要恢复某份兼容配置，不能用旧身份备份覆盖当前有效证书。升级后启动失败会明确报错并保留目标入口与旧制品，使用显式 rollback 恢复，不盲目自动回退配置。

卸载只移除服务和稳定程序入口，保留配置、身份、备份、日志和版本文件；不自动删除 CLI 目录。清除 Agent 数据应在停止且确认备份后由用户显式删除对应目录；平台侧禁用/删除节点是独立操作。首次接入中途失败且未生成 YAML 时，检查私有 identity 目录中的导入材料，确认后手动移除未消费的导入副本再重试，不覆盖已有节点身份。

维护锁为安装根目录 `.maintenance-lock`；只在确认没有维护进程后清理崩溃遗留锁。保留配置备份需定期按本地保留策略归档或清理，Agent 不自动轮转这些备份。

## 诊断与验收

`status` 成功仅表示本地 Agent 可响应，JSON 分别给出启动阶段 starting/running、PID/版本、Gateway 状态计数、运行时 ID/类型/健康与模型数量，不包含完整描述符或凭据。确认隧道已连接、目标模型可调度后，仍须经 Gateway 验证普通请求和 SSE，另外检查真实 Codex/Claude 登录与取消、续期、睡眠恢复、停止后无残留。

Agent 新增维护参数可独立使用：`-config ABS_PATH -init-config`（配合连接参数，独占创建）；`-check-config`（离线验证部署 YAML）；`-control status|stop|reload`（相同用户）；`-log-file ABS_PATH`（有界私有日志）、`-env-file ABS_PATH`（至多 64 KiB 的私有 JSON 环境）。身份替换使用受保护的 `.pending` 记录，重启先验证并补完中断写入；无恢复记录的半对身份仍拒绝启动。不要在运行期间手工移动身份文件或 pending 记录。

开发验证：`python3 -m unittest discover -s deploy/agent/tests`；Windows 使用 `powershell -NoProfile -File deploy/agent/tests/deployment.Tests.ps1`。脚本的 `--stage --root TEMP_DIR` / `-Stage -Root TEMP_DIR` 只生成可审查的暂存树，不注册服务、不启动进程、不把暂存标为已部署；初始 YAML 由目标可执行程序正式安装时创建。完整测试与目标系统记录见 [acceptance.md](acceptance.md)。

后台语义参考 [Microsoft TaskSettings](https://learn.microsoft.com/windows/win32/taskschd/tasksettings) 和 [Apple LaunchAgent 文档](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html)。
