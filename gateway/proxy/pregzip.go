package proxy

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"shopmicro/pkg/logger"
	"shopmicro/pkg/response"
)

// preGzipEntry 缓存条目：预压缩的响应字节 + 过期时间。
type preGzipEntry struct {
	data   []byte
	expiry time.Time
}

// preGzipCache 全局预压缩缓存（sync.Map：读多写少，无锁读）。
var preGzipCache sync.Map

// 缓存条目计数，用于限制总缓存数量防止内存耗尽
var preGzipCount atomic.Int64

// preGzipStop 用于优雅停止清理 goroutine
var preGzipStop chan struct{}

// preGzipConfig 缓存参数（由 InitPreGzip 设置）
var preGzipCfg struct {
	ttl         time.Duration
	minSize     int
	maxEntries  int64
	maxQueryLen int
	maxKeyLen   int
}

// InitPreGzip 初始化预压缩缓存参数并启动清理 goroutine。
// 必须在服务启动时调用，且只能调用一次。
func InitPreGzip(ttl, cleanupInterval time.Duration, minSizeKB, maxEntries, maxQueryLen, maxKeyLen int) {
	preGzipCfg.ttl = ttl
	preGzipCfg.minSize = minSizeKB * 1024
	preGzipCfg.maxEntries = int64(maxEntries)
	preGzipCfg.maxQueryLen = maxQueryLen
	preGzipCfg.maxKeyLen = maxKeyLen

	preGzipStop = make(chan struct{})
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cleanPreGzipCache()
			case <-preGzipStop:
				return
			}
		}
	}()
}

// StopPreGzipCleaner 停止预压缩缓存的定期清理 goroutine。
// 应在服务优雅关闭时调用。
func StopPreGzipCleaner() {
	if preGzipStop != nil {
		close(preGzipStop)
	}
}

// cleanPreGzipCache 遍历缓存并删除过期条目。
// 由于 sync.Map 没有 Range + Delete 的原子操作，
// 使用 Range 收集过期 key 后再逐个删除。
func cleanPreGzipCache() {
	now := time.Now()
	var expiredKeys []string
	preGzipCache.Range(func(key, value interface{}) bool {
		// P2-02: 类型断言加 comma-ok，防止意外类型导致 panic
		entry, ok := value.(*preGzipEntry)
		if !ok {
			preGzipCache.Delete(key) // 清除非法条目
			return true
		}
		if now.After(entry.expiry) {
			keyStr, ok := key.(string)
			if ok {
				expiredKeys = append(expiredKeys, keyStr)
			}
		}
		return true
	})
	for _, k := range expiredKeys {
		if _, deleted := preGzipCache.LoadAndDelete(k); deleted {
			preGzipCount.Add(-1)
		}
	}
}

// preGzipCacheKey 构建缓存键：service/method?queryString（参数排序确保一致性）。
func preGzipCacheKey(service, method string, queryString []byte) string {
	key := "pregzip:" + service + "/" + method
	if len(queryString) > 0 {
		// queryString 已在 Handler() 中通过 url.ParseQuery 校验过（非法则返回 400），
		// 此处忽略解析错误是安全的。
		params, _ := url.ParseQuery(string(queryString))
		keys := make([]string, 0, len(params))
		for k := range params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var buf strings.Builder
		buf.WriteByte('?')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte('&')
			}
			buf.WriteString(k)
			buf.WriteByte('=')
			vals := params[k]
			sort.Strings(vals)
			buf.WriteString(strings.Join(vals, ","))
		}
		key += buf.String()
	}
	// R5-P0-02: 超长缓存键不缓存，防止内存消耗
	if len(key) > preGzipCfg.maxKeyLen {
		return ""
	}
	return key
}

// getPreGzip 读取预压缩缓存。过期条目被惰性删除。
func getPreGzip(key string) ([]byte, bool) {
	val, ok := preGzipCache.Load(key)
	if !ok {
		return nil, false
	}
	// R4-P2-01: 类型断言加 comma-ok，防止意外类型导致 panic
	entry, ok := val.(*preGzipEntry)
	if !ok {
		preGzipCache.Delete(key)
		return nil, false
	}
	if time.Now().After(entry.expiry) {
		if _, deleted := preGzipCache.LoadAndDelete(key); deleted {
			preGzipCount.Add(-1)
		}
		return nil, false
	}
	return entry.data, true
}

// setPreGzip 写入预压缩缓存。
// 超过 preGzipMaxEntries 时不写入新条目，防止内存耗尽。
// R5-P1-01: 使用 LoadOrStore 原子检查，仅在新 key 时递增计数器，
// 防止并发写入相同 key 时计数器永久膨胀导致缓存失效。
func setPreGzip(key string, data []byte) {
	entry := &preGzipEntry{
		data:   data,
		expiry: time.Now().Add(preGzipCfg.ttl),
	}
	if _, loaded := preGzipCache.LoadOrStore(key, entry); loaded {
		return // 已存在，不重复写入也不递增计数器
	}
	newCount := preGzipCount.Add(1)
	// 并发 LoadOrStore 可能短暂超过上限，触发一次清理
	if newCount > preGzipCfg.maxEntries+16 {
		cleanPreGzipCache()
	}
}

// servePreGzip 直接将预压缩字节写入 HTTP 响应。
func servePreGzip(c *app.RequestContext, gzipped []byte) {
	c.Response.Header.Set("Content-Encoding", "gzip")

	// 追加 Vary 而非覆盖，保留 CORS 设的 "Vary: Origin"
	existing := string(c.Response.Header.Peek("Vary"))
	if existing != "" {
		c.Response.Header.Set("Vary", existing+", Accept-Encoding")
	} else {
		c.Response.Header.Set("Vary", "Accept-Encoding")
	}

	c.Response.Header.SetContentType(consts.MIMEApplicationJSON)
	c.Response.SetStatusCode(consts.StatusOK)
	c.Response.SetBody(gzipped)
	c.Abort()
}

// tryPreGzipCache 尝试将成功的 GET 响应预压缩并缓存。
// 仅对 cacheableMethods 白名单中的方法生效。
func (p *Proxy) tryPreGzipCache(c *app.RequestContext, service, method string, resp interface{}) bool {

	// 仅 GET 请求且在白名单中
	if string(c.Request.Method()) != http.MethodGet || !p.cacheableMethods[service+"/"+method] {
		return false
	}

	// 客户端必须接受 gzip
	ae := string(c.Request.Header.Peek("Accept-Encoding"))
	if !strings.Contains(ae, "gzip") {
		return false
	}

	// P1-04: query string 过长不缓存，防止缓存 key 膨胀
	queryString := c.Request.QueryString()
	if len(queryString) > preGzipCfg.maxQueryLen {
		return false
	}

	cacheKey := preGzipCacheKey(service, method, queryString)
	// R5-P0-02: 超长 key 跳过缓存
	if cacheKey == "" {
		return false
	}

	// RPC 响应必须是 JSON 字符串
	s, ok := resp.(string)
	if !ok || s == "" {
		return false
	}

	// 解析为 map，检查是否为包装响应
	var m map[string]interface{}
	if err := sonic.UnmarshalString(s, &m); err != nil {
		return false
	}

	// 只缓存 code==0 的成功响应
	codeRaw, hasCode := m["code"]
	if !hasCode {
		return false
	}

	// P0-02: 严格检查 code 类型，非 float64 视为异常，不缓存
	code, ok := codeRaw.(float64)
	if !ok || int(code) != 0 {
		return false
	}

	// 构建与 handleRPCResponse 一致的响应信封（使用 sonic，与 c.JSON 一致）
	respBody := response.R{
		Code:    0,
		Message: "ok",
		Data:    m["data"],
	}

	jsonBytes, err := sonic.Marshal(respBody)
	if err != nil {
		return false
	}

	// 小响应不值得预压缩 —— 但已解析过一次，直接投递避免 handleRPCResponse 重复解析
	if len(jsonBytes) < preGzipCfg.minSize {
		response.OK(c, m["data"])
		c.Abort()
		return true
	}

	// BestCompression：只压一次，用最高压缩率换取最小缓存体积
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)

	if err != nil {
		return false
	}

	if _, err := w.Write(jsonBytes); err != nil {
		// R7-P2-07: 记录 gzip 写入失败，之前此错误被静默吞掉
		logger.Warnf("pregzip: gzip write failed: %v", err)
		return false
	}
	if err := w.Close(); err != nil {
		logger.Warnf("pregzip: gzip close failed: %v", err)
		return false
	}

	gzipped := buf.Bytes()
	// R5-P0-02: cacheKey 已在函数开头计算并校验
	setPreGzip(cacheKey, gzipped)

	// 直接投递预压缩响应
	servePreGzip(c, gzipped)
	return true
}
