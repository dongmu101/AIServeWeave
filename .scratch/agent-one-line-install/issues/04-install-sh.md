# 04 引导脚本 install.sh（Ubuntu、macOS）

Status: ready-for-agent
Blocked by: 02, 03

依据：[spec.md「引导脚本」](../spec.md#引导脚本)。

## 范围

- 新增 `deploy/agent/install.sh`：四种凭据方式、三种来源、`--upgrade`、默认账户创建、依赖检查、先下载校验后兑换、临时目录清理。
- `agent.sh` 只做必要改动（如有），保持现有参数与语义。
- `deploy/agent/tests/test_deployment.py` 增加管道执行与桩接口用例。

## 验收

- [x] spec「Testing Decisions」脚本各项（macOS 本机已跑通 16+1 个用例；Ubuntu 要等 CI 的 ubuntu-staging 作业）
- [x] 输出不含兑换码与令牌（每个用例都断言；含 pty 交互用例）
- [ ] `shellcheck`（本机未安装，未执行；仅 `bash -n` 通过）

## Comments

### 完成记录

- 新增 `deploy/agent/install.sh`，`agent.sh` 未改。测试在新文件 `deploy/agent/tests/test_install_sh.py`（本地 HTTPS 桩 + `CURL_CA_BUNDLE`），被 CI 现有的 `unittest discover` 自动发现。
- 令牌直写增加 `--registry`、`--gateway` 两个必填项（spec 表中未列，但 Console 的 token 响应带这两个地址，agent.sh 也必须有）；另有测试用的 `--root`/`--stage` 透传。
- 占位符判断用 `__AISW_*` 模式，避免平台下发时被 `ReplaceAll` 一并替换。
- 兑换码走 curl 标准输入，令牌直接从响应写入 0600 文件，不进变量与命令行（`--token` 模式除外，由运维自己给出）。
- 覆盖：三种凭据、交互输入、管道执行、自定义 HTTPS 来源、下载失败/校验和不符不兑换、404/429/503 映射、已安装拒绝与 `--upgrade`、dirty/dev 版本拒绝、明文 HTTP 平台拒绝。
- 未验证：Ubuntu 真机（root、`useradd`、`apt-get`、systemd）、`--source github` 的真实下载（只测了无仓库时的报错）、`shellcheck`。
