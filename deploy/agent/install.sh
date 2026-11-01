#!/usr/bin/env bash
# One-line bootstrap for the standalone Agent on Ubuntu and Apple Silicon macOS.
# It downloads the Agent and agent.sh from the platform (or GitHub Releases),
# verifies them, redeems the credential, then hands over to agent.sh.
#
#   curl -fsSL --proto '=https' https://PLATFORM/agent/install.sh | sudo bash -s -- --code CODE   # Ubuntu
#   curl -fsSL --proto '=https' https://PLATFORM/agent/install.sh | bash -s -- --code CODE        # macOS
#
# 独立 Agent 在 Ubuntu 与 Apple Silicon macOS 上的一行命令引导脚本。它从平台（或 GitHub
# Releases）下载 Agent 与 agent.sh 并校验，兑换凭据，然后交给 agent.sh 完成安装。
#
# Everything lives in functions and the last line calls main, so a truncated
# download defines functions and runs nothing.
# 全部逻辑都在函数里，最后一行才调用 main，下载被截断时只会定义函数而不执行任何东西。
set -Eeuo pipefail
umask 077

# The platform serves this file with the placeholder below replaced by its own
# origin. The comparison pattern is deliberately not the full placeholder, or
# serving would rewrite it too.
# 平台下发本文件时，下面的占位符会被替换为平台自己的站点地址。判断用的模式刻意不是完整占位符，
# 否则下发时它也会被一并替换。
PLATFORM_URL=__AISW_PLATFORM_URL__

usage() {
  cat <<'USAGE'
Usage: install.sh [options]
Credentials (pick one; with none, the install code is prompted on the terminal):
  --code CODE                  One-time install code
  --node-id ID --token TOKEN --ca-sha256 HEX --registry HOST:PORT --gateway HOST:PORT[,HOST:PORT]
                               Token credential; the CA is fetched from the platform and pinned to --ca-sha256
  --bundle ZIP                 Enrollment ZIP downloaded from the console (needs unzip)
Options:
  --source platform|github|https://DIR  Where the Agent comes from (default platform)
  --version VERSION            Default: the platform's version
  --platform URL               Platform origin; built in when the platform served this script
  --user NAME                  Ubuntu service account (default aiserveweave, created if missing)
  --path PATH                  Background CLI PATH, passed to agent.sh
  --upgrade                    Upgrade an installed node; no credential needed
  --root PATH --stage          Prepare a tree without starting a service (for tests)
USAGE
}

fail() { echo "install: $*" >&2; exit 1; }
log() { echo "==> $*"; }

code= node_id= token= ca_sha256= registry= gateway= bundle= source=platform version= platform=
service_user= cli_path= upgrade=false root= stage=false
goos= arch= state= tmp= binary= asset= base_url=

cleanup() { [ -z "$tmp" ] || rm -rf "$tmp"; }

# value_of returns the argument of an option, rejecting a missing or multi-line one.
# value_of 返回选项的参数，缺失或含换行时报错。
value_of() {
  [ "$#" -ge 2 ] || fail "missing value for $1"
  case "$2" in *$'\n'*|*$'\r'*) fail "newlines are not allowed in options";; esac
  printf '%s' "$2"
}

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --help|-h) usage; exit 0;;
      --upgrade) upgrade=true; shift; continue;;
      --stage) stage=true; shift; continue;;
      --code) code=$(value_of "$@");;
      --node-id) node_id=$(value_of "$@");;
      --token) token=$(value_of "$@");;
      --ca-sha256) ca_sha256=$(value_of "$@");;
      --registry) registry=$(value_of "$@");;
      --gateway) gateway=$(value_of "$@");;
      --bundle) bundle=$(value_of "$@");;
      --source) source=$(value_of "$@");;
      --version) version=$(value_of "$@");;
      --platform) platform=$(value_of "$@");;
      --user) service_user=$(value_of "$@");;
      --path) cli_path=$(value_of "$@");;
      --root) root=$(value_of "$@");;
      *) fail "unknown option $1";;
    esac
    shift 2
  done
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

# json_str prints the first string value of key in a JSON document on stdin.
# The platform's responses are flat and machine-written, so no JSON tool is needed.
# json_str 打印标准输入 JSON 文档里 key 的第一个字符串值。平台的响应是扁平的机器输出，不需要 JSON 工具。
json_str() {
  sed -n 's/.*"'"$1"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

# fetch downloads an HTTPS URL to a file; plain HTTP and redirects to it are refused.
# fetch 把 HTTPS URL 下载到文件；拒绝明文 HTTP，也拒绝重定向到明文 HTTP。
fetch() {
  curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --connect-timeout 15 --max-time "${3:-600}" --retry 2 "$1" -o "$2" </dev/null
}

detect_platform() {
  case "$(uname -m)" in x86_64|amd64) arch=amd64;; arm64|aarch64) arch=arm64;; *) fail "unsupported CPU $(uname -m)";; esac
  case "$(uname -s)" in
    Linux)
      goos=linux
      if ! $stage && [ "$(id -u)" -ne 0 ]; then
        fail "Ubuntu installation needs root: run the same command with 'sudo bash' in place of 'bash'"
      fi
      if $stage; then state=$root/state; else state=/var/lib/aiserveweave-agent; fi;;
    Darwin)
      goos=darwin
      [ "$(id -u)" -ne 0 ] || fail "run as the intended user, without sudo"
      [ "$arch" = arm64 ] || fail "macOS requires Apple Silicon"
      state=${root:-"$HOME/Library/Application Support/AIServeWeave/Agent"}/state;;
    *) fail "unsupported system $(uname -s); use install.ps1 on Windows";;
  esac
  if $stage && [ -z "$root" ]; then fail "--stage requires --root"; fi
}

# ensure_tools checks the commands agent.sh and this script rely on. Ubuntu
# installs the missing ones; macOS only says what to install.
# ensure_tools 检查本脚本与 agent.sh 依赖的命令。Ubuntu 自动安装缺失项；macOS 只提示该装什么。
ensure_tools() {
  local missing=() tool
  for tool in curl file awk sed; do command -v "$tool" >/dev/null 2>&1 || missing+=("$tool"); done
  if [ -n "$bundle" ]; then command -v unzip >/dev/null 2>&1 || missing+=(unzip); fi
  command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1 || missing+=(coreutils)
  [ "${#missing[@]}" -gt 0 ] || return 0
  if [ "$goos" = linux ] && ! $stage && command -v apt-get >/dev/null 2>&1; then
    log "installing ${missing[*]}"
    apt-get update -qq </dev/null
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${missing[@]}" </dev/null
  else
    fail "missing required tools: ${missing[*]}"
  fi
}

# check_inputs validates the options before anything is downloaded or redeemed.
# check_inputs 在下载或兑换之前校验选项。
check_inputs() {
  local modes=0
  [ -z "$code" ] || modes=$((modes + 1))
  [ -z "$token$node_id$ca_sha256" ] || modes=$((modes + 1))
  [ -z "$bundle" ] || modes=$((modes + 1))
  [ "$modes" -le 1 ] || fail "use only one of --code, --token (with --node-id/--ca-sha256/--registry/--gateway), --bundle"
  if $upgrade; then
    [ "$modes" -eq 0 ] || fail "--upgrade does not take a credential"
    [ -f "$state/agent.yaml" ] || fail "no installed Agent found for --upgrade"
  elif [ -f "$state/agent.yaml" ]; then
    fail "an Agent is already installed here; use --upgrade to update it (a new credential would not replace its identity)"
  fi
  if [ -n "$token$node_id$ca_sha256$registry$gateway" ]; then
    [ -n "$token" ] && [ -n "$node_id" ] && [ -n "$ca_sha256" ] && [ -n "$registry" ] && [ -n "$gateway" ] ||
      fail "token install needs --node-id, --token, --ca-sha256, --registry and --gateway together"
    [[ "$node_id" =~ ^[A-Za-z0-9._-]+$ ]] || fail "invalid --node-id"
    [[ "$ca_sha256" =~ ^[A-Fa-f0-9]{64}$ ]] || fail "--ca-sha256 must be 64 hex characters"
    [[ "$registry" =~ ^[A-Za-z0-9.:-]+$ ]] || fail "invalid --registry"
    [[ "$gateway" =~ ^[A-Za-z0-9.:,-]+$ ]] || fail "invalid --gateway"
  fi
  if [ -n "$code" ]; then [[ "$code" =~ ^[A-Za-z0-9-]{16,40}$ ]] || fail "the install code is malformed"; fi
  case "$platform" in __AISW_*) platform=;; esac
  [ -n "$platform" ] || platform=$PLATFORM_URL
  case "$platform" in __AISW_*) platform=;; esac
  platform=${platform%/}
  [[ "$platform" =~ ^https://[^/?#[:space:]]+$ ]] || fail "--platform must be the platform's https origin, e.g. https://open7x.com"
  case "$source" in platform|github) ;; https://*) source=${source%/};; *) fail "--source must be platform, github or an https:// release directory";; esac
  if [ -n "$bundle" ]; then [ -f "$bundle" ] || fail "enrollment ZIP not found"; fi
}

# resolve_source reads the platform manifest and decides the version and the
# directory the artifacts are downloaded from.
# resolve_source 读取平台 manifest，确定版本与制品下载目录。
resolve_source() {
  fetch "$platform/agent/v1/manifest" "$tmp/manifest.json" 60 || fail "cannot read the platform manifest at $platform/agent/v1/manifest"
  local platform_version repo
  platform_version=$(json_str version <"$tmp/manifest.json")
  [ -n "$platform_version" ] || fail "the platform manifest has no version"
  [ -n "$version" ] || version=$platform_version
  case "$version" in
    *-dirty) fail "version $version was built from a dirty tree; agent.sh does not install it. Rebuild the platform from a clean commit or pick a release with --version";;
    dev) fail "this platform runs a development build; pick a release with --version and --source github";;
  esac
  [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ || "$version" =~ ^[0-9a-f]{7,40}$ ]] || fail "unsupported version $version"
  asset=aiserveweave-agent_${version}_${goos}_${arch}
  case "$source" in
    platform)
      [ "$version" = "$platform_version" ] || fail "the platform only serves $platform_version; use --source github for $version"
      grep -qF "\"name\":\"$asset\"" "$tmp/manifest.json" || fail "the platform has no $goos/$arch build of $version"
      base_url=$platform/agent/download/$version;;
    github)
      repo=$(json_str repo <"$tmp/manifest.json")
      [ -n "$repo" ] || fail "this platform does not name a GitHub repository"
      [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "GitHub Releases only carry exact vX.Y.Z versions"
      base_url=https://github.com/$repo/releases/download/$version;;
    *) base_url=$source;;
  esac
}

# download_verified fetches the Agent, agent.sh and the checksum file, and
# checks both files against the checksum file before anything is redeemed.
# download_verified 下载 Agent、agent.sh 与校验和文件，并在兑换任何凭据之前按校验和文件核对两个文件。
download_verified() {
  log "downloading Agent $version ($goos/$arch)"
  fetch "$base_url/SHA256SUMS.txt" "$tmp/SHA256SUMS.txt" 60 || fail "cannot download SHA256SUMS.txt"
  fetch "$base_url/$asset" "$tmp/$asset" || fail "cannot download $asset"
  fetch "$base_url/agent.sh" "$tmp/agent.sh" 60 || fail "cannot download agent.sh"
  local name expected
  for name in "$asset" agent.sh; do
    expected=$(awk -v name="$name" '$2==name || $2=="*"name {print $1}' "$tmp/SHA256SUMS.txt")
    [[ "$expected" =~ ^[A-Fa-f0-9]{64}$ ]] || fail "SHA256SUMS.txt must list $name exactly once"
    [ "$(printf '%s' "$expected" | tr A-F a-f)" = "$(sha256_of "$tmp/$name")" ] || fail "SHA256 mismatch for $name"
  done
  binary=$tmp/$asset
}

prompt_code() {
  { : </dev/tty; } 2>/dev/null || fail "no terminal to read the install code from; pass --code, --token or --bundle"
  printf 'Install code (input hidden): ' >/dev/tty
  IFS= read -rs code </dev/tty || fail "no install code entered"
  printf '\n' >/dev/tty
  code=${code//[[:space:]]/}
  [[ "$code" =~ ^[A-Za-z0-9-]{16,40}$ ]] || fail "the install code is malformed"
}

# pem_from_json decodes the \n escapes of a PEM stored as a JSON string.
# pem_from_json 还原以 JSON 字符串保存的 PEM 里的 \n 转义。
pem_from_json() { json_str "$1" <"$2" | awk '{gsub(/\\n/, "\n"); printf "%s", $0}'; }

# require_ca_sha256 checks that the CA file hashes to the expected value.
# require_ca_sha256 校验 CA 文件的哈希与期望值一致。
require_ca_sha256() {
  [ "$(sha256_of "$tmp/ca.pem")" = "$(printf '%s' "$1" | tr A-F a-f)" ] || fail "the CA does not match the expected SHA256; refusing to trust it"
}

# redeem trades the install code for the enrollment material. The code travels
# on stdin and is never printed; every refusal looks alike on purpose.
# redeem 用安装码换取接入材料。安装码走标准输入、从不打印；各种拒绝情形刻意长得一样。
redeem() {
  [ -n "$code" ] || prompt_code
  log "redeeming the install code"
  local status
  status=$(printf '{"code":"%s"}' "$code" | curl --silent --show-error --proto '=https' \
    --connect-timeout 15 --max-time 30 -X POST -H 'Content-Type: application/json' --data-binary @- \
    -o "$tmp/redeem.json" -w '%{http_code}' "$platform/agent/v1/redeem") || fail "cannot reach the platform to redeem the install code"
  code=
  case "$status" in
    200) ;;
    404) fail "the install code is invalid, already used or expired; ask for a new one";;
    429) fail "too many attempts; wait a minute and retry";;
    *) fail "the platform could not redeem the install code (HTTP $status); retry later";;
  esac
  node_id=$(json_str node_id <"$tmp/redeem.json")
  registry=$(json_str registry_addr <"$tmp/redeem.json")
  gateway=$(json_str gateway_addr <"$tmp/redeem.json")
  ca_sha256=$(json_str ca_sha256 <"$tmp/redeem.json")
  pem_from_json ca_pem "$tmp/redeem.json" >"$tmp/ca.pem"
  json_str bootstrap_token <"$tmp/redeem.json" >"$tmp/bootstrap-token"
  rm -f "$tmp/redeem.json"
  [ -n "$node_id" ] && [ -s "$tmp/bootstrap-token" ] && [ -s "$tmp/ca.pem" ] && [ -n "$registry" ] && [ -n "$gateway" ] || fail "the platform's reply was incomplete"
  printf '\n' >>"$tmp/bootstrap-token"
  require_ca_sha256 "$ca_sha256"
}

# token_credential fetches the public CA and pins it to the SHA256 the operator supplied.
# token_credential 获取公开 CA，并按运维提供的 SHA256 固定它。
token_credential() {
  fetch "$platform/agent/v1/ca" "$tmp/ca.json" 60 || fail "cannot fetch the CA from the platform"
  pem_from_json ca_pem "$tmp/ca.json" >"$tmp/ca.pem"
  [ -s "$tmp/ca.pem" ] || fail "the platform's CA reply was empty"
  require_ca_sha256 "$ca_sha256"
  printf '%s\n' "$token" >"$tmp/bootstrap-token"
  token=
}

# bundle_credential reads the files of an enrollment ZIP without extracting paths from it.
# bundle_credential 读取接入 ZIP 里的文件，不按 ZIP 内路径解压。
bundle_credential() {
  unzip -p "$bundle" ca.pem >"$tmp/ca.pem" 2>/dev/null || fail "the ZIP has no ca.pem"
  unzip -p "$bundle" bootstrap-token >"$tmp/bootstrap-token" 2>/dev/null || fail "the ZIP has no bootstrap-token"
  unzip -p "$bundle" connection.json >"$tmp/connection.json" 2>/dev/null || fail "the ZIP has no connection.json"
  node_id=$(json_str node_id <"$tmp/connection.json")
  registry=$(json_str registry_addr <"$tmp/connection.json")
  gateway=$(json_str gateway_addr <"$tmp/connection.json")
  ca_sha256=$(json_str ca_sha256 <"$tmp/connection.json")
  [ -n "$node_id" ] && [ -n "$registry" ] && [ -n "$gateway" ] && [ -n "$ca_sha256" ] || fail "connection.json is incomplete"
  [ -s "$tmp/ca.pem" ] && [ -s "$tmp/bootstrap-token" ] || fail "the ZIP's CA or token is empty"
  require_ca_sha256 "$ca_sha256"
}

ensure_service_user() {
  [ "$goos" = linux ] && ! $stage || return 0
  [ -n "$service_user" ] || service_user=aiserveweave
  [[ "$service_user" =~ ^[a-z_][a-z0-9_-]*$ ]] || fail "invalid --user"
  if ! id "$service_user" >/dev/null 2>&1; then
    log "creating service account $service_user"
    useradd --system --create-home --shell /usr/sbin/nologin "$service_user"
  fi
}

run_agent_sh() {
  local args=("$1" --version "$version" --file "$binary" --checksums "$tmp/SHA256SUMS.txt")
  shift
  args+=("$@")
  [ -z "$service_user" ] || args+=(--user "$service_user")
  [ -z "$cli_path" ] || args+=(--path "$cli_path")
  [ -z "$root" ] || args+=(--root "$root")
  ! $stage || args+=(--stage)
  bash "$tmp/agent.sh" "${args[@]}" </dev/null
}

main() {
  parse_args "$@"
  detect_platform
  check_inputs
  ensure_tools
  tmp=$(mktemp -d)
  trap cleanup EXIT
  trap 'exit 130' INT TERM
  resolve_source
  download_verified
  if $upgrade; then
    run_agent_sh upgrade
    log "upgraded to $version"
    return 0
  fi
  if [ -n "$bundle" ]; then bundle_credential
  elif [ -n "$token" ]; then token_credential
  else redeem; fi
  ensure_service_user
  run_agent_sh install --node-id "$node_id" --registry "$registry" --gateway "$gateway" \
    --ca-file "$tmp/ca.pem" --token-file "$tmp/bootstrap-token"
  log "installed Agent $version as node $node_id"
  if ! $stage; then
    bash "$tmp/agent.sh" status ${root:+--root "$root"} </dev/null || true
    echo "Configure inference backends at http://127.0.0.1:8899 on this machine."
    [ "$goos" != linux ] || echo "From another machine: ssh -L 8899:127.0.0.1:8899 USER@THIS_HOST"
    echo "To upgrade later, run this installer again with --upgrade."
  fi
}

main "$@"
