package health

import (
	"context"
	"fmt"
	"runtime"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// MetricsHandler 暴露 Prometheus 格式的运行时指标端点，供 Prometheus 抓取。
// 零外部依赖：直接输出 Prometheus exposition format，无需 prometheus client 库。
func MetricsHandler() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		c.Status(consts.StatusOK)

		var m runtime.MemStats
		runtime.ReadMemStats(&m)

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
	}
}
