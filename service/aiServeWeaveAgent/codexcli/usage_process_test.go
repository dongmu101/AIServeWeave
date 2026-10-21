//go:build darwin || linux

package codexcli

import (
	"os"
	"testing"
)

// TestReadAccountUsage uses an offline app-server process without starting a turn.
// TestReadAccountUsage 通过离线 app-server 进程查询，不启动推理回合。
func TestReadAccountUsage(t *testing.T) {
	account, err := readAccountUsage(t.Context(), os.Args[0], append(probeEnv(os.Environ()), "AISW_CODEX_FIXTURE=quota"))
	if err != nil || len(account.Windows) != 1 || account.Windows[0].UsedPercent != 25 {
		t.Fatalf("account=%+v error=%v, want primary window at 25%% and nil error", account, err)
	}
}
