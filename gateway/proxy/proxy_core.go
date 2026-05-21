package proxy

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	dproto "github.com/cloudwego/dynamicgo/proto"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/client/genericclient"
	"github.com/cloudwego/kitex/pkg/connpool"
	"github.com/cloudwego/kitex/pkg/discovery"
	"github.com/cloudwego/kitex/pkg/generic"
	"github.com/cloudwego/kitex/pkg/transmeta"
	kotelkitex "github.com/kitex-contrib/obs-opentelemetry/tracing"

	pkgjwt "shopmicro/pkg/jwt"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/redis"
)

const (
	etcdRegistryPrefix = "kitex/registry-etcd"
	tokenPrefix        = "shopmicro:user:token:"
	maxQueryParams     = 20
	etcdDialTimeout    = 5 * time.Second
	maxBodySize        = 900 * 1024
	maxTokenLen        = 4096
	tokenCacheTTL      = 30 * time.Second // token 有效性本地缓存时间
)

var ErrServiceNotFound = errors.New("service not found")
var errWatchClosed = errors.New("watch channel closed")

var methodNameRegex = regexp.MustCompile(`^[A-Za-z][a-zA-Z0-9]*$`)
var serviceRegex = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

type ServiceClient struct {
	Client  genericclient.Client
	Generic generic.Generic
	svcDesc *dproto.ServiceDescriptor
}

type Proxy struct {
	mu               sync.RWMutex
	clients          map[string]*ServiceClient
	instanceKeys     map[string]map[string]bool // 每个服务的 etcd 实例 key 集合（用于准确计数）
	jwtParser        *pkgjwt.Parser
	rdb              *redis.Client
	publicMethod     map[string]bool
	allowedMethods   map[string]map[string]bool
	httpMethodMap    map[string]map[string]bool
	epRateLimitFn    func(ctx context.Context, endpointKey, ip string) bool
	cacheableMethods map[string]bool
	methodTimeout    map[string]time.Duration
	cb               *CircuitBreaker
	rpcTimeout       time.Duration
	connectTimeout   time.Duration
	maxRetries       int
	registerRetries  int
	// tokenCache 缓存 token 有效性，避免每次请求都查 Redis SIsMember
	// key: token string, value: tokenCacheEntry
	tokenCache sync.Map
}

type Option func(*Proxy)

func WithJWT(secret string, issuer string) Option {
	parser, err := pkgjwt.NewParser(secret, issuer)
	if err != nil {
		logger.Fatalf("jwt parser init failed: %v", err)
	}
	return func(p *Proxy) {
		p.jwtParser = parser
	}
}

func WithRedis(rdb *redis.Client) Option {
	return func(p *Proxy) {
		p.rdb = rdb
	}
}

func WithEndpointRateLimit(fn func(ctx context.Context, endpointKey, ip string) bool) Option {
	return func(p *Proxy) {
		p.epRateLimitFn = fn
	}
}

func WithCacheableMethods(service string, methods ...string) Option {
	return func(p *Proxy) {
		for _, m := range methods {
			p.cacheableMethods[service+"/"+m] = true
		}
	}
}

func WithMethodTimeout(service, method string, timeout time.Duration) Option {
	if timeout <= 0 {
		logger.Fatalf("WithMethodTimeout: timeout must be positive, got %v (service=%s method=%s)", timeout, service, method)
	}
	return func(p *Proxy) {
		p.methodTimeout[service+"/"+method] = timeout
	}
}

func WithCircuitBreaker(cb *CircuitBreaker) Option {
	return func(p *Proxy) {
		p.cb = cb
	}
}

func WithPublicMethods(service string, methods ...string) Option {
	return func(p *Proxy) {
		for _, m := range methods {
			p.publicMethod[service+"/"+m] = true
		}
	}
}

func WithAllowedMethods(service string, methods ...string) Option {
	return func(p *Proxy) {
		m := make(map[string]bool)
		for _, method := range methods {
			m[method] = true
		}
		p.allowedMethods[service] = m
	}
}

func WithHTTPMethod(service, method string, httpMethods ...string) Option {
	return func(p *Proxy) {
		m := make(map[string]bool)
		for _, hm := range httpMethods {
			m[strings.ToUpper(hm)] = true
		}
		p.httpMethodMap[service+"/"+method] = m
	}
}

func WithRPCTimeout(d time.Duration) Option {
	if d <= 0 {
		d = 5 * time.Second
	}
	return func(p *Proxy) {
		p.rpcTimeout = d
	}
}

func WithConnectTimeout(d time.Duration) Option {
	if d <= 0 {
		d = 3 * time.Second
	}
	return func(p *Proxy) {
		p.connectTimeout = d
	}
}

func WithMaxRetries(n int) Option {
	if n < 0 {
		n = 0
	}
	return func(p *Proxy) {
		p.maxRetries = n
	}
}

func WithRegisterRetries(n int) Option {
	if n < 1 {
		n = 3
	}
	return func(p *Proxy) {
		p.registerRetries = n
	}
}

func New(opts ...Option) *Proxy {
	p := &Proxy{
		clients:          make(map[string]*ServiceClient),
		instanceKeys:     make(map[string]map[string]bool),
		publicMethod:     make(map[string]bool),
		allowedMethods:   make(map[string]map[string]bool),
		httpMethodMap:    make(map[string]map[string]bool),
		cacheableMethods: make(map[string]bool),
		methodTimeout:    make(map[string]time.Duration),
		rpcTimeout:       5 * time.Second,
		connectTimeout:   3 * time.Second,
		maxRetries:       2,
		registerRetries:  3,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *Proxy) getClient(service string) (*ServiceClient, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	c, ok := p.clients[service]
	return c, ok
}

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
	token := parts[1]
	if len(token) > maxTokenLen {
		return "", false
	}
	if strings.Count(token, ".") != 2 {
		return "", false
	}
	return token, true
}

func buildProtoPath(protoBase, service string) string {
	// 使用 path.Join（始终正斜杠），确保 Windows 本地开发也能正确加载 proto 文件
	return path.Join(protoBase, service, "api", service+".proto")
}

func extractServiceName(key, prefix string) string {
	trimmed := strings.TrimPrefix(key, prefix)
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	return parts[0]
}

func (p *Proxy) registerService(
	ctx context.Context,
	serviceName string,
	protoPath string,
	resolver discovery.Resolver,
) error {
	p.mu.RLock()
	if _, ok := p.clients[serviceName]; ok {
		p.mu.RUnlock()
		return nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.clients[serviceName]; ok {
		return nil
	}

	provider, err := generic.NewPbFileProviderWithDynamicGo(
		protoPath,
		ctx,
		dproto.Options{},
		path.Dir(protoPath))
	if err != nil {
		return fmt.Errorf("create pb provider for %s (%s): %w", serviceName, protoPath, err)
	}

	svcDesc, err := dproto.Options{}.NewDescriptorFromPath(ctx, protoPath, path.Dir(protoPath))
	if err != nil {
		logger.Warnf("parse descriptor for %s: %v (query param type conversion disabled)", serviceName, err)
	}

	g, err := generic.JSONPbGeneric(provider)
	if err != nil {
		if closeErr := provider.Close(); closeErr != nil {
			logger.Warnf("register %s: failed to close provider after error: %v", serviceName, closeErr)
		}
		return fmt.Errorf("create json-pb generic for %s: %w", serviceName, err)
	}

	cli, err := genericclient.NewClient(
		serviceName,
		g,
		client.WithResolver(resolver),
		client.WithRPCTimeout(p.rpcTimeout),
		client.WithConnectTimeout(p.connectTimeout),
		client.WithMetaHandler(transmeta.MetainfoClientHandler),
		client.WithSuite(kotelkitex.NewClientSuite(
			kotelkitex.WithEnableGRPCMetadata(),
		)),
		client.WithLongConnection(connpool.IdleConfig{
			MaxIdlePerAddress: 100,
			MaxIdleGlobal:     500,
			MaxIdleTimeout:    3 * time.Minute,
		}),
	)
	if err != nil {
		if closeErr := provider.Close(); closeErr != nil {
			logger.Warnf("register %s: failed to close provider after error: %v", serviceName, closeErr)
		}
		return fmt.Errorf("create kitex client for %s: %w", serviceName, err)
	}

	p.clients[serviceName] = &ServiceClient{
		Client:  cli,
		Generic: g,
		svcDesc: svcDesc,
	}
	p.instanceKeys[serviceName] = make(map[string]bool)

	logger.Infof("service registered: %s (proto: %s)", serviceName, protoPath)
	return nil
}

// tokenCacheEntry 缓存 token 有效性检查结果。
type tokenCacheEntry struct {
	valid     bool
	expiresAt time.Time
}

// checkTokenCache 检查 token 本地缓存，命中返回 (valid, true)，未命中返回 (false, false)。
func (p *Proxy) checkTokenCache(token string) (valid bool, hit bool) {
	val, ok := p.tokenCache.Load(token)
	if !ok {
		return false, false
	}
	entry, ok := val.(tokenCacheEntry)
	if !ok {
		p.tokenCache.Delete(token)
		return false, false
	}
	if time.Now().After(entry.expiresAt) {
		p.tokenCache.Delete(token)
		return false, false
	}
	return entry.valid, true
}

// setTokenCache 写入 token 有效性缓存。
func (p *Proxy) setTokenCache(token string, valid bool) {
	p.tokenCache.Store(token, tokenCacheEntry{
		valid:     valid,
		expiresAt: time.Now().Add(tokenCacheTTL),
	})
}

// InvalidateTokenCache 使指定 token 的缓存失效（用于 token 注销/刷新场景）。
func (p *Proxy) InvalidateTokenCache(token string) {
	p.tokenCache.Delete(token)
}

// tokenCacheCleanInterval 定期清理 token 缓存的间隔
const tokenCacheCleanInterval = 5 * time.Minute

// CleanTokenCache 清理所有过期的 token 缓存条目，防止已注销用户的 token 永驻内存。
// 应由定时器或清理 goroutine 定期调用。
func (p *Proxy) CleanTokenCache() {
	now := time.Now()
	p.tokenCache.Range(func(key, value interface{}) bool {
		entry, ok := value.(tokenCacheEntry)
		if !ok {
			p.tokenCache.Delete(key)
			return true
		}
		if now.After(entry.expiresAt) {
			p.tokenCache.Delete(key)
		}
		return true
	})
}

// StartTokenCacheCleaner 启动定期清理 token 缓存的 goroutine。
func (p *Proxy) StartTokenCacheCleaner(stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(tokenCacheCleanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				p.CleanTokenCache()
			}
		}
	}()
}
