package middleware

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/common/test/assert"
)

func TestMetricsCollector_CounterAndHistogram(t *testing.T) {
	mc := NewMetricsCollector()

	// 直接灌入数据模拟请求
	mc.inFlight.Add(2)

	// 灌入请求计数
	rc1 := &requestCounter{method: "GET", path: "/api/v1/user/Login", status: 200}
	rc1.count.Store(5)
	mc.counters.Store("GET|/api/v1/user/Login|200", rc1)

	rc2 := &requestCounter{method: "POST", path: "/api/v1/user/RegisterUser", status: 201}
	rc2.count.Store(3)
	mc.counters.Store("POST|/api/v1/user/RegisterUser|201", rc2)

	rc3 := &requestCounter{method: "GET", path: "/api/v1/user/Login", status: 401}
	rc3.count.Store(1)
	mc.counters.Store("GET|/api/v1/user/Login|401", rc3)

	// 灌入延迟直方图
	dur := newDurationBuckets(defaultHistogramBuckets)
	dur.observe(0.012)
	dur.observe(0.045)
	dur.observe(0.003)
	mc.durations.Store("GET|/api/v1/user/Login", dur)

	// 输出
	var buf bytes.Buffer
	mc.WritePrometheus(&buf)
	output := buf.String()

	// Counter 格式
	assert.True(t, strings.Contains(output, "http_requests_total{"))
	assert.True(t, strings.Contains(output, `method="GET",path="/api/v1/user/Login",status="200"} 5`))
	assert.True(t, strings.Contains(output, `method="POST",path="/api/v1/user/RegisterUser",status="201"} 3`))
	assert.True(t, strings.Contains(output, `method="GET",path="/api/v1/user/Login",status="401"} 1`))

	// 排序：GET 在 POST 前面
	assert.True(t, strings.Index(output, "GET") < strings.Index(output, "POST"))

	// Histogram 格式
	assert.True(t, strings.Contains(output, "# TYPE http_request_duration_seconds histogram"))
	assert.True(t, strings.Contains(output, "http_request_duration_seconds_bucket{method=\"GET\",path=\"/api/v1/user/Login\",le=\"0.005\"}"))
	assert.True(t, strings.Contains(output, "http_request_duration_seconds_bucket{method=\"GET\",path=\"/api/v1/user/Login\",le=\"+Inf\"} 3"))
	assert.True(t, strings.Contains(output, "http_request_duration_seconds_sum{method=\"GET\",path=\"/api/v1/user/Login\"}"))
	assert.True(t, strings.Contains(output, "http_request_duration_seconds_count{method=\"GET\",path=\"/api/v1/user/Login\"} 3"))

	// Gauge
	assert.True(t, strings.Contains(output, "http_requests_in_flight 2"))
	assert.True(t, strings.Contains(output, "# TYPE http_requests_in_flight gauge"))
}

func TestMetricsCollector_EmptyOutput(t *testing.T) {
	mc := NewMetricsCollector()
	var buf bytes.Buffer
	mc.WritePrometheus(&buf)
	output := buf.String()

	// 应有 HELP/TYPE 头，但无数据行
	assert.True(t, strings.Contains(output, "# HELP http_requests_total"))
	assert.True(t, strings.Contains(output, "# TYPE http_requests_total counter"))
	assert.True(t, strings.Contains(output, "# HELP http_request_duration_seconds"))
	assert.True(t, strings.Contains(output, "# TYPE http_request_duration_seconds histogram"))
	assert.True(t, strings.Contains(output, "# HELP http_requests_in_flight"))
	assert.True(t, strings.Contains(output, "# TYPE http_requests_in_flight gauge"))
	assert.True(t, strings.Contains(output, "http_requests_in_flight 0"))
	// 无计数器/直方图数据行
	assert.False(t, strings.Contains(output, `http_requests_total{`))
	assert.False(t, strings.Contains(output, `http_request_duration_seconds_bucket`))
}

func TestDurationBuckets(t *testing.T) {
	b := newDurationBuckets([]float64{0.01, 0.05, 0.1})

	b.observe(0.005) // counts[0]++ → smallest bucket where 0.005 <= 0.01
	b.observe(0.03)  // counts[1]++ → 0.03 > 0.01, 0.03 <= 0.05
	b.observe(0.08)  // counts[2]++ → 0.08 > 0.05, 0.08 <= 0.1
	b.observe(0.5)   // beyond all buckets → no increment

	_, total, counts := b.snapshot()
	assert.DeepEqual(t, uint64(4), total)
	// counts 是非累计的（每个桶只计属于自己的观测值）
	assert.DeepEqual(t, uint64(1), counts[0]) // 0.005 <= 0.01
	assert.DeepEqual(t, uint64(1), counts[1]) // 0.03 <= 0.05
	assert.DeepEqual(t, uint64(1), counts[2]) // 0.08 <= 0.1
}

func TestDurationBucketsCumulative(t *testing.T) {
	b := newDurationBuckets([]float64{0.01, 0.05, 0.1})

	b.observe(0.005) // counts[0]++
	b.observe(0.03)  // counts[1]++
	b.observe(0.08)  // counts[2]++
	b.observe(0.5)   // no increment

	// 非累计的 counts
	_, _, counts := b.snapshot()
	assert.DeepEqual(t, uint64(1), counts[0])
	assert.DeepEqual(t, uint64(1), counts[1])
	assert.DeepEqual(t, uint64(1), counts[2])

	// 输出时累计：WritePrometheus 做 cumulative += counts[i]
	// le=0.01 → 1, le=0.05 → 2, le=0.1 → 3, +Inf → 4
	var buf bytes.Buffer
	mc := NewMetricsCollector()
	mc.durations.Store("GET|/test", b)
	mc.WritePrometheus(&buf)
	out := buf.String()
	assert.True(t, strings.Contains(out, `le="0.01"} 1`))
	assert.True(t, strings.Contains(out, `le="0.05"} 2`))
	assert.True(t, strings.Contains(out, `le="0.1"} 3`))
	assert.True(t, strings.Contains(out, `le="+Inf"} 4`))
}

func TestEscapeLabelValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`hello"world`, `hello\"world`},
		{`back\slash`, `back\\slash`},
		{"line\nbreak", `line\nbreak`},
		{"normal/path", "normal/path"},
	}
	for _, tc := range tests {
		assert.DeepEqual(t, tc.want, escapeLabelValue(tc.input))
	}
}

func TestFormatFloat(t *testing.T) {
	assert.DeepEqual(t, "0.005", formatFloat(0.005))
	assert.DeepEqual(t, "1.0", formatFloat(1.0))
	assert.DeepEqual(t, "10.0", formatFloat(10.0))
	assert.DeepEqual(t, "2.5", formatFloat(2.5))
}

func TestNewDurationBuckets_SortsBounds(t *testing.T) {
	b := newDurationBuckets([]float64{0.5, 0.01, 0.1})
	assert.DeepEqual(t, 0.01, b.bounds[0])
	assert.DeepEqual(t, 0.1, b.bounds[1])
	assert.DeepEqual(t, 0.5, b.bounds[2])
}

func TestRequestCounter_Atomic(t *testing.T) {
	rc := &requestCounter{method: "GET", path: "/test", status: 200}
	assert.DeepEqual(t, uint64(0), rc.count.Load())
	rc.count.Add(1)
	assert.DeepEqual(t, uint64(1), rc.count.Load())
	rc.count.Add(4)
	assert.DeepEqual(t, uint64(5), rc.count.Load())
}

// 编译时检查：确保 MetricsCollector 实现了预期接口
var _ interface {
	InFlight() int64
	WritePrometheus(io.Writer)
} = (*MetricsCollector)(nil)
