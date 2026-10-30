#!/usr/bin/env bash
# Install and maintain the standalone Agent on Ubuntu and Apple Silicon macOS.
# 在 Ubuntu 与 Apple Silicon macOS 安装及维护独立 Agent。
set -Eeuo pipefail
umask 077

usage() {
  cat <<'USAGE'
Usage: agent.sh install|upgrade|rollback|start|stop|status|reload|uninstall [options]
  --version vX.Y.Z             Exact release, required for install/upgrade/rollback
  --file PATH --checksums PATH Local binary and release SHA256SUMS.txt (no download)
  --base-url HTTPS_URL         Release directory; online mode downloads named assets
  --user USER                  Ubuntu service account (required on first install)
  --path PATH                  Explicit background CLI PATH (saved at installation)
  --node-id ID --registry HOST:PORT --gateway HOST:PORT[,HOST:PORT]
  --ca-file PATH --token-file PATH  First-install enrollment files, not secret values
  --root PATH --stage          Prepare a deployment tree without registering/starting
                              a service. Staging is for inspection, never activation.
  --root PATH                 macOS user installation root override
Uninstall retains state, identities, CLI logins and release backups.
USAGE
}
fail() { echo "agent deployment: $*" >&2; exit 1; }
case "${1:-}" in --help|-h) usage;exit 0;;esac
command_name=${1:-}; [ -n "$command_name" ] || { usage; exit 2; }; shift
version= local_file= checksums= base_url= service_user= cli_path= node_id= registry= gateway= ca_file= token_file= root= stage=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --stage) stage=true; shift; continue;;
    --help|-h) usage; exit 0;;
    --version|--file|--checksums|--base-url|--user|--path|--node-id|--registry|--gateway|--ca-file|--token-file|--root)
      [ "$#" -ge 2 ] || fail "missing value for $1"
      case "$2" in *$'\n'*|*$'\r'*) fail "newlines are not allowed in options";; esac
      case "$1" in
        --version) version=$2;; --file) local_file=$2;; --checksums) checksums=$2;; --base-url) base_url=$2;;
        --user) service_user=$2;; --path) cli_path=$2;; --node-id) node_id=$2;; --registry) registry=$2;;
        --gateway) gateway=$2;; --ca-file) ca_file=$2;; --token-file) token_file=$2;; --root) root=$2;;
      esac; shift 2;;
    *) fail "unknown option $1";;
  esac
done
case "$command_name" in install|upgrade|rollback|start|stop|status|reload|uninstall) ;; *) usage; exit 2;; esac
if $stage;then [ -n "$root" ] || fail "--stage requires an explicit --root";fi
cli_path_option=$cli_path
platform=$(uname -s)
case "$(uname -m)" in x86_64|amd64) arch=amd64;; arm64|aarch64) arch=arm64;; *) fail "unsupported native CPU";; esac
case "$platform" in
  Linux)
    # Read the distribution data as data, not executable shell input.
    # 发行版信息按数据读取，不作为 shell 代码执行。
    distro=$(sed -n 's/^ID=//p' /etc/os-release | tr -d '"')
    release=$(sed -n 's/^VERSION_ID=//p' /etc/os-release | tr -d '"')
    [ "$distro" = ubuntu ] || fail "requires Ubuntu 22.04/24.04/26.04"
    case "$release" in 22.04|24.04|26.04) ;; *) fail "unsupported Ubuntu version $release";; esac
    goos=linux
    if ! $stage; then
      [ "$EUID" -eq 0 ] || fail "Ubuntu installation/maintenance requires sudo"
      [ -z "$root" ] || fail "--root on Ubuntu requires --stage"
      command -v systemctl >/dev/null || fail "systemd is required"
    fi
    if $stage; then [ -n "$root" ] || fail "--stage requires --root"; else root=/usr/local/lib/aiserveweave-agent; fi
    state=/var/lib/aiserveweave-agent; entry=/usr/local/bin/aiserveweave-agent
    if $stage; then state=$root/state;entry=$root/bin/aiserveweave-agent;fi
    unit=$root/aiserveweave-agent.service
    ;;
  Darwin)
    goos=darwin
    [ "$EUID" -ne 0 ] || fail "run as the intended user, without sudo"
    [ "$(sysctl -n hw.optional.arm64)" = 1 ] || fail "requires Apple Silicon"
    arch=arm64
    case "$(sw_vers -productVersion | cut -d. -f1)" in 15|26|27) ;; *) fail "unsupported macOS version";; esac
    root=${root:-"$HOME/Library/Application Support/AIServeWeave/Agent"}
    state=$root/state;entry=$root/bin/aiserveweave-agent
    unit=$root/com.aiserveweave.agent.plist
    ;;
  *) fail "use agent.ps1 on Windows";;
esac
case "$root" in /*) ;; *) fail "installation root must be absolute";; esac
config=$state/agent.yaml
lock=$root/.maintenance-lock
work=
cleanup() { [ -z "$work" ] || rm -rf "$work"; rmdir "$lock" 2>/dev/null || true; }
if $stage && [ -d "$root" ] && [ ! -f "$root/.staging-only" ];then fail "staging refuses an existing non-staging tree";fi
mkdir -p "$root"
if $stage;then touch "$root/.staging-only";elif [ -e "$root/.staging-only" ];then fail "a staged tree cannot be activated; install into a fresh deployment root";fi
if [ "$goos" = linux ] && ! $stage;then
  [ ! -L "$root" ] || fail "installation root must not be a symlink"
  chmod 755 "$root"
fi
mkdir "$lock" 2>/dev/null || fail "another maintenance operation holds $lock"
trap cleanup EXIT
trap 'exit 130' INT TERM
work=$(mktemp -d "$root/.prepare.XXXXXX")

run_agent() {
  if [ "$goos" = linux ] && ! $stage; then
    runuser -u "$service_user" -- env HOME="$user_home" PATH="$cli_path" "$@"
  else "$@"; fi
}
load_environment() {
  if [ "$goos" = linux ]; then
    if [ -f "$root/service-user" ]; then
      saved_user=$(cat "$root/service-user")
      [ -z "$service_user" ] || [ "$saved_user" = "$service_user" ] || fail "changing service user requires an explicit identity migration"
      service_user=$saved_user
    fi
    [ -n "$service_user" ] || fail "--user is required"
    uid=$(id -u "$service_user") || fail "service user does not exist"
    [ "$uid" -ne 0 ] || fail "Agent must run as an ordinary user"
    user_home=$(getent passwd "$service_user" | cut -d: -f6)
    [ -d "$user_home" ] || fail "service user's home directory is missing"
    cli_path=${cli_path:-"$user_home/.local/bin:$user_home/.npm-global/bin:/usr/local/bin:/usr/bin:/bin"}
  else user_home=$HOME;cli_path=${cli_path:-$PATH}; fi
  if [ -f "$root/cli-path" ] && [ -z "$cli_path_option" ]; then
    cli_path=$(cat "$root/cli-path")
  fi
}
control() { run_agent "$entry" -config "$config" -control "$1"; }
service_stop() {
  if $stage; then return; fi
  if [ "$goos" = linux ]; then
    if [ "$(systemctl show -p LoadState --value aiserveweave-agent.service)" != not-found ]; then systemctl stop aiserveweave-agent.service;fi
  else
    old_status=$(control status 2>/dev/null || true)
    old_pid=$(printf '%s' "$old_status" | sed -n 's/.*"pid":\([0-9][0-9]*\).*/\1/p')
    job_info=$(launchctl print "gui/$(id -u)/com.aiserveweave.agent" 2>/dev/null || true)
    if [ -z "$old_pid" ];then old_pid=$(printf '%s\n' "$job_info" | sed -n 's/^[[:space:]]*pid = \([0-9][0-9]*\)$/\1/p' | head -1);fi
    if [ -n "$job_info" ];then launchctl bootout "gui/$(id -u)/com.aiserveweave.agent" || fail "cannot stop LaunchAgent";fi
    if [ -n "$old_pid" ];then
      for ((attempt=0;attempt<25;attempt++));do
        if ! kill -0 "$old_pid" 2>/dev/null;then return;fi
        sleep 1
      done
      fail "old Agent PID did not exit; stable entry was not switched"
    fi
  fi
}
service_start() {
  if $stage; then return; fi
  if [ "$goos" = linux ]; then systemctl start aiserveweave-agent.service
  else launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.aiserveweave.agent.plist"; fi
}
wait_ready() {
  if $stage; then return; fi
  for ((attempt=0;attempt<120;attempt++));do
    if control status >"$work/status" 2>/dev/null && grep -q '"phase":"running"' "$work/status"; then cat "$work/status";return;fi
    sleep 1
  done
  fail "Agent local control did not become ready; inspect logs, then retry or rollback"
}
load_environment
case "$command_name" in
  status)
    if [ "$goos" = linux ];then systemctl --no-pager status aiserveweave-agent.service || true
    else launchctl print "gui/$(id -u)/com.aiserveweave.agent" || true;fi
    control status;exit;;
  stop) service_stop;exit;;
  start) service_start;wait_ready;exit;;
  reload) control reload;exit;;
  uninstall)
    service_stop
    if ! $stage;then
      if [ "$goos" = linux ];then systemctl disable aiserveweave-agent.service;rm -f /etc/systemd/system/aiserveweave-agent.service;systemctl daemon-reload
      else rm -f "$HOME/Library/LaunchAgents/com.aiserveweave.agent.plist";fi
    fi
    rm -f "$entry"
    echo "Uninstalled service; state and release backups retained at $state and $root";exit;;
esac
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]] || fail "--version must be an exact vX.Y.Z release"
asset=aiserveweave-agent_${version}_${goos}_${arch}
release_dir=$root/releases/$version
candidate=$work/$asset
if [ "$command_name" = rollback ]; then
  [ -f "$release_dir/$asset" ] || fail "retained release not found"
  cp "$release_dir/$asset" "$candidate"
  checksums=$release_dir/SHA256SUMS.txt
else
  if [ -n "$local_file" ]; then
    [ -n "$checksums" ] || fail "local installation requires --checksums"
    cp "$local_file" "$candidate"
  else
    [ -z "$checksums" ] || fail "--checksums requires --file"
    [ -n "$base_url" ] || fail "online installation requires --base-url for a trusted exact release directory"
    case "$base_url" in https://*) ;; *) fail "download source must use HTTPS";; esac
    curl --fail --location --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 600 --retry 2 "$base_url/$asset" -o "$candidate"
    checksums=$work/SHA256SUMS.txt
    curl --fail --location --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 60 "$base_url/SHA256SUMS.txt" -o "$checksums"
  fi
fi
expected=$(awk -v name="$asset" '$2==name || $2=="*"name {print $1}' "$checksums")
[[ "$expected" =~ ^[A-Fa-f0-9]{64}$ ]] || fail "checksum manifest must contain exactly one selected asset"
if command -v sha256sum >/dev/null;then actual=$(sha256sum "$candidate" | cut -d' ' -f1);else actual=$(shasum -a 256 "$candidate" | cut -d' ' -f1);fi
[ "$(echo "$expected" | tr A-F a-f)" = "$actual" ] || fail "SHA256 mismatch"
format=$(file -b "$candidate")
case "$goos/$arch:$format" in
  linux/amd64:*ELF*64-bit*x86-64*|linux/arm64:*ELF*64-bit*ARM*aarch64*|darwin/arm64:*Mach-O*64-bit*arm64*) ;;
  *) fail "binary format/architecture mismatch: expected $goos/$arch";;
esac
chmod 755 "$candidate"
# Staging never runs a foreign executable or registers a service.
# 暂存不执行异平台程序，也不注册服务。
if ! $stage;then
  chmod 755 "$work"
  [ "$(run_agent "$candidate" -version)" = "aiserveweave-agent $version" ] || fail "embedded version mismatch"
fi
if [ -f "$release_dir/$asset" ];then cmp -s "$candidate" "$release_dir/$asset" || fail "release version already exists with different bytes";fi
mkdir -p "$root/releases" "$(dirname "$entry")"
if [ "$goos" = linux ] && ! $stage;then
  [ ! -L "$state" ] || fail "state directory must not be a symlink"
  install -d -m 700 -o "$service_user" -g "$(id -gn "$service_user")" "$state"
  run_agent mkdir -p "$state/identity"
  run_agent chmod 700 "$state/identity"
else mkdir -p "$state/identity";chmod 700 "$state" "$state/identity";fi
if [ ! -f "$config" ];then
  [ "$command_name" = install ] || fail "initial installation is required"
  [ -n "$node_id" ] && [ -n "$registry" ] && [ -n "$gateway" ] && [ -f "$ca_file" ] && [ -f "$token_file" ] || fail "first install requires node ID, Registry, Gateway, CA and token files"
  for target in "$state/identity/ca.crt" "$state/identity/bootstrap.token";do [ ! -e "$target" ] || fail "existing enrollment material requires explicit recovery";done
  cp "$ca_file" "$work/ca.crt";cp "$token_file" "$work/bootstrap.token"
  if [ "$goos" = linux ] && ! $stage;then chown "$service_user:$(id -gn "$service_user")" "$work/ca.crt" "$work/bootstrap.token";fi
  run_agent cp "$work/ca.crt" "$state/identity/ca.crt"
  run_agent cp "$work/bootstrap.token" "$state/identity/bootstrap.token"
  run_agent chmod 600 "$state/identity/ca.crt" "$state/identity/bootstrap.token"
  if $stage;then
    echo "Staged verified binary; run install on the target system to initialize YAML."
  else
    run_agent "$candidate" -config "$config" -init-config -node-id "$node_id" -registry "$registry" -gateway "$gateway" -ca-file "$state/identity/ca.crt" -bootstrap-token-file "$state/identity/bootstrap.token" -cert-file "$state/identity/node.crt" -key-file "$state/identity/node.key"
  fi
fi
if ! $stage;then run_agent "$candidate" -config "$config" -check-config;fi
mkdir -p "$release_dir"
if [ ! -f "$release_dir/$asset" ];then cp "$candidate" "$release_dir/$asset";fi
chmod 755 "$release_dir/$asset"
if [ "$goos" = linux ];then chmod 755 "$root" "$root/releases" "$release_dir";fi
printf '%s  %s\n' "$expected" "$asset" >"$release_dir/SHA256SUMS.txt"
if [ -f "$config" ];then
  run_agent mkdir -p "$state/backups"
  run_agent cp -p "$config" "$state/backups/agent.$(date -u +%Y%m%dT%H%M%S).yaml"
fi
printf '%s\n' "$cli_path" >"$root/cli-path"
# Quote generated service data without evaluating shell fragments.
# 引用生成的服务数据，不求值 shell 片段。
systemd_value() { local value=$1;value=${value//\\/\\\\};value=${value//\"/\\\"};value=${value//%/%%};printf '"%s"' "$value"; }
xml_value() { printf '%s' "$1" | sed -e 's/\&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g' -e "s/'/\&apos;/g"; }
if [ "$goos" = linux ];then
  printf '%s\n' "$service_user" >"$root/service-user"
  {
    echo '[Unit]';echo 'Description=AIServeWeave Agent';echo 'Wants=network-online.target';echo 'After=network-online.target'
    echo '[Service]';echo 'Type=simple';printf 'User=%s\n' "$service_user"
    printf 'WorkingDirectory=%s\n' "$(systemd_value "$state")"
    printf 'Environment=%s %s\n' "$(systemd_value "HOME=$user_home")" "$(systemd_value "PATH=$cli_path")"
    echo 'EnvironmentFile=-/etc/aiserveweave-agent/agent.env'
    printf 'ExecStart=%s -config %s\n' "$(systemd_value "$entry")" "$(systemd_value "$config")"
    printf 'ExecReload=%s -config %s -control reload\n' "$(systemd_value "$entry")" "$(systemd_value "$config")"
    echo 'Restart=on-failure';echo 'RestartSec=5';echo 'TimeoutStopSec=25';echo 'KillMode=control-group';echo 'UMask=0077'
    echo '[Install]';echo 'WantedBy=multi-user.target'
  } >"$unit"
else
  if [ ! -f "$root/environment.json" ];then printf '{}\n' >"$root/environment.json";chmod 600 "$root/environment.json";fi
  logdir="$HOME/Library/Logs/AIServeWeave/Agent"
  if $stage;then logdir=$root/logs;fi
  mkdir -p "$logdir"
  {
    echo '<?xml version="1.0" encoding="UTF-8"?>';echo '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">';echo '<plist version="1.0"><dict>'
    echo '<key>Label</key><string>com.aiserveweave.agent</string><key>ProgramArguments</key><array>'
    for arg in "$entry" -config "$config" -env-file "$root/environment.json" -log-file "$logdir/agent.log";do printf '<string>%s</string>\n' "$(xml_value "$arg")";done
    echo '</array><key>EnvironmentVariables</key><dict>'
    printf '<key>HOME</key><string>%s</string><key>PATH</key><string>%s</string>\n' "$(xml_value "$user_home")" "$(xml_value "$cli_path")"
    echo '</dict><key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>ThrottleInterval</key><integer>15</integer><key>ExitTimeOut</key><integer>25</integer><key>ProcessType</key><string>Background</string></dict></plist>'
  } >"$unit"
fi
service_stop
ln -s "$release_dir/$asset" "$work/next"
mv -f "$work/next" "$entry"
if ! $stage;then
  if [ "$goos" = linux ];then
    mkdir -p /etc/aiserveweave-agent;chmod 700 /etc/aiserveweave-agent
    if [ ! -f /etc/aiserveweave-agent/agent.env ];then install -m 600 /dev/null /etc/aiserveweave-agent/agent.env;fi
    install -m 644 "$unit" /etc/systemd/system/aiserveweave-agent.service
    systemctl daemon-reload;systemctl enable aiserveweave-agent.service
  else
    mkdir -p "$HOME/Library/LaunchAgents";cp "$unit" "$HOME/Library/LaunchAgents/com.aiserveweave.agent.plist"
  fi
fi
service_start
wait_ready
echo "$command_name complete: $version ($goos/$arch). Node connectivity and real requests require separate acceptance."
