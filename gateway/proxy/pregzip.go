package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
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

// R2-P1-03: 统一写锁，保护 preGzipOrder、preGzipCount 以及所有写操作，
// 消除 sync.Map + atomic + Mutex 混用导致的 count/order 与 cache 不一致窗口。
var preGzipMu sync.Mutex
var preGzipOrder []string
var preGzipCount int64

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
// R2-P1-03: 在 preGzipMu 锁内执行，确保 cache/order/count 一致性。
func cleanPreGzipCache() {
	preGzipMu.Lock()
	defer preGzipMu.Unlock()
	cleanPreGzipCacheLocked()
}

// cleanPreGzipCacheLocked 删除过期条目，调用方必须持有 preGzipMu。
func cleanPreGzipCacheLocked() {
	now := time.Now()
	var expiredKeys []string
	preGzipCache.Range(func(key, value interface{}) bool {
		entry, ok := value.(*preGzipEntry)
		if !ok {
			keyStr, _ := key.(string)
			if keyStr != "" {
				expiredKeys = append(expiredKeys, keyStr)
			}
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
		preGzipCache.Delete(k)
	}
	if len(expiredKeys) > 0 {
		expiredSet := make(map[string]struct{}, len(expiredKeys))
		for _, k := range expiredKeys {
			expiredSet[k] = struct{}{}
		}
		filtered := make([]string, 0, len(preGzipOrder)-len(expiredSet))
		for _, k := range preGzipOrder {
			if _, ok := expiredSet[k]; !ok {
				filtered = append(filtered, k)
			}
		}
		preGzipOrder = filtered
		preGzipCount -= int64(len(expiredKeys))
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

// getPreGzip 读取预压缩缓存。
// R2-P1-03: 过期条目仅返回 miss，不执行惰性删除（由定期 cleaner 统一处理），
// 保持读路径完全无锁，避免写操作争用。
func getPreGzip(key string) ([]byte, bool) {
	val, ok := preGzipCache.Load(key)
	if !ok {
		return nil, false
	}
	entry, ok := val.(*preGzipEntry)
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiry) {
		return nil, false
	}
	return entry.data, true
}

// setPreGzip 写入预压缩缓存。
// R2-P1-03: 整个写操作（LoadOrStore + order 追加 + count 递增 + 淘汰）
// 在 preGzipMu 锁内完成，消除 count/order 与 cache 的不一致窗口。
// 读路径（getPreGzip）保持无锁，不受影响。
func setPreGzip(key string, data []byte) {
	entry := &preGzipEntry{
		data:   data,
		expiry: time.Now().Add(preGzipCfg.ttl),
	}
	preGzipMu.Lock()
	defer preGzipMu.Unlock()

	if _, loaded := preGzipCache.LoadOrStore(key, entry); loaded {
		return // 已存在，不重复写入也不递增计数器
	}
	preGzipOrder = append(preGzipOrder, key)
	preGzipCount++

	if preGzipCount > preGzipCfg.maxEntries {
		// 先清理过期条目
		cleanPreGzipCacheLocked()
		// 清理后仍超过上限，FIFO 淘汰最旧的条目
		for preGzipCount > preGzipCfg.maxEntries && len(preGzipOrder) > 0 {
			oldest := preGzipOrder[0]
			copy(preGzipOrder, preGzipOrder[1:])
			preGzipOrder[len(preGzipOrder)-1] = ""
			preGzipOrder = preGzipOrder[:len(preGzipOrder)-1]
			preGzipCache.Delete(oldest)
			preGzipCount--
		}
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

// InvalidatePreGzipByService 按 service 前缀批量清除 preGzip 缓存。
// R2-P1-03: 在 preGzipMu 锁内执行，确保 cache/order/count 一致性。
func InvalidatePreGzipByService(service string) int {
	prefix := "pregzip:" + service + "/"
	preGzipMu.Lock()
	defer preGzipMu.Unlock()

	var deletedKeys []string
	preGzipCache.Range(func(key, _ interface{}) bool {
		keyStr, ok := key.(string)
		if !ok {
			return true
		}
		if strings.HasPrefix(keyStr, prefix) {
			preGzipCache.Delete(keyStr)
			deletedKeys = append(deletedKeys, keyStr)
		}
		return true
	})
	if len(deletedKeys) == 0 {
		return 0
	}
	preGzipCount -= int64(len(deletedKeys))

	deletedSet := make(map[string]struct{}, len(deletedKeys))
	for _, k := range deletedKeys {
		deletedSet[k] = struct{}{}
	}
	filtered := make([]string, 0, len(preGzipOrder))
	for _, k := range preGzipOrder {
		if _, ok := deletedSet[k]; !ok {
			filtered = append(filtered, k)
		}
	}
	preGzipOrder = filtered

	return len(deletedKeys)
}

// PreGzipCacheMiddleware 返回一个轻量中间件，在所有业务中间件之前
// 检查 pre-gzip 缓存。命中时直接响应并 Abort，跳过后续所有中间件。
// 仅对 GET 请求生效。
func (p *Proxy) PreGzipCacheMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		// 仅 GET
		if string(c.Request.Method()) != http.MethodGet {
			c.Next(ctx)
			return
		}

		// 必须带 Accept-Encoding: gzip
		ae := string(c.Request.Header.Peek("Accept-Encoding"))
		if !strings.Contains(ae, "gzip") {
			c.Next(ctx)
			return
		}

		// 从路径提取 service/method：/api/v1/:service/:method
		path := string(c.Request.URI().Path())
		parts := strings.SplitN(path, "/", 5) // ["", "api", "v1", "product", "GetCategoryTree"]
		if len(parts) < 5 {
			c.Next(ctx)
			return
		}
		service, method := parts[3], toMethodName(parts[4])
		if service == "" || method == "" {
			c.Next(ctx)
			return
		}

		// 检查是否在缓存白名单
		if !p.cacheableMethods[service+"/"+method] {
			c.Next(ctx)
			return
		}

		// 尝试读缓存
		cacheKey := preGzipCacheKey(service, method, c.Request.QueryString())
		if cacheKey == "" {
			c.Next(ctx)
			return
		}
		if gzipped, ok := getPreGzip(cacheKey); ok {
			servePreGzip(c, gzipped)
			return
		}

		c.Next(ctx)
	}
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
	if !ok {
		logger.Warnf("pregzip: resp is %T, not string (service=%s method=%s)", resp, service, method)
		return false
	}
	if s == "" {
		return false
	}

	// 解析原始 protobuf JSON 响应
	var m map[string]interface{}
	if err := sonic.UnmarshalString(s, &m); err != nil {
		logger.Warnf("pregzip: unmarshal failed (service=%s method=%s): %v", service, method, err)
		return false
	}

	// GenericCall 成功 (err==nil) 的响应就是成功响应，
	// 不需要检查 code 字段（原始 protobuf JSON 没有 code）。
	// 构建与 handleRPCResponse 一致的响应信封。
	// handleRPCResponse 逻辑：有 code → 取 m["data"]；无 code → 整个 m
	var data interface{}
	if _, hasCode := m["code"]; hasCode {
		data = m["data"]
	} else {
		data = m
	}

	respBody := response.R{
		Code:    0,
		Message: "ok",
		Data:    data,
	}

	jsonBytes, err := sonic.Marshal(respBody)
	if err != nil {
		return false
	}

	// 小响应不值得预压缩 —— 但已解析过一次，直接投递避免 handleRPCResponse 重复解析
	if len(jsonBytes) < preGzipCfg.minSize {
		logger.Infof("pregzip: skip caching, response too small (%d < %d) (service=%s method=%s)", len(jsonBytes), preGzipCfg.minSize, service, method)
		response.OK(c, data)
		c.Abort()
		return true
	}

	// BestSpeed：压缩一次缓存，速度优先，小体积数据压缩率差异可忽略
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)

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
