package proxy

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	dproto "github.com/cloudwego/dynamicgo/proto"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/client/genericclient"
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
	instanceCount    map[string]int64 // 每个服务的 etcd 实例引用计数
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

func New(opts ...Option) *Proxy {
	p := &Proxy{
		clients:          make(map[string]*ServiceClient),
		instanceCount:    make(map[string]int64),
		publicMethod:     make(map[string]bool),
		allowedMethods:   make(map[string]map[string]bool),
		httpMethodMap:    make(map[string]map[string]bool),
		cacheableMethods: make(map[string]bool),
		methodTimeout:    make(map[string]time.Duration),
		rpcTimeout:       5 * time.Second,
		connectTimeout:   3 * time.Second,
		maxRetries:       2,
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
	return filepath.Join(protoBase, service, "api", service+".proto")
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
		filepath.Dir(protoPath),
	)
	if err != nil {
		return fmt.Errorf("create pb provider for %s (%s): %w", serviceName, protoPath, err)
	}

	svcDesc, err := dproto.Options{}.NewDescriptorFromPath(ctx, protoPath, filepath.Dir(protoPath))
	if err != nil {
		logger.Warnf("parse descriptor for %s: %v (query param type conversion disabled)", serviceName, err)
	}

	g, err := generic.JSONPbGeneric(provider)
	if err != nil {
		_ = provider.Close()
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
	p.instanceCount[serviceName] = 1

	logger.Infof("service registered: %s (proto: %s)", serviceName, protoPath)
	return nil
}
