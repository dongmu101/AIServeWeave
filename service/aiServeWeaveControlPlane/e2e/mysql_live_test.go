package e2e_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/service"
	"github.com/zeromicro/go-zero/rest"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/handler"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/token"
	"AIServeWeave/service/aiServeWeaveGateway/controlplaneclient"
)

// mysqlDSNEnv names the same environment variable
// internal/store/gormstore/mysql_live_test.go gates on. It is redeclared
// here rather than imported: that file's constant is unexported in a
// different package, and this one only needs the string, not anything else
// from that package.
//
// mysqlDSNEnv 与 internal/store/gormstore/mysql_live_test.go 所依据的是同一个
// 环境变量名。这里重新声明而不是导入：那个常量在另一个包里是未导出的，而这里
// 只需要这个字符串，不需要那个包的其他任何东西。
const mysqlDSNEnv = "AISW_MYSQL_TEST_DSN"

// newLiveMySQLHarness starts a real go-zero control plane backed by a real
// MySQL 9.7 database, for the full-stack scenarios harness_test.go's
// memstore-backed harness structurally cannot exercise: STATUS.md's J08
// calls out "Gateway 重启" and "多副本查询" specifically, and both are about
// what a second, independent process sees through the database — a
// per-process fake has nothing to disagree about with itself.
//
// It reuses the harness type's call/waitReady methods but not
// newHarnessWith's construction, since that function is hard-wired to
// memstore.New(); duplicating the handful of lines that differ was less
// risk to the shared helper every other test in this package depends on
// than parameterizing it for a store this file is the only user of.
//
// newLiveMySQLHarness 启动一个由真实 MySQL 9.7 数据库支撑的真实 go-zero 控制面，
// 用于 harness_test.go 那个基于 memstore 的 harness 在结构上就无法覆盖的全栈场景：
// STATUS.md 的 J08 专门点名了「Gateway 重启」与「多副本查询」，两者说的都是
// 第二个、独立的进程透过数据库看到了什么——一个进程内的假件，没有什么可以拿来
// 跟自己意见相左。
//
// 它复用了 harness 类型的 call/waitReady 方法，但没有复用 newHarnessWith 的
// 构造逻辑，因为那个函数写死了 memstore.New()；把这几行不同的地方复制一份，
// 比把它参数化成本文件唯一用户所需的样子，对本包其余每个测试都依赖的这个共享
// helper 造成的风险更小。
func newLiveMySQLHarness(t *testing.T) *harness {
	t.Helper()
	dsn := os.Getenv(mysqlDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to run this test against a real MySQL 9.7 instance", mysqlDSNEnv)
	}

	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{
		Logger:         gormlogger.Default.LogMode(gormlogger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("connecting to %s: %v", mysqlDSNEnv, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB(): %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	st := gormstore.New(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := st.MigrateJobs(ctx); err != nil {
		t.Fatalf("MigrateJobs: %v", err)
	}
	// None of these tables carry a database-level foreign key to another —
	// every cross-table join in this service happens in Go, not in the
	// schema — so a plain TRUNCATE per table is enough; there is no
	// constraint order to respect.
	//
	// 这些表之间没有任何一个持有数据库层面的外键——本服务里跨表的关联都发生在
	// Go 代码里，而不是 schema 里——因此逐表一次朴素的 TRUNCATE 就够了，不存在
	// 需要遵循的约束顺序。
	for _, table := range []string{"jobs", "job_artifacts", "audit_logs", "api_keys", "users", "tenants"} {
		if err := db.Exec("TRUNCATE TABLE " + table).Error; err != nil {
			t.Fatalf("truncating %s: %v", table, err)
		}
	}

	port := freePort(t)
	cfg := config.Config{
		RestConf: rest.RestConf{
			ServiceConf: service.ServiceConf{
				Name: "controlplane-e2e-mysql",
				Log:  logx.LogConf{Mode: "console", Level: "severe"},
			},
			Host: "127.0.0.1",
			Port: port,
		},
		Auth:           config.AuthConf{AccessSecret: accessSecret, AccessExpire: time.Hour},
		InternalToken:  internalToken,
		BootstrapToken: bootstrapToken,
	}
	issuer, err := token.NewIssuer(accessSecret, time.Hour, runtime.NewSystemClock())
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	svcCtx := &svc.ServiceContext{
		Config: cfg,
		Logic:  logic.New(st, runtime.NewSystemClock()),
		Issuer: issuer,
	}

	server, err := rest.NewServer(cfg.RestConf)
	if err != nil {
		t.Fatalf("rest.NewServer: %v", err)
	}
	handler.RegisterHandlers(server, svcCtx)
	go server.Start()
	t.Cleanup(server.Stop)

	h := &harness{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", port), server: server}
	h.waitReady()
	return h
}

// TestLiveMySQLGatewayRestartRecoversJobRoute is J08's coverage for
// "Gateway 重启": a job created by "replica 1" (one JobsClient) is read back
// by "replica 2" (a second, independent JobsClient standing in for the same
// Gateway process after a restart), which is only possible because the
// route binding survived in real MySQL, not in either client's memory.
//
// TestLiveMySQLGatewayRestartRecoversJobRoute 是 J08 对「Gateway 重启」的
// 覆盖：由"副本 1"（一个 JobsClient）创建的 job，被"副本 2"（另一个独立的
// JobsClient，代表同一个 Gateway 进程重启之后）读回——这之所以可能，只是因为
// 路由绑定真实地存活在 MySQL 里，而不在任何一个客户端的内存中。
func TestLiveMySQLGatewayRestartRecoversJobRoute(t *testing.T) {
	h := newLiveMySQLHarness(t)

	replica1, err := controlplaneclient.NewJobsClient(controlplaneclient.JobsClientConfig{Endpoint: h.base, Token: internalToken})
	if err != nil {
		t.Fatalf("NewJobsClient(replica1): %v", err)
	}
	ctx := context.Background()
	if _, err := replica1.CreateJob(ctx, controlplaneclient.CreateJobRequest{
		JobID: "job_restart_1", TenantID: "tenant-a", WorkflowID: "text-to-image",
		NodeID: "node-1", RuntimeID: "comfy-1", BackendRunID: "prompt-1", State: "running",
	}); err != nil {
		t.Fatalf("CreateJob via replica1: %v", err)
	}

	// "replica1" is gone; nothing here reuses its process, its client, or
	// any in-memory state. replica2 is a fresh client pointed at the same
	// control plane, standing in for the Gateway replica that restarted.
	//
	// "replica1"已经不在了；这里没有任何东西复用它的进程、它的客户端，或任何
	// 内存状态。replica2 是一个全新的客户端，指向同一个控制面，代表那个已经
	// 重启过的 Gateway 副本。
	replica2, err := controlplaneclient.NewJobsClient(controlplaneclient.JobsClientConfig{Endpoint: h.base, Token: internalToken})
	if err != nil {
		t.Fatalf("NewJobsClient(replica2): %v", err)
	}
	recovered, err := replica2.ListActiveJobsForRoute(ctx, "node-1", "comfy-1")
	if err != nil {
		t.Fatalf("ListActiveJobsForRoute via replica2: %v", err)
	}
	found := false
	for _, j := range recovered {
		if j.JobID == "job_restart_1" {
			found = true
			if j.BackendRunID != "prompt-1" {
				t.Errorf("recovered job's BackendRunID = %q, want prompt-1", j.BackendRunID)
			}
		}
	}
	if !found {
		t.Fatal("replica2 could not recover job_restart_1's route binding after \"replica1\" restarted")
	}
}

// TestLiveMySQLMultiReplicaVisibilityAndConcurrentStateUpdates is J08's
// coverage for "多副本查询": two Gateway replicas that both happen to be
// connected to the same node concurrently observe and persist state for the
// same job — real MySQL's ObservedSeq gate, not any coordination between
// them, is what keeps the result coherent.
//
// TestLiveMySQLMultiReplicaVisibilityAndConcurrentStateUpdates 是 J08 对
// 「多副本查询」的覆盖：两个恰好同时连接到同一个节点的 Gateway 副本，各自
// 观测并持久化同一个 job 的状态——让结果保持一致的是真实 MySQL 的 ObservedSeq
// 门槛，不是它们之间的任何协调。
func TestLiveMySQLMultiReplicaVisibilityAndConcurrentStateUpdates(t *testing.T) {
	h := newLiveMySQLHarness(t)
	ctx := context.Background()

	replicaA, err := controlplaneclient.NewJobsClient(controlplaneclient.JobsClientConfig{Endpoint: h.base, Token: internalToken})
	if err != nil {
		t.Fatalf("NewJobsClient(replicaA): %v", err)
	}
	replicaB, err := controlplaneclient.NewJobsClient(controlplaneclient.JobsClientConfig{Endpoint: h.base, Token: internalToken})
	if err != nil {
		t.Fatalf("NewJobsClient(replicaB): %v", err)
	}

	if _, err := replicaA.CreateJob(ctx, controlplaneclient.CreateJobRequest{
		JobID: "job_multi_1", TenantID: "tenant-a", WorkflowID: "wf",
		NodeID: "node-shared", RuntimeID: "comfy-shared", BackendRunID: "prompt-1", State: "pending",
	}); err != nil {
		t.Fatalf("CreateJob via replicaA: %v", err)
	}

	// replicaB sees it immediately — no cache, no delay, because both talk
	// to the same database rather than to each other.
	//
	// replicaB 立刻就能看到它——没有缓存，没有延迟，因为两者对话的是同一个
	// 数据库，而不是彼此。
	byB, err := replicaB.GetJob(ctx, "tenant-a", "job_multi_1")
	if err != nil {
		t.Fatalf("GetJob via replicaB: %v", err)
	}
	if byB.BackendRunID != "prompt-1" {
		t.Errorf("replicaB read BackendRunID = %q, want prompt-1", byB.BackendRunID)
	}

	// Both replicas observed a status change and report it concurrently.
	// The higher ObservedSeq must win regardless of which HTTP request
	// happens to be processed last.
	//
	// 两个副本都观测到了一次状态变化，并发地报告它。无论哪个 HTTP 请求碰巧
	// 最后被处理，更高的 ObservedSeq 都必须获胜。
	doneA := make(chan error, 1)
	doneB := make(chan error, 1)
	go func() {
		_, _, err := replicaA.UpdateJobState(ctx, "tenant-a", "job_multi_1", controlplaneclient.JobStateUpdate{State: "running", ObservedSeq: 1})
		doneA <- err
	}()
	go func() {
		_, _, err := replicaB.UpdateJobState(ctx, "tenant-a", "job_multi_1", controlplaneclient.JobStateUpdate{State: "succeeded", ObservedSeq: 2})
		doneB <- err
	}()
	if err := <-doneA; err != nil {
		t.Fatalf("UpdateJobState via replicaA: %v", err)
	}
	if err := <-doneB; err != nil {
		t.Fatalf("UpdateJobState via replicaB: %v", err)
	}

	final, err := replicaA.GetJob(ctx, "tenant-a", "job_multi_1")
	if err != nil {
		t.Fatalf("final GetJob: %v", err)
	}
	if final.State != "succeeded" || final.ObservedSeq != 2 {
		t.Errorf("final job = {state: %q, observed_seq: %d}, want {succeeded, 2} (the higher sequence, sent by replicaB)", final.State, final.ObservedSeq)
	}
}

// TestLiveMySQLRetryAfterAnUnknownOutcomeIsIdempotent is J08's coverage for
// "提交结果未知": a Gateway replica that could not tell whether its first
// CreateJob call reached the control plane retries it with the identical
// request. Real MySQL's primary-key uniqueness, not any retry-suppression
// logic on either side, is what makes the second attempt land as a read of
// the first rather than a duplicate row or a lost update.
//
// TestLiveMySQLRetryAfterAnUnknownOutcomeIsIdempotent 是 J08 对「提交结果
// 未知」的覆盖：一个无法判断自己第一次 CreateJob 调用是否抵达控制面的
// Gateway 副本，用完全相同的请求重试它。真实 MySQL 的主键唯一性——而不是
// 任何一侧的重试抑制逻辑——才是让第二次尝试落地为对第一次的一次读取、而不是
// 一行重复记录或一次丢失更新的原因。
func TestLiveMySQLRetryAfterAnUnknownOutcomeIsIdempotent(t *testing.T) {
	h := newLiveMySQLHarness(t)
	ctx := context.Background()
	client, err := controlplaneclient.NewJobsClient(controlplaneclient.JobsClientConfig{Endpoint: h.base, Token: internalToken})
	if err != nil {
		t.Fatalf("NewJobsClient: %v", err)
	}

	req := controlplaneclient.CreateJobRequest{
		JobID: "job_retry_1", TenantID: "tenant-a", WorkflowID: "wf",
		NodeID: "node-1", RuntimeID: "comfy-1", BackendRunID: "prompt-1", State: "pending",
	}
	first, err := client.CreateJob(ctx, req)
	if err != nil {
		t.Fatalf("first CreateJob: %v", err)
	}

	// Simulate the ambiguous-outcome retry the persistence contract
	// describes: the exact same request, sent again because the first
	// response was never seen.
	//
	// 模拟持久化契约描述的那种结果不明的重试：完全相同的请求，因为从未见到
	// 第一次的响应而再发一次。
	second, err := client.CreateJob(ctx, req)
	if err != nil {
		t.Fatalf("retried CreateJob: %v", err)
	}
	if second.JobID != first.JobID || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("retried CreateJob = %+v, want the same row as the first: %+v", second, first)
	}

	// ListActiveJobsForRoute is a plain count of what real MySQL actually
	// holds for this route — a duplicate row from a non-idempotent retry
	// would show up here as job_retry_1 appearing twice.
	//
	// ListActiveJobsForRoute 是对真实 MySQL 就这条路由实际持有内容的一次直白
	// 计数——一次非幂等重试造成的重复行，会在这里表现为 job_retry_1 出现两次。
	active, err := client.ListActiveJobsForRoute(ctx, "node-1", "comfy-1")
	if err != nil {
		t.Fatalf("ListActiveJobsForRoute: %v", err)
	}
	count := 0
	for _, j := range active {
		if j.JobID == "job_retry_1" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("job_retry_1 appears %d times for its route, want exactly 1 — a retried create must not produce a second row", count)
	}
}
