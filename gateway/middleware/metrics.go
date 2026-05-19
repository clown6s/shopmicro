package middleware

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

// defaultHistogramBuckets Prometheus 标准延迟分桶边界（秒）
var defaultHistogramBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// requestCounter 记录某个 (method, path, status) 组合的请求计数
type requestCounter struct {
	method string
	path   string
	status int
	count  atomic.Uint64
}

// durationBuckets 固定分桶的 histogram，通过 mutex 保证线程安全
type durationBuckets struct {
	bounds []float64 // 上界（已排序）
	counts []uint64  // counts[i] = 观测值 <= bounds[i] 的数量
	sumNs  uint64    // 观测值总和（纳秒）
	total  uint64    // 观测值总数
	mu     sync.Mutex
}

func newDurationBuckets(bounds []float64) *durationBuckets {
	cp := make([]float64, len(bounds))
	copy(cp, bounds)
	sort.Float64s(cp)
	return &durationBuckets{
		bounds: cp,
		counts: make([]uint64, len(cp)),
	}
}

func (b *durationBuckets) observe(seconds float64) {
	ns := uint64(seconds * 1e9)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sumNs += ns
	b.total++
	// 只递增最小匹配桶（标准 Prometheus histogram 写法）
	for i, bound := range b.bounds {
		if seconds <= bound {
			b.counts[i]++
			return
		}
	}
	// 超出所有桶边界，不递增任何桶（+Inf 桶在输出时 = total）
}

// snapshot 返回当前时刻的直方图快照（读时无锁）
func (b *durationBuckets) snapshot() (sumNs, total uint64, counts []uint64) {
	b.mu.Lock()
	sumNs = b.sumNs
	total = b.total
	counts = make([]uint64, len(b.counts))
	copy(counts, b.counts)
	b.mu.Unlock()
	return
}

// MetricsCollector 收集 HTTP 请求指标，输出 Prometheus exposition format。
// 零外部依赖，复用项目现有手写 Prometheus 格式方案。
type MetricsCollector struct {
	// 请求计数：key = "method|path|status" -> *requestCounter
	counters sync.Map
	// 请求延迟直方图：key = "method|path" -> *durationBuckets
	durations sync.Map
	// 当前并发请求数
	inFlight atomic.Int64
}

var globalCollector *MetricsCollector

func init() {
	globalCollector = NewMetricsCollector()
}

// DefaultMetricsCollector 返回全局指标收集器实例。
func DefaultMetricsCollector() *MetricsCollector {
	return globalCollector
}

// NewMetricsCollector 创建新的指标收集器。
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{}
}

// InFlight 返回当前并发请求数。
func (mc *MetricsCollector) InFlight() int64 {
	return mc.inFlight.Load()
}

// PrometheusMiddleware 返回记录 QPS、延迟分布、并发数的 Hertz 中间件。
// 应在中间件链最前面注册以覆盖所有请求。
func PrometheusMiddleware() app.HandlerFunc {
	mc := globalCollector
	return func(ctx context.Context, c *app.RequestContext) {
		mc.inFlight.Add(1)
		start := time.Now()
		c.Next(ctx)
		elapsed := time.Since(start).Seconds()

		mc.inFlight.Add(-1)

		method := string(c.Method())
		path := string(c.Request.URI().Path())
		status := c.Response.StatusCode()

		// 请求计数
		countKey := method + "|" + path + "|" + strconv.Itoa(status)
		v, _ := mc.counters.LoadOrStore(countKey, &requestCounter{
			method: method,
			path:   path,
			status: status,
		})
		v.(*requestCounter).count.Add(1)

		// 延迟直方图
		durKey := method + "|" + path
		dv, _ := mc.durations.LoadOrStore(durKey, newDurationBuckets(defaultHistogramBuckets))
		dv.(*durationBuckets).observe(elapsed)
	}
}

// WritePrometheus 将收集到的指标以 Prometheus exposition format 写入 w。
func (mc *MetricsCollector) WritePrometheus(w io.Writer) {
	// --- http_requests_total ---
	fmt.Fprintf(w, "# HELP http_requests_total Total number of HTTP requests\n")
	fmt.Fprintf(w, "# TYPE http_requests_total counter\n")

	type counterEntry struct {
		method, path string
		status       int
		count        uint64
	}
	var entries []counterEntry
	mc.counters.Range(func(key, value any) bool {
		rc := value.(*requestCounter)
		entries = append(entries, counterEntry{
			method: rc.method,
			path:   rc.path,
			status: rc.status,
			count:  rc.count.Load(),
		})
		return true
	})
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].method != entries[j].method {
			return entries[i].method < entries[j].method
		}
		if entries[i].path != entries[j].path {
			return entries[i].path < entries[j].path
		}
		return entries[i].status < entries[j].status
	})
	for _, e := range entries {
		fmt.Fprintf(w, `http_requests_total{method="%s",path="%s",status="%d"} %d`+"\n",
			e.method, escapeLabelValue(e.path), e.status, e.count)
	}

	// --- http_request_duration_seconds (histogram) ---
	fmt.Fprintf(w, "# HELP http_request_duration_seconds HTTP request duration in seconds\n")
	fmt.Fprintf(w, "# TYPE http_request_duration_seconds histogram\n")

	type durEntry struct {
		method  string
		path    string
		buckets *durationBuckets
	}
	var durEntries []durEntry
	mc.durations.Range(func(key, value any) bool {
		parts := strings.SplitN(key.(string), "|", 2)
		if len(parts) != 2 {
			return true
		}
		durEntries = append(durEntries, durEntry{
			method:  parts[0],
			path:    parts[1],
			buckets: value.(*durationBuckets),
		})
		return true
	})
	sort.Slice(durEntries, func(i, j int) bool {
		if durEntries[i].method != durEntries[j].method {
			return durEntries[i].method < durEntries[j].method
		}
		return durEntries[i].path < durEntries[j].path
	})
	for _, e := range durEntries {
		b := e.buckets
		sumNs, total, counts := b.snapshot()
		sumSec := float64(sumNs) / 1e9
		escapedPath := escapeLabelValue(e.path)

		cumulative := uint64(0)
		for i, bound := range b.bounds {
			cumulative += counts[i]
			fmt.Fprintf(w, `http_request_duration_seconds_bucket{method="%s",path="%s",le="%s"} %d`+"\n",
				e.method, escapedPath, formatFloat(bound), cumulative)
		}
		// +Inf 桶 = 总数
		fmt.Fprintf(w, `http_request_duration_seconds_bucket{method="%s",path="%s",le="+Inf"} %d`+"\n",
			e.method, escapedPath, total)
		fmt.Fprintf(w, `http_request_duration_seconds_sum{method="%s",path="%s"} %s`+"\n",
			e.method, escapedPath, formatFloat(sumSec))
		fmt.Fprintf(w, `http_request_duration_seconds_count{method="%s",path="%s"} %d`+"\n",
			e.method, escapedPath, total)
	}

	// --- http_requests_in_flight ---
	fmt.Fprintf(w, "# HELP http_requests_in_flight Current number of HTTP requests being processed\n")
	fmt.Fprintf(w, "# TYPE http_requests_in_flight gauge\n")
	fmt.Fprintf(w, "http_requests_in_flight %d\n", mc.inFlight.Load())
}

// escapeLabelValue 转义 Prometheus label 值中的特殊字符。
func escapeLabelValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// formatFloat 格式化浮点数，去除末尾多余零。
func formatFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0" // Prometheus le 标签约定使用浮点格式
	}
	return s
}
