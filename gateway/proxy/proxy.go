package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/gopkg/cloud/metainfo"
	dproto "github.com/cloudwego/dynamicgo/proto"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/client/genericclient"
	"github.com/cloudwego/kitex/pkg/discovery"
	"github.com/cloudwego/kitex/pkg/generic"
	"github.com/cloudwego/kitex/pkg/transmeta"
	kotelkitex "github.com/kitex-contrib/obs-opentelemetry/tracing"

	clientv3 "go.etcd.io/etcd/client/v3"

	"shopmicro/pkg/errcode"
	pkgjwt "shopmicro/pkg/jwt"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/redis"
	"shopmicro/pkg/response"

	"shopmicro/gateway/middleware"
)

const (
	// etcdRegistryPrefix 是 kitex registry-etcd 使用的键命名空间。
	etcdRegistryPrefix = "kitex/registry-etcd"
	tokenPrefix        = "shopmicro:user:token:"

	// P3-07: query 参数最大数量，防止 URL 过长导致解析 DoS
	maxQueryParams = 20

	// P3-03: etcd 连接超时
	etcdDialTimeout = 5 * time.Second
)

// ServiceClient 将 kitex 泛化客户端与其描述符绑定。
type ServiceClient struct {
	Client  genericclient.Client
	Generic generic.Generic
	svcDesc *dproto.ServiceDescriptor // proto 服务描述符，用于字段类型查询
}

// Proxy 是核心的 HTTP→gRPC 泛化代理。
type Proxy struct {
	mu sync.RWMutex

	clients map[string]*ServiceClient

	jwtParser    *pkgjwt.Parser
	rdb          *redis.Client
	publicMethod map[string]bool
	// P1-12: 每个服务的允许方法列表（nil = 允许所有方法）
	allowedMethods map[string]map[string]bool
	// httpMethodMap 限制每个 RPC 方法允许的 HTTP 方法（GET/POST/PUT/DELETE）。
	// 键格式为 "service/method"（例如 "user/Login" → {"POST": true}）。
	// 如果某个方法未注册，则允许所有 HTTP 方法。
	httpMethodMap map[string]map[string]bool

	// epRateLimitFn 在代理处理器中检查按端点限流。
	// 返回 true 表示允许，false 表示拒绝。nil = 不进行端点限流。
	epRateLimitFn func(ctx context.Context, endpointKey, ip string) bool
}

// Option 在构建时配置 Proxy。
type Option func(*Proxy)

// WithJWT 设置用于验证入站令牌的 JWT 密钥。
// 如果密钥太短（< 32 字符），在启动时 panic。
// issuer 是可选的 —— 如果非空，"iss" 不匹配的令牌将被拒绝。
func WithJWT(secret string, issuer string) Option {
	parser, err := pkgjwt.NewParser(secret, issuer)
	if err != nil {
		logger.Fatalf("jwt parser init failed: %v", err)
	}
	return func(p *Proxy) {
		p.jwtParser = parser
	}
}

// WithRedis 设置用于令牌验证的 Redis 客户端。
func WithRedis(rdb *redis.Client) Option {
	return func(p *Proxy) {
		p.rdb = rdb
	}
}

// WithEndpointRateLimit 注入一个按端点限流检查器。
// 检查器接收 (ctx, "service/method", clientIP) 并返回 true=允许。
// 这确保端点限流不依赖于 HTTP 方法。
func WithEndpointRateLimit(fn func(ctx context.Context, endpointKey, ip string) bool) Option {
	return func(p *Proxy) {
		p.epRateLimitFn = fn
	}
}

// WithPublicMethods 标记不需要 JWT 令牌的 RPC 方法。
// 键格式为 "service/method"（例如 "user/Login"），以防止
// 跨服务认证绕过 —— 标记为 user 服务公开的 "Login"
// 不能跳过 "order/Login" 方法的认证。
func WithPublicMethods(service string, methods ...string) Option {
	return func(p *Proxy) {
		for _, m := range methods {
			p.publicMethod[service+"/"+m] = true
		}
	}
}

// New 使用给定选项创建 Proxy。
func New(opts ...Option) *Proxy {
	p := &Proxy{
		clients:        make(map[string]*ServiceClient),
		publicMethod:   make(map[string]bool),
		allowedMethods: make(map[string]map[string]bool),
		httpMethodMap:  make(map[string]map[string]bool),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// WithAllowedMethods 注册每个服务的 RPC 方法白名单。
// 示例: WithAllowedMethods("user", "RegisterUser", "Login", "Logout")
// 如果未为某个服务注册方法，则允许所有方法。
func WithAllowedMethods(service string, methods ...string) Option {
	return func(p *Proxy) {
		m := make(map[string]bool)
		for _, method := range methods {
			m[method] = true
		}
		p.allowedMethods[service] = m
	}
}

// WithHTTPMethod 限制指定 RPC 方法允许的 HTTP 请求方法。
// 键格式为 "service/method"，值为允许的 HTTP 方法（GET/POST/PUT/DELETE）。
// 示例: WithHTTPMethod("user", "Login", http.MethodPost)
//
//	WithHTTPMethod("user", "ListUser", http.MethodGet, http.MethodPost)
//
// 如果某个 RPC 方法未注册，则允许所有 HTTP 方法。
func WithHTTPMethod(service, method string, httpMethods ...string) Option {
	return func(p *Proxy) {
		m := make(map[string]bool)
		for _, hm := range httpMethods {
			m[strings.ToUpper(hm)] = true
		}
		p.httpMethodMap[service+"/"+method] = m
	}
}

// =========================
// 注册服务
// =========================

// registerService 为给定服务创建 Kitex 泛化客户端，
// 并缓存到 p.clients 中。幂等操作：对同一个 serviceName
// 的第二次调用是空操作。
func (p *Proxy) registerService(
	ctx context.Context,
	serviceName string,
	protoPath string,
	resolver discovery.Resolver,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.clients[serviceName]; ok {
		return nil
	}

	// 从磁盘上的 proto 文件构建 PbDescriptorProvider。
	// JSONPbGeneric 需要 PbDescriptorProviderDynamicGo，由
	// NewPbFileProviderWithDynamicGo 提供。
	provider, err := generic.NewPbFileProviderWithDynamicGo(
		protoPath,
		ctx,
		dproto.Options{},
		filepath.Dir(protoPath), // 在同一目录中搜索导入
	)
	if err != nil {
		return fmt.Errorf("create pb provider for %s (%s): %w", serviceName, protoPath, err)
	}

	// 单独解析 proto 描述符用于字段类型查询（query param 类型感知转换）。
	// 不能读 provider.Provide() channel，因为 JSONPbGeneric 内部也要读同一个带缓冲 1 的 channel，
	// 提前消费会导致 JSONPbGeneric 永久阻塞。
	svcDesc, err := dproto.Options{}.NewDescriptorFromPath(ctx, protoPath, filepath.Dir(protoPath))
	if err != nil {
		logger.Warnf("parse descriptor for %s: %v (query param type conversion disabled)", serviceName, err)
	}

	// JSONPbGeneric: 网关接收 JSON 请求体；Kitex 在线路上序列化为 PB。
	g, err := generic.JSONPbGeneric(provider)
	if err != nil {
		_ = provider.Close()
		return fmt.Errorf("create json-pb generic for %s: %w", serviceName, err)
	}

	// P2-07: 在 Call() 中手动重试，而非使用 client.WithFailureRetry ——
	// 这样我们可以完全控制哪些错误可重试。
	cli, err := genericclient.NewClient(
		serviceName,
		g,
		client.WithResolver(resolver),
		client.WithRPCTimeout(5*time.Second),
		client.WithConnectTimeout(3*time.Second),
		client.WithMetaHandler(transmeta.MetainfoClientHandler),
		client.WithSuite(kotelkitex.NewClientSuite(
			kotelkitex.WithEnableGRPCMetadata(),
		)),
	)
	if err != nil {
		_ = provider.Close()
		return fmt.Errorf("create kitex client for %s: %w", serviceName, err)
	}

	p.clients[serviceName] = &ServiceClient{
		Client:  cli,
		Generic: g,
		svcDesc: svcDesc,
	}

	logger.Infof("service registered: %s (proto: %s)", serviceName, protoPath)
	return nil
}

// =========================
// 获取客户端
// =========================

func (p *Proxy) getClient(service string) (*ServiceClient, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	c, ok := p.clients[service]
	return c, ok
}

// =========================
// 调用 RPC
// =========================

// Call 对指定的服务和方法执行泛化 RPC 调用。
// req 必须是 JSON 编码的字符串（用于 JSONPbGeneric）。
// method 必须是 CamelCase 的 protobuf RPC 名称（例如 "ListUser"，而非
// gRPC 全路径 "api.user.v1.UserService/ListUser"）。
func (p *Proxy) Call(
	ctx context.Context,
	service string,
	method string,
	req interface{},
) (interface{}, error) {
	cli, ok := p.getClient(service)
	if !ok {
		return nil, fmt.Errorf("service not found: %s", service)
	}

	return cli.Client.GenericCall(ctx, method, req)
}

// =========================
// 发现 & 监听服务
// =========================

// DiscoverAndRegister 扫描 Etcd 中所有当前已注册的 Kitex 服务，
// 并为每个服务注册一个泛化客户端。
func (p *Proxy) DiscoverAndRegister(
	resolver discovery.Resolver,
	etcdAddrs []string,
	protoBase string,
) error {
	etcdClient, err := newEtcdClient(etcdAddrs)
	if err != nil {
		return err
	}
	defer etcdClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 键存储格式:  kitex/registry-etcd/{serviceName}/{addr}
	prefix := etcdRegistryPrefix + "/"
	resp, err := etcdClient.Get(ctx, prefix, clientv3.WithPrefix(), clientv3.WithKeysOnly())
	if err != nil {
		return fmt.Errorf("etcd list services: %w", err)
	}

	seen := make(map[string]struct{})
	for _, kv := range resp.Kvs {
		svcName := extractServiceName(string(kv.Key), prefix)
		if svcName == "" {
			continue
		}
		if _, done := seen[svcName]; done {
			continue
		}
		seen[svcName] = struct{}{}

		protoPath := buildProtoPath(protoBase, svcName)

		// P2-06: 如果 proto 文件缺失则快速失败 —— 网关
		// 没有它就无法为该服务处理任何请求。
		if _, err := os.Stat(protoPath); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("proto file not found for service %q: %s (protoBase=%s) — cannot start without it",
					svcName, protoPath, protoBase)
			}
			return fmt.Errorf("stat proto file for service %q: %w", svcName, err)
		}

		if err := p.registerService(ctx, svcName, protoPath, resolver); err != nil {
			logger.Warnf("failed to register service %s: %v", svcName, err)
		}
	}
	return nil
}

// WatchServices 监听 Etcd 中新注册的服务并自动注册。
// 它在后台协程中运行，立即返回。
// P1-03: 接受生命周期上下文用于取消，并在 etcd watch 通道
// 意外关闭时添加指数退避的重连逻辑。
func (p *Proxy) WatchServices(
	ctx context.Context,
	resolver discovery.Resolver,
	etcdAddrs []string,
	protoBase string,
) {
	go func() {
		const maxBackoff = 30 * time.Second
		backoff := time.Second

		for {
			if ctx.Err() != nil {
				return // 父上下文已取消
			}

			err := p.watchOnce(ctx, resolver, etcdAddrs, protoBase)

			if ctx.Err() != nil {
				return
			}

			// 退避: 1s -> 2s -> 4s -> ... -> 30s 上限。
			if err == nil {
				backoff = time.Second
			} else {
				// Watch 通道关闭（etcd 连接丢失或其他错误）
				logger.Warnf("WatchServices: watch channel closed, reconnecting in %v: %v", backoff, err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
		}
	}()
}

// watchOnce 执行单次 etcd watch 循环，直到通道关闭或上下文取消。
func (p *Proxy) watchOnce(
	ctx context.Context,
	resolver discovery.Resolver,
	etcdAddrs []string,
	protoBase string,
) error {
	etcdClient, err := newEtcdClient(etcdAddrs)
	if err != nil {
		return fmt.Errorf("connect to etcd: %w", err)
	}
	defer etcdClient.Close()

	prefix := etcdRegistryPrefix + "/"
	// P1-03: 使用生命周期上下文而非 context.Background()
	watchCh := etcdClient.Watch(ctx, prefix, clientv3.WithPrefix())

	for wresp := range watchCh {
		if wresp.Err() != nil {
			return fmt.Errorf("etcd watch error: %w", wresp.Err())
		}
		for _, ev := range wresp.Events {
			if ev.Type != clientv3.EventTypePut {
				continue
			}
			svcName := extractServiceName(string(ev.Kv.Key), prefix)
			if svcName == "" {
				continue
			}
			if _, ok := p.getClient(svcName); ok {
				continue // 已注册
			}
			protoPath := buildProtoPath(protoBase, svcName)

			// DiscoverAndRegister 会捕获缺失文件并失败。
			if _, err := os.Stat(protoPath); err != nil {
				if os.IsNotExist(err) {
					logger.Warnf("WatchServices: proto file not found for %s: %s — service registered but not usable until proto is in place", svcName, protoPath)
					continue
				}
				logger.Warnf("WatchServices: stat proto for %s: %v", svcName, err)
				continue
			}

			// R2-P2-04: 对瞬态错误最多重试注册 3 次
			regErr := p.retryRegister(ctx, svcName, protoPath, resolver, 3)
			if regErr != nil {
				logger.Warnf("WatchServices: failed to register %s after retries: %v", svcName, regErr)
			}
		}
	}

	return fmt.Errorf("watch channel closed")
}

// =========================
// HTTP 处理器
// =========================

// Handler 返回一个 Hertz HandlerFunc，将
// /api/v1/:service/:method 请求分发到对应的 gRPC 服务。
func (p *Proxy) Handler() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		service := c.Param("service")
		method := toMethodName(c.Param("method"))

		// R2-P1-02: 拒绝无效的方法名（例如路径遍历、特殊字符）
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
		// 如果 allowedMethods 非空，未显式注册的任何服务将被拒绝。
		if len(p.allowedMethods) > 0 {
			allowed, ok := p.allowedMethods[service]
			if !ok {
				// P2-02: 返回通用错误以防止服务/方法枚举
				logger.Warnf("rejected request: service not in whitelist (service=%s)", service)
				response.Fail(c, http.StatusForbidden, int(errcode.CodeGwParam), "access denied")
				c.Abort()
				return
			}
			if !allowed[method] {
				// P2-02: 返回通用错误以防止方法枚举
				logger.Warnf("rejected request: method not allowed (service=%s, method=%s)", service, method)
				response.Fail(c, http.StatusForbidden, int(errcode.CodeGwParam), "access denied")
				c.Abort()
				return
			}
		}

		// P3-03: 验证 POST/PUT 请求的 Content-Type 为 application/json
		reqMethod := strings.ToUpper(string(c.Request.Method()))
		if reqMethod == http.MethodPost || reqMethod == http.MethodPut {
			ct := string(c.ContentType())
			if ct != "" && !strings.HasPrefix(ct, "application/json") {
				response.Fail(c, http.StatusUnsupportedMediaType, int(errcode.CodeGwParam), "content type must be application/json")
				c.Abort()
				return
			}
		}

		// HTTP 方法校验 —— 限制每个 RPC 方法允许的 GET/POST/PUT/DELETE。
		if methods, ok := p.httpMethodMap[service+"/"+method]; ok {
			if !methods[reqMethod] {
				logger.Warnf("rejected request: http method not allowed (service=%s, method=%s, http=%s)", service, method, reqMethod)
				response.Fail(c, http.StatusMethodNotAllowed, int(errcode.CodeGwParam), "method not allowed")
				c.Abort()
				return
			}
		}

		// P3-07: 限制 query 参数数量防止 DoS
		if queryString := c.Request.QueryString(); len(queryString) > 0 {
			paramCount := strings.Count(string(queryString), "&") + 1
			if paramCount > maxQueryParams {
				response.Fail(c, http.StatusBadRequest, int(errcode.CodeGwParam), "too many query parameters")
				c.Abort()
				return
			}
		}

		// 按端点限流（与方法无关 —— 适用于 GET/POST/PUT/DELETE）。
		if p.epRateLimitFn != nil {
			epKey := service + "/" + method
			ip := middleware.ClientRealIP(c)
			if !p.epRateLimitFn(ctx, epKey, ip) {
				response.Fail(c, http.StatusTooManyRequests, int(errcode.CodeGwRateLimit), "too many requests")
				c.Abort()
				return
			}
		}

		// JWT 认证 —— 对公开方法跳过（键格式: "service/method"）。
		authCtx, ok := p.authenticate(ctx, c, service, method)
		if !ok {
			return
		}
		ctx = authCtx

		// Pre-gzip 缓存命中检查：跳过 RPC 调用，直接投递预压缩响应
		if string(c.Request.Method()) == http.MethodGet {
			ae := string(c.Request.Header.Peek("Accept-Encoding"))
			if strings.Contains(ae, "gzip") {
				if gzipped, ok := getPreGzip(preGzipCacheKey(service, method, c.Request.QueryString())); ok {
					servePreGzip(c, gzipped)
					return
				}
			}
		}

		// 将请求体传递给泛化客户端。
		// GET 请求的参数通常在 query string 中，需要合并到 JSON body。
		body := string(c.Request.Body())
		if body == "" || body == "null" {
			body = "{}"
		}
		// 将 query string 参数合并到 JSON body 中（主要用于 GET 请求的分页参数等）。
		// 使用 proto 描述符做类型感知转换，避免 string 字段的值被错误转为数字。
		if queryString := c.Request.QueryString(); len(queryString) > 0 {
			if sc, ok := p.getClient(service); ok && sc.svcDesc != nil {
				if merged, err := p.mergeQueryToJSONTyped(body, string(queryString), sc.svcDesc, method); err == nil {
					body = merged
				} else {
					logger.Warnf("merge query to json failed: %v", err)
				}
			} else if merged, err := mergeQueryToJSON(body, string(queryString)); err == nil {
				body = merged
			} else {
				logger.Warnf("merge query to json (fallback) failed: %v", err)
			}
		}

		resp, err := p.Call(ctx, service, method, body)

		// Pre-gzip 缓存回填：成功 GET 响应预压缩后缓存，后续直接投递
		if err == nil {
			if p.tryPreGzipCache(c, service, method, resp) {
				return
			}
		}

		p.handleRPCResponse(c, service, method, resp, err)
	}
}

// authenticate 验证 JWT 令牌并检查 Redis 中的令牌状态。
// 如果认证成功或方法是公开的，返回增强的上下文（含 metainfo）。
// 如果认证失败，返回 false 并写入错误响应。
func (p *Proxy) authenticate(ctx context.Context, c *app.RequestContext, service, method string) (context.Context, bool) {
	if p.jwtParser == nil || p.publicMethod[service+"/"+method] {
		return ctx, true
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

	// P1-1: 验证令牌在 Redis SET 中仍然有效（多设备支持）。
	// 使用 SISMEMBER 检查令牌是否属于用户的活跃令牌集合。
	// SISMEMBER 对不存在的键返回 (false, nil) —— 没有 redis.Nil 歧义。
	if p.rdb != nil {
		tokenKey := fmt.Sprintf("%s%d", tokenPrefix, claims.UserID)
		isMember, err := p.rdb.SIsMember(ctx, tokenKey, tokenStr)
		if err != nil {
			// Redis 连接错误时执行失败关闭，确保已撤销的令牌不被接受。
			logger.Warnf("redis sismember failed (fail-closed): user_id=%d err=%v", claims.UserID, err)
			response.Fail(c, http.StatusServiceUnavailable, int(errcode.CodeGwSrvUnavailable), "service temporarily unavailable")
			c.Abort()
			return nil, false
		} else if !isMember {
			// 令牌不在用户的活跃集合中 → 已撤销或已过期。
			response.Unauthorized(c, "token revoked")
			c.Abort()
			return nil, false
		}
	}

	// 通过 Kitex metainfo 将身份信息转发给下游服务。
	ctx = metainfo.WithPersistentValue(ctx, "token", tokenStr)
	ctx = metainfo.WithPersistentValue(ctx, "user_id", fmt.Sprintf("%d", claims.UserID))

	// 在 Hertz 请求上下文中设置 user_id，供下游中间件使用（例如按用户限流）
	c.Set("user_id", fmt.Sprintf("%d", claims.UserID))

	return ctx, true
}

// handleRPCResponse 处理 RPC 调用结果并写入 HTTP 响应。
// 处理熔断器错误、下游业务错误和包装的 JSON 响应。
func (p *Proxy) handleRPCResponse(c *app.RequestContext, service, method string, resp interface{}, err error) {
	if err != nil {
		// 尝试解析下游 BizError（支持 Kitex 远程错误包装格式）。
		// 优先检查原始 err，再检查 unwrap 后的 err。
		if biz, ok := errcode.ParseBizError(err); ok {
			// 业务错误是客户端问题，不记 ERROR 日志
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
		// 请求体序列化错误 —— JSON 字段类型与 proto 定义不匹配。
		// 这是客户端问题，返回 400 并提示检查请求参数。
		errMsg := err.Error()
		if strings.Contains(errMsg, "service not found") {
			logger.Warnf("service not found: service=%s method=%s", service, method)
			response.Fail(c, http.StatusNotFound, int(errcode.CodeGwParam), "service not found")
			c.Abort()
			return
		}
		if strings.Contains(errMsg, "json convert to protobuf failed") ||
			strings.Contains(errMsg, "marshal message failed") ||
			strings.Contains(errMsg, "failed to unmarshal") ||
			strings.Contains(errMsg, "cannot parse") {
			logger.Warnf("request serialization failed: service=%s method=%s err=%v", service, method, err)
			response.Fail(c, http.StatusBadRequest, int(errcode.CodeGwParam), "请求参数类型错误，请检查字段类型")
			c.Abort()
			return
		}
		// 内部/网络错误 —— 绝不暴露给客户端。
		logger.Errorf("rpc call failed: service=%s method=%s err=%v", service, method, err)
		response.Fail(c, http.StatusInternalServerError, int(errcode.CodeGwServer), "internal server error")
		c.Abort()
		return
	}

	// P0 修复（修订 P2-06）：单次反序列化为 map，然后根据 "code" 字段分支。
	// 避免对包装响应的同一 JSON 进行两次反序列化。
	if s, ok := resp.(string); ok && s != "" {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			// P1-05: 反序列化失败不泄露原始内容，返回通用错误
			logger.Warnf("rpc response unmarshal failed: service=%s method=%s err=%v", service, method, err)
			response.Fail(c, http.StatusInternalServerError, int(errcode.CodeGwServer), "internal server error")
			c.Abort()
			return
		}

		// 通过 "code" 字段的存在检测包装响应
		if codeRaw, hasCode := m["code"]; hasCode {
			// P0-02: 严格检查 code 类型，非 float64 视为异常
			code, ok := codeRaw.(float64)
			if !ok {
				logger.Warnf("rpc response has non-numeric code: service=%s method=%s code=%v", service, method, codeRaw)
				response.Fail(c, http.StatusInternalServerError, int(errcode.CodeGwServer), "internal server error")
				c.Abort()
				return
			}

			if int(code) != 0 {
				msg, _ := m["message"].(string)
				c.JSON(response.HttpStatus(int(code)), response.R{
					Code:    int(code),
					Message: msg,
					Data:    m["data"],
				})
				c.Abort()
				return
			}

			// Code == 0（成功包装）：提取 data 字段
			response.OK(c, m["data"])
			c.Abort()
			return
		}

		// 原始 protobuf 响应 —— 将整个 body 包装为 "data"
		response.OK(c, m)
		c.Abort()
		return
	}

	// 非 string 响应（理论上不会发生，JSONPbGeneric 始终返回 string）
	response.OK(c, resp)
	c.Abort()
}

// =========================
// 辅助函数
// =========================

// methodNameRegex 验证方法名只包含字母数字字符
// 且以字母开头。防止通过 URL 参数进行路径遍历或注入。
var methodNameRegex = regexp.MustCompile(`^[A-Za-z][a-zA-Z0-9]*$`)

// P2-01: serviceRegex 验证服务名只包含小写字母数字字符。
// 防止在构建 proto 文件路径时发生路径注入。
var serviceRegex = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// toMethodName 确保方法名的首字母大写。
// 如果方法名不符合格式验证，返回空字符串。
func toMethodName(method string) string {
	if method == "" || !methodNameRegex.MatchString(method) {
		return ""
	}
	return strings.ToUpper(method[:1]) + method[1:]
}

func bearerToken(authHeader string) (string, bool) {
	authHeader = strings.TrimSpace(authHeader)
	if authHeader == "" {
		return "", false
	}
	parts := strings.Fields(authHeader)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// buildProtoPath 返回 {protoBase}/{service}/api/{service}.proto。
func buildProtoPath(protoBase, service string) string {
	return filepath.Join(protoBase, service, "api", service+".proto")
}

// extractServiceName 从 Etcd 键中解析服务名。
// 键格式: {prefix}{serviceName}/{addr}
func extractServiceName(key, prefix string) string {
	trimmed := strings.TrimPrefix(key, prefix)
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	return parts[0]
}

// retryRegister 尝试注册服务，最多重试 maxRetries 次，带退避。
// 如果所有尝试都失败，返回最后一个错误。
func (p *Proxy) retryRegister(ctx context.Context, serviceName, protoPath string, resolver discovery.Resolver, maxRetries int) error {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if err := p.registerService(ctx, serviceName, protoPath, resolver); err != nil {
			lastErr = err
			if i < maxRetries-1 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(i+1) * time.Second):
				}
			}
			continue
		}
		return nil
	}
	return lastErr
}

// newEtcdClient 创建一个最小化的 etcd v3 客户端。
func newEtcdClient(addrs []string) (*clientv3.Client, error) {
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   addrs,
		DialTimeout: etcdDialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to etcd %v: %w", addrs, err)
	}
	return cli, nil
}

// mergeQueryToJSON 将 URL query string 参数合并到 JSON body 中。
// query 参数保持 string 类型，不做 int/bool 自动转换。
func mergeQueryToJSON(body, queryString string) (string, error) {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return body, fmt.Errorf("parse body: %w", err)
	}
	if m == nil {
		m = make(map[string]interface{})
	}

	// Hertz QueryString 返回未转义的原始 query string
	params, err := url.ParseQuery(queryString)
	if err != nil {
		return body, fmt.Errorf("parse query: %w", err)
	}

	for key, values := range params {
		if len(values) == 0 {
			continue
		}
		// body 中已有的字段优先，query param 不覆盖
		if _, exists := m[key]; exists {
			continue
		}
		// query param 一律保持 string —— HTTP query string 本身就是 string 类型。
		// 不做 int/bool 自动转换，避免把 adCode="534000" 错转为数字导致 proto string 字段序列化失败。
		m[key] = values[0]
	}

	out, err := json.Marshal(m)
	if err != nil {
		return body, fmt.Errorf("marshal merged: %w", err)
	}
	return string(out), nil
}

// mergeQueryToJSONTyped 使用 proto 服务描述符做类型感知的 query→JSON 合并。
// 根据 proto 字段类型决定是否将 query value 转为数字/布尔，
// 避免 string 字段（如 adCode）被错误转为数字导致 Kitex 序列化失败。
func (p *Proxy) mergeQueryToJSONTyped(body, queryString string, svcDesc *dproto.ServiceDescriptor, method string) (string, error) {
	// 查找方法描述符，获取输入消息类型
	mtdDesc := svcDesc.LookupMethodByName(method)
	if mtdDesc == nil {
		// 找不到方法描述符，回退到无类型转换
		return mergeQueryToJSON(body, queryString)
	}
	inputMsg := mtdDesc.Input().Message()
	if inputMsg == nil {
		return mergeQueryToJSON(body, queryString)
	}

	var m map[string]interface{}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return body, fmt.Errorf("parse body: %w", err)
	}
	if m == nil {
		m = make(map[string]interface{})
	}

	params, err := url.ParseQuery(queryString)
	if err != nil {
		return body, fmt.Errorf("parse query: %w", err)
	}

	for key, values := range params {
		if len(values) == 0 {
			continue
		}
		// body 中已有的字段优先，query param 不覆盖
		if _, exists := m[key]; exists {
			continue
		}
		val := values[0]

		// 查询 proto 字段类型（优先用 JSON name，再用 proto name）
		field := inputMsg.ByJSONName(key)
		if field == nil {
			field = inputMsg.ByName(key)
		}

		if field == nil {
			// 字段不在 proto 定义中，保持 string
			m[key] = val
			continue
		}

		ft := field.Type().Type()
		switch {
		case isProtoIntType(ft):
			if intVal, err := strconv.ParseInt(val, 10, 64); err == nil {
				m[key] = intVal
			} else {
				m[key] = val
			}
		case isProtoFloatType(ft):
			if floatVal, err := strconv.ParseFloat(val, 64); err == nil {
				m[key] = floatVal
			} else {
				m[key] = val
			}
		case ft == dproto.BOOL:
			if boolVal, err := strconv.ParseBool(val); err == nil {
				m[key] = boolVal
			} else {
				m[key] = val
			}
		default:
			// STRING, MESSAGE, BYTES, ENUM 等 —— 保持 string
			m[key] = val
		}
	}

	out, err := json.Marshal(m)
	if err != nil {
		return body, fmt.Errorf("marshal merged: %w", err)
	}
	return string(out), nil
}

// isProtoIntType 判断字段类型是否为整数类型。
func isProtoIntType(t dproto.Type) bool {
	switch t {
	case dproto.INT32, dproto.SINT32, dproto.UINT32, dproto.SFIX32, dproto.FIX32,
		dproto.INT64, dproto.SINT64, dproto.UINT64, dproto.SFIX64, dproto.FIX64:
		return true
	}
	return false
}

// isProtoFloatType 判断字段类型是否为浮点类型。
func isProtoFloatType(t dproto.Type) bool {
	return t == dproto.FLOAT || t == dproto.DOUBLE
}
