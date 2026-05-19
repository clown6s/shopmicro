package health

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"shopmicro/gateway/middleware"
)

// R5-P3-06: 缓存 runtime.MemStats，避免每次 /metrics 请求触发 STW 暂停。
// 每 10 秒在后台 goroutine 中更新一次，Prometheus 15s 抓取间隔下数据足够新鲜。
var cachedMemStats atomic.Pointer[runtime.MemStats]

// metricsStop 用于优雅停止 metrics 更新 goroutine
var metricsStop chan struct{}

func init() {
	// 立即填充一次，避免首次请求返回零值
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	cachedMemStats.Store(&m)

	// R4-P1-02: 通过 metricsStop channel 支持优雅停止
	metricsStop = make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				cachedMemStats.Store(&m)
			case <-metricsStop:
				return
			}
		}
	}()
}

// StopMetricsUpdater 停止 metrics 缓存更新的后台 goroutine。
// 应在服务优雅关闭时调用。
func StopMetricsUpdater() {
	if metricsStop != nil {
		close(metricsStop)
	}
}

// MetricsHandler 暴露 Prometheus 格式的运行时指标端点，供 Prometheus 抓取。
// 零外部依赖：直接输出 Prometheus exposition format，无需 prometheus client 库。
func MetricsHandler() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		c.Status(consts.StatusOK)

		// R5-P3-06: 使用缓存的 memstats，避免 STW
		m := cachedMemStats.Load()
		if m == nil {
			// 极端情况：init 尚未完成（理论上不会发生）
			var fresh runtime.MemStats
			runtime.ReadMemStats(&fresh)
			m = &fresh
		}

		fmt.Fprintf(c, "# HELP go_goroutines Number of goroutines that currently exist\n")
		fmt.Fprintf(c, "# TYPE go_goroutines gauge\n")
		fmt.Fprintf(c, "go_goroutines %d\n", runtime.NumGoroutine())

		fmt.Fprintf(c, "# HELP go_mem_alloc_bytes Number of bytes allocated and still in use\n")
		fmt.Fprintf(c, "# TYPE go_mem_alloc_bytes gauge\n")
		fmt.Fprintf(c, "go_mem_alloc_bytes %d\n", m.Alloc)

		fmt.Fprintf(c, "# HELP go_mem_sys_bytes Total bytes of memory obtained from the OS\n")
		fmt.Fprintf(c, "# TYPE go_mem_sys_bytes gauge\n")
		fmt.Fprintf(c, "go_mem_sys_bytes %d\n", m.Sys)

		fmt.Fprintf(c, "# HELP go_gc_pause_total_ns Total cumulative GC pause in nanoseconds\n")
		fmt.Fprintf(c, "# TYPE go_gc_pause_total_ns counter\n")
		fmt.Fprintf(c, "go_gc_pause_total_ns %d\n", m.PauseTotalNs)

		fmt.Fprintf(c, "# HELP go_gc_num Total number of GC cycles completed\n")
		fmt.Fprintf(c, "# TYPE go_gc_num counter\n")
		fmt.Fprintf(c, "go_gc_num %d\n", m.NumGC)

		// P2: HTTP 请求指标（QPS、延迟分布、错误率、并发数）
		middleware.DefaultMetricsCollector().WritePrometheus(c)
	}
}
