package hostresources

import (
	"encoding/binary"
	"errors"
	"math"
	goruntime "runtime"
	"testing"
)

func TestParseProcStat(t *testing.T) {
	data := []byte("cpu  100 10 50 800 40 0 0 0 7 0\ncpu0 1 2 3 4 5 6 7 8\nintr 1\n")
	got, err := parseProcStat(data)
	if err != nil {
		t.Fatal(err)
	}
	// total = user..steal = 1000; idle = idle+iowait = 840; guest not re-added.
	if got.total != 1000 || got.busy != 160 {
		t.Fatalf("got %+v, want busy 160 total 1000", got)
	}
	if _, err := parseProcStat([]byte("intr 1\n")); err == nil {
		t.Fatal("missing cpu line must error")
	}
	if _, err := parseProcStat([]byte("cpu 1 x 3 4\n")); err == nil {
		t.Fatal("non-numeric field must error")
	}
}

func TestCPUUtilizationDelta(t *testing.T) {
	share, ok := cpuUtilization(cpuTimes{busy: 100, total: 1000}, cpuTimes{busy: 150, total: 1100})
	if !ok || math.Abs(share-0.5) > 1e-9 {
		t.Fatalf("share = %v ok=%v, want 0.5", share, ok)
	}
	if _, ok := cpuUtilization(cpuTimes{busy: 10, total: 100}, cpuTimes{busy: 10, total: 100}); ok {
		t.Fatal("no progress must not yield a figure")
	}
	if _, ok := cpuUtilization(cpuTimes{busy: 10, total: 100}, cpuTimes{busy: 5, total: 200}); ok {
		t.Fatal("a counter reset must not yield a figure")
	}
}

func TestObserveCPUNeedsTwoSamples(t *testing.T) {
	r := NewUsageReader()
	if got := r.observeCPU(cpuTimes{busy: 10, total: 100}); got != nil {
		t.Fatalf("first sample must have no utilization, got %v", *got)
	}
	got := r.observeCPU(cpuTimes{busy: 35, total: 200})
	if got == nil || math.Abs(*got-0.25) > 1e-9 {
		t.Fatalf("second sample = %v, want 0.25", got)
	}
}

func TestParseMemInfoUsage(t *testing.T) {
	avail, total, err := parseMemInfoUsage([]byte("MemTotal: 1000 kB\nMemFree: 100 kB\nMemAvailable: 600 kB\nCached: 300 kB\n"))
	if err != nil || avail != 600*1024 || total != 1000*1024 {
		t.Fatalf("got %d/%d %v", avail, total, err)
	}
	avail, _, err = parseMemInfoUsage([]byte("MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 300 kB\n"))
	if err != nil || avail != 450*1024 {
		t.Fatalf("fallback avail = %d %v, want %d", avail, err, 450*1024)
	}
	if _, _, err := parseMemInfoUsage([]byte("MemFree: 1 kB\n")); err == nil {
		t.Fatal("missing MemTotal must error")
	}
}

func TestParseLoadAvg(t *testing.T) {
	got, err := parseLoadAvg([]byte("1.25 0.80 0.50 2/300 4242\n"))
	if err != nil || got != 1.25 {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := parseLoadAvg(nil); err == nil {
		t.Fatal("empty loadavg must error")
	}
}

func TestCgroupMemory(t *testing.T) {
	// 1 GiB limit, 900 MiB charged, of which 600 MiB is file cache (100 MiB
	// shmem) and 50 MiB reclaimable slab: used = 900 - (600-100) - 50 = 350.
	stat := []byte("anon 300000000\nfile 629145600\nshmem 104857600\nslab_reclaimable 52428800\ninactive_file 1\n")
	avail, total, ok := cgroupMemory([]byte("1073741824\n"), []byte("943718400\n"), stat, 16<<30)
	if !ok || total != 1<<30 || avail != (1024-350)<<20 {
		t.Fatalf("got avail %d total %d ok %v, want %d", avail, total, ok, (1024-350)<<20)
	}
	// Without the aggregate "file", inactive_file is the fallback.
	avail, _, ok = cgroupMemory([]byte("1073741824\n"), []byte("805306368\n"), []byte("inactive_file 268435456\n"), 16<<30)
	if !ok || avail != 512<<20 {
		t.Fatalf("fallback avail %d, want %d", avail, 512<<20)
	}
	if _, _, ok := cgroupMemory([]byte("max\n"), []byte("1\n"), nil, 16<<30); ok {
		t.Fatal("unlimited cgroup must not narrow host memory")
	}
	if _, _, ok := cgroupMemory([]byte("34359738368\n"), []byte("1\n"), nil, 16<<30); ok {
		t.Fatal("a limit above the host total must not narrow host memory")
	}
	avail, _, ok = cgroupMemory([]byte("100\n"), []byte("500\n"), nil, 1000)
	if !ok || avail != 0 {
		t.Fatalf("usage above limit must clamp available to 0, got %d", avail)
	}
}

func TestCgroupMemoryDirPicksTheTightestAncestor(t *testing.T) {
	files := map[string]string{
		"/sys/fs/cgroup/memory.max":                                 "max",
		"/sys/fs/cgroup/system.slice/memory.max":                    "8589934592",
		"/sys/fs/cgroup/system.slice/aisw-agent.service/memory.max": "2147483648",
	}
	read := func(path string) ([]byte, error) {
		if v, ok := files[path]; ok {
			return []byte(v + "\n"), nil
		}
		return nil, errors.New("missing")
	}
	self := []byte("0::/system.slice/aisw-agent.service\n")
	if got := cgroupMemoryDir("/sys/fs/cgroup", self, read); got != "/sys/fs/cgroup/system.slice/aisw-agent.service" {
		t.Fatalf("dir = %q, want the service's own cgroup", got)
	}
	files["/sys/fs/cgroup/system.slice/memory.max"] = "1073741824"
	if got := cgroupMemoryDir("/sys/fs/cgroup", self, read); got != "/sys/fs/cgroup/system.slice" {
		t.Fatalf("dir = %q, want the tighter parent", got)
	}
	// Inside a cgroup namespace the path is "/" and the limit sits at root.
	files["/sys/fs/cgroup/memory.max"] = "536870912"
	if got := cgroupMemoryDir("/sys/fs/cgroup", []byte("0::/\n"), read); got != "/sys/fs/cgroup" {
		t.Fatalf("dir = %q, want root", got)
	}
	if got := cgroupMemoryDir("/sys/fs/cgroup", nil, func(string) ([]byte, error) { return nil, errors.New("x") }); got != "" {
		t.Fatalf("dir = %q, want none without any limit", got)
	}
}

func TestParseDarwinLoadavg(t *testing.T) {
	raw := make([]byte, 24)
	binary.LittleEndian.PutUint32(raw[0:4], 2048*3) // 3.0 at fscale 2048
	binary.LittleEndian.PutUint64(raw[16:24], 2048)
	got, err := parseDarwinLoadavg(raw)
	if err != nil || got != 3 {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := parseDarwinLoadavg(raw[:8]); err == nil {
		t.Fatal("short struct must error")
	}
	if _, err := parseDarwinLoadavg(make([]byte, 24)); err == nil {
		t.Fatal("zero scale must error")
	}
}

func TestDarwinMemory(t *testing.T) {
	avail, total := darwinMemory(16<<30, 35)
	if total != 16<<30 || avail != (16<<30)/100*35 {
		t.Fatalf("got %d/%d", avail, total)
	}
	if avail, _ := darwinMemory(100, 250); avail != 100 {
		t.Fatalf("level above 100 must clamp, got %d", avail)
	}
}

func TestEstimatedCPU(t *testing.T) {
	cpus := float64(goruntime.NumCPU())
	if got := estimatedCPU(cpus / 2); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("got %v want 0.5", got)
	}
	if got := estimatedCPU(cpus * 10); got != 1 {
		t.Fatalf("overload must clamp to 1, got %v", got)
	}
}

// TestUsageReaderLive is a smoke test of the real platform probe: on the
// platforms with an implementation, memory must be measurable.
func TestUsageReaderLive(t *testing.T) {
	r := NewUsageReader()
	u, err := r.Read()
	if errors.Is(err, ErrUsageUnsupported) {
		t.Skip("no live usage probe on this platform")
	}
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if goruntime.GOOS == "linux" || goruntime.GOOS == "darwin" || goruntime.GOOS == "windows" {
		if u.MemoryTotalBytes == nil || *u.MemoryTotalBytes <= 0 {
			t.Fatalf("memory total not measured: %+v", u)
		}
		if u.MemoryAvailableBytes != nil && *u.MemoryAvailableBytes > *u.MemoryTotalBytes {
			t.Fatalf("available %d exceeds total %d", *u.MemoryAvailableBytes, *u.MemoryTotalBytes)
		}
	}
	if u.CPUUtilization != nil && (*u.CPUUtilization < 0 || *u.CPUUtilization > 1) {
		t.Fatalf("cpu out of range: %v", *u.CPUUtilization)
	}
	t.Logf("usage: cpu=%v estimated=%v load1=%v mem=%v/%v", deref(u.CPUUtilization), u.CPUEstimated,
		deref(u.LoadAverage1m), deref(u.MemoryAvailableBytes), deref(u.MemoryTotalBytes))
}

func deref[T any](p *T) any {
	if p == nil {
		return "n/a"
	}
	return *p
}
