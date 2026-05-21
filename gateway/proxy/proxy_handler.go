package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/sony/gobreaker"

	"shopmicro/gateway/middleware"
	"shopmicro/pkg/errcode"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/response"
)

// =========================
// HTTP 处理器
// =========================

// Handler 返回一个 Hertz HandlerFunc，将
// /api/v1/:service/:method 请求分发到对应的 gRPC 服务。
func (p *Proxy) Handler() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		service := c.Param("service")
		method := toMethodName(c.Param("method"))

		// R2-P1-02: 拒绝无效的方法名
		if method == "" {
			response.Fail(c, http.StatusBadRequest, int(errcode.CodeGwParam), "invalid method name")
			c.Abort()
			return
		}
		// P2-01: 拒绝空或可疑的服务名
		if service == "" || strings.ContainsAny(service, "./\\") || !serviceRegex.MatchString(service) {
			response.Fail(c, http.StatusBadRequest, int(errcode.CodeGwParam), "invalid service name")
			c.Abort()
			return
		}

		// P1-12: 服务/方法白名单检查
		if len(p.allowedMethods) > 0 {
			allowed, ok := p.allowedMethods[service]
			if !ok {
				logger.Warnf("rejected request: service not in whitelist (service=%s)", service)
				response.Fail(c, http.StatusForbidden, int(errcode.CodeGwParam), "access denied")
				c.Abort()
				return
			}
			if !allowed[method] {
				logger.Warnf("rejected request: method not allowed (service=%s, method=%s)", service, method)
				response.Fail(c, http.StatusForbidden, int(errcode.CodeGwParam), "access denied")
				c.Abort()
				return
			}
		}

		// P3-03: 验证 POST/PUT 请求的 Content-Type
		reqMethod := strings.ToUpper(string(c.Request.Method()))
		if reqMethod == http.MethodPost || reqMethod == http.MethodPut {
			ct := string(c.ContentType())
			if ct != "" && !strings.HasPrefix(ct, "application/json") {
				response.Fail(c, http.StatusUnsupportedMediaType, int(errcode.CodeGwParam), "content type must be application/json")
				c.Abort()
				return
			}
		}

		// HTTP 方法校验
		if methods, ok := p.httpMethodMap[service+"/"+method]; ok {
			if !methods[reqMethod] {
				logger.Warnf("rejected request: http method not allowed (service=%s, method=%s, http=%s)", service, method, reqMethod)
				response.Fail(c, http.StatusMethodNotAllowed, int(errcode.CodeGwParam), "method not allowed")
				c.Abort()
				return
			}
		}

		// R5-P2-06+P2-07: 一次性解析 query string
		var queryParams url.Values
		if queryString := c.Request.QueryString(); len(queryString) > 0 {
			var parseErr error
			queryParams, parseErr = url.ParseQuery(string(queryString))
			if parseErr != nil {
				response.Fail(c, http.StatusBadRequest, int(errcode.CodeGwParam), "invalid query parameters")
				c.Abort()
				return
			}
			if len(queryParams) > maxQueryParams {
				response.Fail(c, http.StatusBadRequest, int(errcode.CodeGwParam), "too many query parameters")
				c.Abort()
				return
			}
		}

		// 按端点限流
		if p.epRateLimitFn != nil {
			epKey := service + "/" + method
			ip := middleware.ClientRealIP(c)
			if !p.epRateLimitFn(ctx, epKey, ip) {
				response.Fail(c, http.StatusTooManyRequests, int(errcode.CodeGwRateLimit), "too many requests")
				c.Abort()
				return
			}
		}

		// JWT 认证
		authCtx, ok := p.authenticate(ctx, c, service, method)
		if !ok {
			return
		}
		ctx = authCtx

		// Pre-gzip 缓存命中已由前置中间件处理，此处无需重复检查

		// 请求体处理
		rawBody := c.Request.Body()
		if len(rawBody) > maxBodySize {
			response.Fail(c, http.StatusRequestEntityTooLarge, int(errcode.CodeGwParam), "request body too large")
			c.Abort()
			return
		}
		body := string(rawBody)
		if body == "" || body == "null" {
			body = "{}"
		}
		// 将 query string 参数合并到 JSON body 中
		if len(queryParams) > 0 {
			if sc, ok := p.getClient(service); ok && sc.svcDesc != nil {
				if merged, err := p.mergeQueryToJSONTyped(body, queryParams, sc.svcDesc, method); err == nil {
					body = merged
				} else {
					logger.Warnf("merge query to json failed: %v", err)
				}
			} else if merged, err := mergeQueryToJSON(body, queryParams); err == nil {
				body = merged
			} else {
				logger.Warnf("merge query to json (fallback) failed: %v", err)
			}
		}

		// 方法级超时覆盖
		var callOpts []callopt.Option
		if timeout, ok := p.methodTimeout[service+"/"+method]; ok {
			perAttempt := timeout / 3
			if perAttempt < 5*time.Second {
				perAttempt = 5 * time.Second
			}
			callOpts = append(callOpts, callopt.WithRPCTimeout(perAttempt))
		}

		resp, err := p.Call(ctx, service, method, body, callOpts...)

		// Pre-gzip 缓存回填
		if err == nil {
			if p.tryPreGzipCache(c, service, method, resp) {
				return
			}
		}

		p.handleRPCResponse(c, service, method, resp, err)
	}
}

// authenticate 验证 JWT 令牌并检查 Redis 中的令牌状态。
func (p *Proxy) authenticate(ctx context.Context, c *app.RequestContext, service, method string) (context.Context, bool) {
	if p.publicMethod[service+"/"+method] {
		return ctx, true
	}
	if p.jwtParser == nil {
		logger.Errorf("authentication requested but jwt parser not configured (service=%s method=%s)", service, method)
		response.Fail(c, http.StatusServiceUnavailable, int(errcode.CodeGwServer), "authentication not configured")
		c.Abort()
		return nil, false
	}

	tokenStr, ok := bearerToken(string(c.GetHeader("Authorization")))
	if !ok {
		response.Unauthorized(c, "missing token")
		c.Abort()
		return nil, false
	}
	claims, err := p.jwtParser.Parse(tokenStr)
	if err != nil {
		response.Unauthorized(c, "invalid token")
		c.Abort()
		return nil, false
	}

	// P1-1: 验证令牌在 Redis SET 中仍然有效（带本地缓存）
	if p.rdb != nil {
		if valid, hit := p.checkTokenCache(tokenStr); hit {
			if !valid {
				response.Unauthorized(c, "token revoked")
				c.Abort()
				return nil, false
			}
			// 缓存命中且有效，跳过 Redis 查询
		} else {
			tokenKey := fmt.Sprintf("%s%d", tokenPrefix, claims.UserID)
			isMember, err := p.rdb.SIsMember(ctx, tokenKey, tokenStr)
			if err != nil {
				logger.Warnf("redis sismember failed (fail-closed): user_id=%d err=%v", claims.UserID, err)
				response.Fail(c, http.StatusServiceUnavailable, int(errcode.CodeGwSrvUnavailable), "service temporarily unavailable")
				c.Abort()
				return nil, false
			} else if !isMember {
				p.setTokenCache(tokenStr, false)
				response.Unauthorized(c, "token revoked")
				c.Abort()
				return nil, false
			}
			p.setTokenCache(tokenStr, true)
		}
	}

	// 通过 Kitex metainfo 转发身份信息
	ctx = metainfo.WithPersistentValue(ctx, "token", tokenStr)
	ctx = metainfo.WithPersistentValue(ctx, "user_id", fmt.Sprintf("%d", claims.UserID))

	c.Set("user_id", fmt.Sprintf("%d", claims.UserID))

	return ctx, true
}

// handleRPCResponse 处理 RPC 调用结果并写入 HTTP 响应。
func (p *Proxy) handleRPCResponse(c *app.RequestContext, service, method string, resp interface{}, err error) {
	if err != nil {
		// 熔断器打开
		if errors.Is(err, gobreaker.ErrOpenState) {
			logger.Warnf("circuit breaker open: service=%s method=%s", service, method)
			response.Fail(c, http.StatusServiceUnavailable, int(errcode.CodeGwSrvUnavailable), "下游服务暂时不可用（熔断）")
			c.Abort()
			return
		}
		// 熔断器半开状态请求过多
		if errors.Is(err, gobreaker.ErrTooManyRequests) {
			logger.Warnf("circuit breaker half-open too many requests: service=%s method=%s", service, method)
			response.Fail(c, http.StatusServiceUnavailable, int(errcode.CodeGwSrvUnavailable), "下游服务正在恢复中")
			c.Abort()
			return
		}

		// 解析下游 BizError
		if biz, ok := errcode.ParseBizError(err); ok {
			response.BizErr(c, biz)
			c.Abort()
			return
		}
		unwrapped := errors.Unwrap(err)
		if unwrapped != nil {
			if biz, ok := errcode.ParseBizError(unwrapped); ok {
				response.BizErr(c, biz)
				c.Abort()
				return
			}
		}
		// R5-P3-01: 服务未找到
		if errors.Is(err, ErrServiceNotFound) {
			logger.Warnf("service not found: service=%s method=%s", service, method)
			response.Fail(c, http.StatusNotFound, int(errcode.CodeGwParam), "service not found")
			c.Abort()
			return
		}
		errMsg := err.Error()
		// 请求体序列化错误
		if strings.Contains(errMsg, "json convert to protobuf failed") ||
			strings.Contains(errMsg, "marshal message failed") ||
			strings.Contains(errMsg, "failed to unmarshal") ||
			strings.Contains(errMsg, "cannot parse") {
			logger.Warnf("request serialization failed: service=%s method=%s err=%v", service, method, err)
			response.Fail(c, http.StatusBadRequest, int(errcode.CodeGwParam), "请求参数类型错误，请检查字段类型")
			c.Abort()
			return
		}
		// 内部错误
		logger.Errorf("rpc call failed: service=%s method=%s err=%v", service, method, err)
		response.Fail(c, http.StatusInternalServerError, int(errcode.CodeGwServer), "internal server error")
		c.Abort()
		return
	}

	// 成功响应处理
	if s, ok := resp.(string); ok && s != "" {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			logger.Warnf("rpc response unmarshal failed: service=%s method=%s err=%v", service, method, err)
			response.Fail(c, http.StatusInternalServerError, int(errcode.CodeGwServer), "internal server error")
			c.Abort()
			return
		}

		if codeRaw, hasCode := m["code"]; hasCode {
			code, ok := codeRaw.(float64)
			if !ok {
				logger.Warnf("rpc response has non-numeric code: service=%s method=%s code=%v", service, method, codeRaw)
				response.Fail(c, http.StatusInternalServerError, int(errcode.CodeGwServer), "internal server error")
				c.Abort()
				return
			}

			if int(code) != 0 {
				msg, _ := m["message"].(string)
				// 使用统一的 HttpStatus 映射，不直接透传下游 code 为 HTTP 状态码，
				// 防止下游服务控制网关返回任意 4xx/5xx 状态码。
				httpCode := response.HttpStatus(int(code))
				c.JSON(httpCode, response.R{
					Code:    int(code),
					Message: msg,
					Data:    m["data"],
				})
				c.Abort()
				return
			}

			response.OK(c, m["data"])
			c.Abort()
			return
		}

		response.OK(c, m)
		c.Abort()
		return
	}

	response.OK(c, resp)
	c.Abort()
}
