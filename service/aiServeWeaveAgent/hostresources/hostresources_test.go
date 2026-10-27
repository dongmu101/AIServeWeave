package hostresources

import (
	"context"
	goruntime "runtime"
	"testing"
)

func TestParseProcMemInfo(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    int64
		wantErr bool
	}{
		{
			name: "typical meminfo",
			data: "MemTotal:       16384000 kB\nMemFree:         1000000 kB\n",
			want: 16384000 * 1024,
		},
		{
			name: "MemTotal not first line",
			data: "MemFree: 1000 kB\nMemTotal: 2048 kB\nMemAvailable: 500 kB\n",
			want: 2048 * 1024,
		},
		{
			name:    "missing MemTotal",
			data:    "MemFree: 1000 kB\n",
			wantErr: true,
		},
		{
			name:    "malformed MemTotal line",
			data:    "MemTotal:\n",
			wantErr: true,
		},
		{
			name:    "non-numeric MemTotal value",
			data:    "MemTotal: abc kB\n",
			wantErr: true,
		},
		{
			name:    "empty input",
			data:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProcMemInfo([]byte(tt.data))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseProcMemInfo(%q) = %d, nil; want error", tt.data, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseProcMemInfo(%q) unexpected error: %v", tt.data, err)
			}
			if got != tt.want {
				t.Errorf("parseProcMemInfo(%q) = %d, want %d", tt.data, got, tt.want)
			}
		})
	}
}

func TestParseNvidiaSMIMemoryTotal(t *testing.T) {
	tests := []struct {
		name      string
		out       string
		wantCount int32
		wantBytes int64
		wantErr   bool
	}{
		{
			name:      "single gpu",
			out:       "24576\n",
			wantCount: 1,
			wantBytes: 24576 * 1024 * 1024,
		},
		{
			name:      "multiple gpus summed",
			out:       "24576\n24576\n",
			wantCount: 2,
			wantBytes: 2 * 24576 * 1024 * 1024,
		},
		{
			name:      "trailing blank lines ignored",
			out:       "24576\n\n",
			wantCount: 1,
			wantBytes: 24576 * 1024 * 1024,
		},
		{
			name:    "no gpus reported",
			out:     "",
			wantErr: true,
		},
		{
			name:    "malformed line",
			out:     "not-a-number\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count, bytes, err := parseNvidiaSMIMemoryTotal(tt.out)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseNvidiaSMIMemoryTotal(%q) = (%d, %d), nil; want error", tt.out, count, bytes)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseNvidiaSMIMemoryTotal(%q) unexpected error: %v", tt.out, err)
			}
			if count != tt.wantCount || bytes != tt.wantBytes {
				t.Errorf("parseNvidiaSMIMemoryTotal(%q) = (%d, %d), want (%d, %d)", tt.out, count, bytes, tt.wantCount, tt.wantBytes)
			}
		})
	}
}

func TestRunCommand(t *testing.T) {
	t.Run("captures stdout", func(t *testing.T) {
		out, err := runCommand(context.Background(), "echo", "-n", "hello")
		if err != nil {
			t.Fatalf("runCommand(echo) unexpected error: %v", err)
		}
		if out != "hello" {
			t.Errorf("runCommand(echo) = %q, want %q", out, "hello")
		}
	})

	t.Run("missing binary returns error", func(t *testing.T) {
		_, err := runCommand(context.Background(), "aiserveweave-definitely-not-a-real-command")
		if err == nil {
			t.Fatal("runCommand(missing binary) = nil error, want error")
		}
	})
}

// TestDetect only asserts the fields Detect can always determine from the Go
// runtime itself (CPU cores, OS, Arch); memory and GPU detection depend on
// what tools this test machine happens to have installed, so their presence
// is not part of the contract this test verifies.
func TestDetect(t *testing.T) {
	res := Detect(context.Background(), nil)
	if res == nil {
		t.Fatal("Detect() = nil")
	}
	if res.GetCpuCores() != int32(goruntime.NumCPU()) {
		t.Errorf("Detect().CpuCores = %d, want %d", res.GetCpuCores(), goruntime.NumCPU())
	}
	if res.GetOs() != goruntime.GOOS {
		t.Errorf("Detect().Os = %q, want %q", res.GetOs(), goruntime.GOOS)
	}
	if res.GetArch() != goruntime.GOARCH {
		t.Errorf("Detect().Arch = %q, want %q", res.GetArch(), goruntime.GOARCH)
	}
}
