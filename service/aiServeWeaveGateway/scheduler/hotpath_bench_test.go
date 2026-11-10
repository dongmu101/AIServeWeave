package scheduler

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/routing"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// Hot-path benchmarks for node selection and the model catalogue. They run a
// fleet of benchNodes nodes, each reporting benchRuntimes runtime instances,
// which is the scale at which a per-request walk over the node inventory
// starts to matter.
//
// 节点选择与模型目录的热路径基准。它们模拟一个由 benchNodes 个节点组成的集群，每个
// 节点上报 benchRuntimes 个运行时实例——这个规模下，每请求遍历一遍节点清单的代价
// 才开始显现。
const (
	benchNodes    = 200
	benchRuntimes = 4
	benchModels   = 6
	benchAliases  = 20
)

type benchFleet struct {
	h        *gatewaytest.Harness
	sched    *Scheduler
	controls []*gatewaytest.Control
}

// benchSnapshots builds the inventory one node reports; salt varies the
// queue figures between reports so a re-report is a real change.
//
// benchSnapshots 构造一个节点上报的清单；salt 让每次上报的队列数据不同，使重复上报
// 成为真实变化。
func benchSnapshots(node, salt int) []runtime.Snapshot {
	snaps := make([]runtime.Snapshot, 0, benchRuntimes)
	for r := 0; r < benchRuntimes; r++ {
		models := make([]runtime.Model, 0, benchModels)
		for m := 0; m < benchModels; m++ {
			models = append(models, runtime.Model{
				ID: fmt.Sprintf("m%d", m),
				Capabilities: runtime.CapabilitySet{
					runtime.CapabilityChat:       {Level: runtime.SupportSupported},
					runtime.CapabilityChatStream: {Level: runtime.SupportSupported},
					runtime.CapabilityMessages:   {Level: runtime.SupportSupported},
				},
			})
		}
		snaps = append(snaps, runtime.Snapshot{
			Descriptor: runtime.Descriptor{ID: fmt.Sprintf("rt%d", r), Kind: runtime.KindOllama, BaseURL: "http://127.0.0.1:11434", MaxConcurrent: 4},
			State:      runtime.StateHealthy,
			Health:     runtime.HealthReport{QueueRunning: (node + r + salt) % 3},
			Discovery:  runtime.Discovery{Models: models},
		})
	}
	return snaps
}

func newBenchFleet(b *testing.B) *benchFleet {
	b.Helper()
	// gatewaytest.Harness is written against *testing.T, which a benchmark
	// does not have. Nothing here fails a test, so a detached T is enough to
	// satisfy its Helper calls; the harness is closed by hand below.
	//
	// gatewaytest.Harness 以 *testing.T 编写，而基准没有它。这里不会令测试失败，所以
	// 一个游离的 T 足以满足它的 Helper 调用；harness 在下面手动关闭。
	t := new(testing.T)
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	fleet := &benchFleet{h: h}
	b.Cleanup(h.Close)

	for i := 0; i < benchNodes; i++ {
		id := fmt.Sprintf("node-%03d", i)
		c := h.StartControl(id, &tunnelv1.Hello{
			NodeId:       id,
			AgentVersion: "bench",
			RuntimeIds:   []string{"rt0", "rt1", "rt2", "rt3"},
			Labels:       map[string]string{"zone": fmt.Sprintf("z%d", i%4), "tier": "gpu"},
		})
		if c.Expect(t).GetAck() == nil {
			b.Fatal("no HelloAck")
		}
		fleet.controls = append(fleet.controls, c)
		fleet.report(i, 0, true)
		for s := 0; s < 2; s++ {
			h.OpenSlot(id, tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, fmt.Sprintf("%s-slot-%d", id, s), nil)
		}
	}
	gatewaytest.WaitFor(t, "fleet to settle", func() bool {
		for _, n := range h.Srv.Nodes() {
			if len(n.Runtimes) != benchRuntimes || n.IdleSlots[tunnelv1.SlotClass_SLOT_CLASS_INFERENCE] != 2 {
				return false
			}
		}
		return len(h.Srv.Nodes()) == benchNodes
	})

	routes := make([]routing.Route, 0, benchAliases)
	for i := 0; i < benchAliases; i++ {
		routes = append(routes, routing.Route{Model: fmt.Sprintf("alias-%02d", i), Targets: []routing.Target{
			{RuntimeModel: fmt.Sprintf("m%d", i%benchModels), Priority: 0, Weight: 1},
			{RuntimeModel: fmt.Sprintf("m%d", (i+1)%benchModels), Priority: 1, Weight: 1},
		}})
	}
	table, err := routing.New(routes)
	if err != nil {
		b.Fatalf("routing.New: %v", err)
	}
	fleet.sched = New(h.Srv, Config{Clock: h.Clock, Routes: table})
	return fleet
}

// report sends node i's runtime inventory, the write that races with the
// scheduler's reads in production.
//
// report 发送第 i 个节点的运行时清单，也就是生产环境里与调度器读路径相互竞争的那次写入。
func (f *benchFleet) report(i, salt int, full bool) {
	_ = f.controls[i].Stream.FromAgent(&tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Full:       full,
		ReportedAt: timestamppb.New(f.h.Clock.Now()),
		Snapshots:  tunnelwire.SnapshotsToProto(benchSnapshots(i, salt)),
	}}})
}

// BenchmarkPickCandidates is one routed request's candidate computation: the
// "alias-00" route has two targets, so it walks the fleet twice.
//
// BenchmarkPickCandidates 是一次路由请求的候选计算："alias-00" 有两个 target，所以
// 会遍历集群两次。
func BenchmarkPickCandidates(b *testing.B) {
	f := newBenchFleet(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := f.sched.candidates(ctx, "alias-00", runtime.CapabilityChat); len(got) == 0 {
			b.Fatal("no candidates")
		}
	}
}

// BenchmarkPickCandidatesUnrouted is the single-target case: a model used as
// the node advertises it.
//
// BenchmarkPickCandidatesUnrouted 是单 target 的情形：按节点声明的原样使用的模型。
func BenchmarkPickCandidatesUnrouted(b *testing.B) {
	f := newBenchFleet(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := f.sched.candidates(ctx, "m0", runtime.CapabilityChat); len(got) == 0 {
			b.Fatal("no candidates")
		}
	}
}

// BenchmarkPickCandidatesWithStatusWrites runs the pick on every CPU while a
// writer keeps re-reporting runtime inventories, so the cost of contending
// with the write path on node.mu is visible.
//
// BenchmarkPickCandidatesWithStatusWrites 在所有 CPU 上并发执行 pick，同时一个写入者
// 不断重新上报运行时清单，使与写路径争用 node.mu 的代价可见。
func BenchmarkPickCandidatesWithStatusWrites(b *testing.B) {
	f := newBenchFleet(b)
	ctx := context.Background()

	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for salt := 1; !stop.Load(); salt++ {
			for i := 0; i < benchNodes && !stop.Load(); i++ {
				f.report(i, salt, false)
			}
		}
	}()
	b.Cleanup(func() { stop.Store(true); wg.Wait() })

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if got := f.sched.candidates(ctx, "alias-00", runtime.CapabilityChat); len(got) == 0 {
				b.Error("no candidates")
				return
			}
		}
	})
}

// BenchmarkModels is the /v1/models catalogue with a routing table: aliases x
// up to three capability probes x nodes.
//
// BenchmarkModels 是带路由表的 /v1/models 目录：别名数 x 至多三次能力探测 x 节点数。
func BenchmarkModels(b *testing.B) {
	f := newBenchFleet(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := f.sched.Models(ctx); len(got) != benchAliases {
			b.Fatalf("models = %d, want %d", len(got), benchAliases)
		}
	}
}

// BenchmarkModelsUnrouted is the catalogue without a routing table, a single
// walk over the fleet.
//
// BenchmarkModelsUnrouted 是没有路由表的目录，只遍历一遍集群。
func BenchmarkModelsUnrouted(b *testing.B) {
	f := newBenchFleet(b)
	f.sched.SetRoutes(nil)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := f.sched.Models(ctx); len(got) != benchModels {
			b.Fatalf("models = %d, want %d", len(got), benchModels)
		}
	}
}
