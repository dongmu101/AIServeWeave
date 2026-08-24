package ratelimit_test

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"

	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/ratelimit"
)

// redisFactory joins the Redis implementation to the contract suite when
// AISW_REDIS_ADDR names a reachable server. It is opt-in for the same reason
// the live Ollama test is: `go test ./...` must stay hermetic on a machine
// that has no Redis, while the Lua half of the token bucket still gets checked
// against the same assertions the Go half passes.
//
// Each run works in its own Redis database key space by flushing first, so a
// developer's local Redis is not left holding this suite's leftovers.
//
// redisFactory 在 AISW_REDIS_ADDR 指向一个可达服务器时，把 Redis 实现接入契约套件。
// 它是选择性开启的，理由与那个真实 Ollama 测试相同：`go test ./...` 在没有 Redis 的
// 机器上必须保持自足，而令牌桶的 Lua 那一半，仍然要接受 Go 那一半所通过的同一组断言。
//
// 每次运行先 flush，在自己的键空间里工作，因此开发者本地的 Redis 不会留下本套件的
// 残余。
func redisFactory(t *testing.T) (factory, bool) {
	addr := os.Getenv("AISW_REDIS_ADDR")
	if addr == "" {
		return factory{}, false
	}
	return factory{
		name: "redis",
		build: func(t *testing.T, clock *gatewaytest.Clock) (ratelimit.Limiter, func() int) {
			t.Helper()
			client := redis.NewClient(&redis.Options{Addr: addr})
			ctx := context.Background()
			if err := client.Ping(ctx).Err(); err != nil {
				t.Fatalf("AISW_REDIS_ADDR is set to %q but the server is not reachable: %v", addr, err)
			}
			if err := client.FlushDB(ctx).Err(); err != nil {
				t.Fatalf("flushing the test database: %v", err)
			}
			t.Cleanup(func() { _ = client.Close() })

			l, err := ratelimit.NewRedis(ratelimit.RedisConfig{Client: client, Clock: clock})
			if err != nil {
				t.Fatalf("ratelimit.NewRedis: %v", err)
			}
			// Redis keeps no per-tenant Go state, so it has nothing to report
			// for the sweep case; the suite skips that assertion for it. Its
			// equivalent is the PEXPIRE the scripts set, which Redis enforces
			// on its own schedule and a test cannot observe through a fake
			// clock.
			//
			// Redis 侧不保存任何按租户的 Go 状态，因此清理用例对它无从报告；套件会为
			// 它跳过那条断言。它的对应物是脚本设置的 PEXPIRE，那由 Redis 按自己的
			// 节奏执行，测试无法透过假时钟观察到。
			return l, nil
		},
	}, true
}
