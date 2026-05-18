package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

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

const (
	// preGzipTTL 缓存有效期
	preGzipTTL = 3600 * time.Second

	// preGzipMinSize 最小缓存阈值：小于此大小的响应不值得预压缩
	preGzipMinSize = 100 * 1024

	// preGzipMaxEntries 最大缓存条目数，防止攻击者构造大量唯一 URL 撑爆内存
	preGzipMaxEntries = 1000

	// P1-04: query string 最大长度，超过此值的请求不缓存
	preGzipMaxQueryLen = 2048
)

// preGzipCacheKey 构建缓存键：service/method?queryString（参数排序确保一致性）。
func preGzipCacheKey(service, method string, queryString []byte) string {
	key := "pregzip:" + service + "/" + method
	if len(queryString) > 0 {
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
			buf.WriteString(params[k][0])
		}
		key += buf.String()
	}
	return key
}

// getPreGzip 读取预压缩缓存。过期条目被惰性删除。
func getPreGzip(key string) ([]byte, bool) {
	val, ok := preGzipCache.Load(key)
	if !ok {
		return nil, false
	}
	entry := val.(*preGzipEntry)
	if time.Now().After(entry.expiry) {
		preGzipCache.Delete(key)
		preGzipCount.Add(-1)
		return nil, false
	}
	return entry.data, true
}

// setPreGzip 写入预压缩缓存。
// 超过 preGzipMaxEntries 时不写入新条目，防止内存耗尽。
func setPreGzip(key string, data []byte) {
	for {
		current := preGzipCount.Load()
		if current >= preGzipMaxEntries {
			return
		}
		if preGzipCount.CompareAndSwap(current, current+1) {
			break
		}
	}
	preGzipCache.Store(key, &preGzipEntry{
		data:   data,
		expiry: time.Now().Add(preGzipTTL),
	})
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
func (p *Proxy) tryPreGzipCache(c *app.RequestContext, service, method string, resp interface{}) bool {
	// 仅 GET 请求
	if string(c.Request.Method()) != http.MethodGet {
		return false
	}

	// 客户端必须接受 gzip
	ae := string(c.Request.Header.Peek("Accept-Encoding"))
	if !strings.Contains(ae, "gzip") {
		return false
	}

	// P1-04: query string 过长不缓存，防止缓存 key 膨胀
	queryString := c.Request.QueryString()
	if len(queryString) > preGzipMaxQueryLen {
		return false
	}

	// RPC 响应必须是 JSON 字符串
	s, ok := resp.(string)
	if !ok || s == "" {
		return false
	}

	// 解析为 map，检查是否为包装响应
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
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

	// 构建与 handleRPCResponse 一致的响应信封
	respBody := response.R{
		Code:    0,
		Message: "ok",
		Data:    m["data"],
	}

	jsonBytes, err := json.Marshal(respBody)
	if err != nil {
		return false
	}

	// 小响应不值得预压缩
	if len(jsonBytes) < preGzipMinSize {
		return false
	}

	// BestCompression：只压一次，用最高压缩率换取最小缓存体积
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return false
	}
	if _, err := w.Write(jsonBytes); err != nil {
		return false
	}
	w.Close()

	gzipped := buf.Bytes()
	cacheKey := preGzipCacheKey(service, method, queryString)
	setPreGzip(cacheKey, gzipped)

	// 直接投递预压缩响应
	servePreGzip(c, gzipped)
	return true
}
