#!/bin/sh
# Mirror a service's output to a bounded host log and forward shutdown signals.
# 将服务输出同时写到容器日志与有界宿主机文件，并向服务转发退出信号。
set -eu
umask 077

log_file="${1:?missing log file}"
program="${2:?missing service program}"
shift 2
capture=false
if [ "${1:-}" = "--aisw-capture-logs" ]; then
  capture=true
  shift
fi
# Registry's original entrypoint runs its command through sh -c.
# Registry 原来的入口通过 sh -c 执行命令。
if [ "$program" = /bin/sh ]; then
  set -- -c "$@"
fi
# An overridden Compose command is a one-off operation with its own output.
# Compose 覆盖命令时，运维命令直接执行并保留自己的输出。
if [ "$capture" = false ]; then
  exec "$program" "$@"
fi
set -- "$program" "$@"
: >> "$log_file"

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
mkfifo "$tmp_dir/output"

app_pid=
interrupted=false
pending_signal=
forward() {
  interrupted=true
  pending_signal="$1"
  if [ -n "$app_pid" ]; then
    kill -s "$1" "$app_pid" 2>/dev/null || :
  fi
}
trap 'forward TERM' TERM
trap 'forward INT' INT
trap 'forward HUP' HUP

log_size=$(wc -c < "$log_file")
export AISW_SERVICE_LOG_FILE="$log_file"
LC_ALL=C awk -v size="$log_size" -v limit=10485760 '
  BEGIN { path = ENVIRON["AISW_SERVICE_LOG_FILE"] }
  {
    print
    fflush()
    bytes = length($0) + 1
    if (disabled || bytes > limit) {
      if (bytes > limit) {
        print "Log line exceeds the file size limit; see the container log" > "/dev/stderr"
      }
      next
    }
    if (size + bytes > limit) {
      close(path)
      if (system("p=\"$AISW_SERVICE_LOG_FILE\"; if [ -e \"$p.1\" ]; then mv -f \"$p.1\" \"$p.2\" || exit; fi; mv -f \"$p\" \"$p.1\"")) {
        print "Could not rotate the service log; see the container log" > "/dev/stderr"
        disabled = 1
        next
      }
      size = 0
    }
    print >> path
    fflush(path)
    size += bytes
  }
' < "$tmp_dir/output" &
log_pid=$!

"$@" > "$tmp_dir/output" 2>&1 &
app_pid=$!
[ -z "$pending_signal" ] || kill -s "$pending_signal" "$app_pid" 2>/dev/null || :
while :; do
  status=0
  interrupted=false
  wait "$app_pid" || status=$?
  [ "$interrupted" = true ] || break
done
app_pid=
while :; do
  interrupted=false
  wait "$log_pid" || :
  [ "$interrupted" = true ] || break
done
exit "$status"
