package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
	"github.com/sony/gobreaker"

	clientv3 "go.etcd.io/etcd/client/v3"

	"shopmicro/pkg/errcode"
	pkgjwt "shopmicro/pkg/jwt"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/redis"
	"shopmicro/pkg/response"
)

const (
	// etcdRegistryPrefix is the key namespace used by kitex registry-etcd.
	etcdRegistryPrefix = "kitex/registry-etcd"
	tokenPrefix        = "shopmicro:user:token:"
)

// ServiceClient bundles a kitex generic client with its descriptor.
type ServiceClient struct {
	Client  genericclient.Client
	Generic generic.Generic
}

// Proxy is the central HTTP→gRPC generic proxy.
type Proxy struct {
	mu sync.RWMutex

	clients map[string]*ServiceClient

	jwtParser    *pkgjwt.Parser
	rdb          *redis.Client
	publicMethod map[string]bool
	// P1-12: Allowed methods per service (nil = allow all methods)
	allowedMethods map[string]map[string]bool

	// perServiceCB stores one circuit breaker per service.
	perServiceCB sync.Map // map[string]*gobreaker.CircuitBreaker
	cbSettings   *cbConfig

	// epRateLimitFn checks per-endpoint rate limits from the proxy handler.
	// Returns true if allowed, false if rejected. nil = no endpoint limiting.
	epRateLimitFn func(ctx context.Context, endpointKey, ip string) bool
}

// cbConfig holds circuit breaker configuration.
type cbConfig struct {
	maxRequests         uint32
	interval            time.Duration
	timeout             time.Duration
	consecutiveFailures uint32
}

// Option configures Proxy at construction time.
type Option func(*Proxy)

// WithJWT sets the JWT secret used to validate inbound tokens.
// Panics at startup if the secret is too short (< 32 chars).
// issuer is optional — if non-empty, tokens with a different "iss" are rejected.
func WithJWT(secret string, issuer string) Option {
	parser, err := pkgjwt.NewParser(secret, issuer)
	if err != nil {
		logger.Fatalf("jwt parser init failed: %v", err)
	}
	return func(p *Proxy) {
		p.jwtParser = parser
	}
}

// WithRedis sets the Redis client for token validation.
func WithRedis(rdb *redis.Client) Option {
	return func(p *Proxy) {
		p.rdb = rdb
	}
}

// WithCircuitBreaker enables per-service circuit breaking on RPC calls.
// When too many consecutive RPC failures occur for a service, subsequent
// calls are fast-rejected with gobreaker.ErrOpenState instead of waiting for timeout.
func WithCircuitBreaker(maxRequests int, interval, timeout time.Duration, consecutiveFailures uint32) Option {
	return func(p *Proxy) {
		p.cbSettings = &cbConfig{
			maxRequests:         uint32(maxRequests),
			interval:            interval,
			timeout:             timeout,
			consecutiveFailures: consecutiveFailures,
		}
	}
}

// WithEndpointRateLimit injects a per-endpoint rate limit checker.
// The checker receives (ctx, "service/method", clientIP) and returns true=allowed.
// This ensures endpoint limits are enforced regardless of HTTP method.
func WithEndpointRateLimit(fn func(ctx context.Context, endpointKey, ip string) bool) Option {
	return func(p *Proxy) {
		p.epRateLimitFn = fn
	}
}

// WithPublicMethods marks RPC methods that do NOT require a JWT token.
func WithPublicMethods(methods ...string) Option {
	return func(p *Proxy) {
		for _, m := range methods {
			p.publicMethod[m] = true
		}
	}
}

// New creates a Proxy with the given options.
func New(opts ...Option) *Proxy {
	p := &Proxy{
		clients:        make(map[string]*ServiceClient),
		publicMethod:   make(map[string]bool),
		allowedMethods: make(map[string]map[string]bool),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// WithAllowedMethods registers a whitelist of RPC methods per service.
// Example: WithAllowedMethods("user", "RegisterUser", "Login", "Logout")
// If no methods are registered for a service, all methods are allowed.
func WithAllowedMethods(service string, methods ...string) Option {
	return func(p *Proxy) {
		m := make(map[string]bool)
		for _, method := range methods {
			m[method] = true
		}
		p.allowedMethods[service] = m
	}
}

// =========================
// register service
// =========================

// registerService creates a Kitex generic client for the given service and
// caches it in p.clients. It is idempotent: a second call for the same
// serviceName is a no-op.
func (p *Proxy) registerService(
	serviceName string,
	protoPath string,
	resolver discovery.Resolver,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.clients[serviceName]; ok {
		return nil
	}

	// Build a PbDescriptorProvider from the proto file on disk.
	// JSONPbGeneric requires PbDescriptorProviderDynamicGo, provided by
	// NewPbFileProviderWithDynamicGo.
	provider, err := generic.NewPbFileProviderWithDynamicGo(
		protoPath,
		context.Background(),
		dproto.Options{},
		filepath.Dir(protoPath), // search imports in the same directory
	)
	if err != nil {
		return fmt.Errorf("create pb provider for %s (%s): %w", serviceName, protoPath, err)
	}

	// JSONPbGeneric: gateway receives JSON body; Kitex serialises to PB on the wire.
	g, err := generic.JSONPbGeneric(provider)
	if err != nil {
		_ = provider.Close()
		return fmt.Errorf("create json-pb generic for %s: %w", serviceName, err)
	}

	// P2-07: Manual retry in Call() instead of client.WithFailureRetry —
	// this gives us full control over which errors are retryable.
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
	}

	logger.Infof("service registered: %s (proto: %s)", serviceName, protoPath)
	return nil
}

// =========================
// get client
// =========================

func (p *Proxy) getClient(service string) (*ServiceClient, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	c, ok := p.clients[service]
	return c, ok
}

func (p *Proxy) getBreaker(service string) *gobreaker.CircuitBreaker {
	if p.cbSettings == nil {
		return nil
	}
	cbI, _ := p.perServiceCB.LoadOrStore(service, gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "gateway:" + service,
		MaxRequests: p.cbSettings.maxRequests,
		Interval:    p.cbSettings.interval,
		Timeout:     p.cbSettings.timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= p.cbSettings.consecutiveFailures
		},
	}))
	return cbI.(*gobreaker.CircuitBreaker)
}

// =========================
// call RPC
// =========================

// Call performs a generic RPC to the named service and method.
// req must be a JSON-encoded string (for JSONPbGeneric).
// method must be the CamelCase protobuf RPC name (e.g. "ListUser", NOT
// a gRPC full path like "api.user.v1.UserService/ListUser").
//
// Kitex handles connection-level retry internally (middlewares.go),
// so no manual retry loop is needed here — a single call suffices.
//
// If circuit breaker is enabled (via WithCircuitBreaker) and the breaker
// for the service is open, returns gobreaker.ErrOpenState immediately.
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

	call := func() (interface{}, error) {
		// JSONPbGeneric: WriteJSON.Write calls LookupMethodByName(method)
		// which expects CamelCase method name, NOT gRPC full path.
		// Use method directly; the generic client already knows the service.
		return cli.Client.GenericCall(ctx, method, req)
	}

	cb := p.getBreaker(service)
	if cb == nil {
		return call()
	}

	return cb.Execute(func() (interface{}, error) {
		return call()
	})
}

// =========================
// discover & watch services
// =========================

// DiscoverAndRegister scans Etcd for all currently registered Kitex services
// and registers a generic client for each one.
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

	// Keys are stored as:  kitex/registry-etcd/{serviceName}/{addr}
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

		// P2-06: fail-fast if the proto file is missing — the gateway cannot
		// serve any request to this service without it.
		if _, err := os.Stat(protoPath); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("proto file not found for service %q: %s (protoBase=%s) — cannot start without it",
					svcName, protoPath, protoBase)
			}
			return fmt.Errorf("stat proto file for service %q: %w", svcName, err)
		}

		if err := p.registerService(svcName, protoPath, resolver); err != nil {
			logger.Warnf("failed to register service %s: %v", svcName, err)
		}
	}
	return nil
}

// WatchServices watches Etcd for newly registered services and registers them
// automatically. It runs in a background goroutine and returns immediately.
// P1-03: Accepts a lifecycle context for cancellation and adds reconnect logic
// with exponential backoff when the etcd watch channel closes unexpectedly.
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
				return // parent context cancelled
			}

			err := p.watchOnce(ctx, resolver, etcdAddrs, protoBase)
			if ctx.Err() != nil {
				return
			}

			// Watch channel closed (etcd connection lost or other error)
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
	}()
}

// watchOnce performs a single etcd watch loop until the channel closes or context cancels.
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
	// P1-03: Use lifecycle context instead of context.Background()
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
				continue // already registered
			}
			protoPath := buildProtoPath(protoBase, svcName)

			// P2-06: warn but do not block — a newly-deployed service may
			// momentarily appear in etcd before its proto file is copied over
			// (e.g. rolling update race).  If it persists, the next startup
			// DiscoverAndRegister will catch the missing file and fail.
			if _, err := os.Stat(protoPath); err != nil {
				if os.IsNotExist(err) {
					logger.Warnf("WatchServices: proto file not found for %s: %s — service registered but not usable until proto is in place", svcName, protoPath)
					continue
				}
				logger.Warnf("WatchServices: stat proto for %s: %v", svcName, err)
				continue
			}

			// R2-P2-04: Retry registerService up to 3 times for transient errors
			regErr := p.retryRegister(svcName, protoPath, resolver, 3)
			if regErr != nil {
				logger.Warnf("WatchServices: failed to register %s after retries: %v", svcName, regErr)
			}
		}
	}

	return fmt.Errorf("watch channel closed")
}

// =========================
// HTTP handler
// =========================

// Handler returns a Hertz HandlerFunc that dispatches
// /api/v1/:service/:method requests to the corresponding gRPC service.
func (p *Proxy) Handler() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		service := c.Param("service")
		method := toMethodName(c.Param("method"))

		// R2-P1-02: Reject invalid method names (e.g. path traversal, special chars)
		if method == "" {
			response.Fail(c, http.StatusBadRequest, int(errcode.CodeGwParam), "invalid method name")
			c.Abort()
			return
		}
		// R2-P1-02: Reject empty or suspicious service names
		if service == "" || strings.ContainsAny(service, "./\\") {
			response.Fail(c, http.StatusBadRequest, int(errcode.CodeGwParam), "invalid service name")
			c.Abort()
			return
		}

		// P1-12: Service/method whitelist enforcement
		// If allowedMethods is non-empty, ANY service not explicitly registered is denied.
		if len(p.allowedMethods) > 0 {
			allowed, ok := p.allowedMethods[service]
			if !ok {
				// Service not in whitelist → reject
				response.Fail(c, http.StatusForbidden, int(errcode.CodeGwParam),
					fmt.Sprintf("service %s not allowed", service))
				c.Abort()
				return
			}
			if !allowed[method] {
				response.Fail(c, http.StatusForbidden, int(errcode.CodeGwParam),
					fmt.Sprintf("method %s not allowed for service %s", method, service))
				c.Abort()
				return
			}
		}

		// Per-endpoint rate limit (method-independent — works for GET/POST/PUT/DELETE).
		if p.epRateLimitFn != nil {
			epKey := service + "/" + method
			if !p.epRateLimitFn(ctx, epKey, c.ClientIP()) {
				response.Fail(c, http.StatusTooManyRequests, int(errcode.CodeGwRateLimit), "too many requests")
				c.Abort()
				return
			}
		}

		// JWT authentication — skip for public methods.
		if p.jwtParser != nil && !p.publicMethod[method] {
			tokenStr, ok := bearerToken(string(c.GetHeader("Authorization")))
			if !ok {
				response.Unauthorized(c, "missing token")
				c.Abort()
				return
			}
			claims, err := p.jwtParser.Parse(tokenStr)
			if err != nil {
				response.Unauthorized(c, "invalid token")
				c.Abort()
				return
			}

			// P1-1: Verify token is still active in Redis SET (multi-device support).
			// Uses SISMEMBER to check if the token belongs to the user's active token set.
			// SISMEMBER returns (false, nil) for non-existent keys — no redis.Nil ambiguity.
			if p.rdb != nil {
				tokenKey := fmt.Sprintf("%s%d", tokenPrefix, claims.UserID)
				isMember, err := p.rdb.SIsMember(ctx, tokenKey, tokenStr)
				if err != nil {
					// Redis connection errors fail closed so revoked tokens are not accepted.
					logger.Warnf("redis sismember failed (fail-closed): user_id=%d err=%v", claims.UserID, err)
					response.Fail(c, http.StatusServiceUnavailable, int(errcode.CodeGwSrvUnavailable), "service temporarily unavailable")
					c.Abort()
					return
				} else if !isMember {
					// Token not in the user's active set → revoked or expired.
					response.Unauthorized(c, "token revoked")
					c.Abort()
					return
				}
			}

			// Forward identity to downstream service via Kitex metainfo.
			ctx = metainfo.WithPersistentValue(ctx, "token", tokenStr)
			ctx = metainfo.WithPersistentValue(ctx, "user_id", fmt.Sprintf("%d", claims.UserID))

			// Set user_id in Hertz request context for downstream middleware (e.g. per-user rate limiting)
			c.Set("user_id", fmt.Sprintf("%d", claims.UserID))
		}

		// Pass the raw JSON request body to the generic client.
		// WriteJSON treats nil as "{}", but an empty string "" will fail to
		// unmarshal, so default to "{}" for GET / empty-body requests.
		body := string(c.Request.Body())
		if body == "" {
			body = "{}"
		}

		resp, err := p.Call(ctx, service, method, body)
		if err != nil {
			// Circuit breaker is open — fast reject with 503.
			if errors.Is(err, gobreaker.ErrOpenState) {
				response.ServiceUnavailable(c)
				return
			}
			// gobreaker wraps the inner error — unwrap before checking.
			unwrapped := errors.Unwrap(err)
			if unwrapped != nil {
				if biz, ok := errcode.ParseBizError(unwrapped); ok {
					response.BizErr(c, biz)
					return
				}
			}
			// Try to parse downstream BizError for proper status code.
			if biz, ok := errcode.ParseBizError(err); ok {
				response.BizErr(c, biz)
				return
			}
			// Internal / network error — never expose to client.
			logger.Errorf("rpc call failed: service=%s method=%s err=%v", service, method, err)
			response.Fail(c, http.StatusInternalServerError, int(errcode.CodeGwServer), "internal server error")
			return
		}

		// P0 fix: correctly handle both wrapped and raw protobuf responses.
		// Wrapped response:  {"code":0,"message":"","data":{...}}  (has "code" field)
		// Raw protobuf response: {"id":1,"token":"..."}                 (no "code" field)
		if s, ok := resp.(string); ok && s != "" {
			// R4-P3-06: Single-pass detection — use json.RawMessage to check for "code"
			// field without a full second unmarshal.
			var raw struct {
				Code    json.RawMessage `json:"code"`
				Message json.RawMessage `json:"message"`
				Data    json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal([]byte(s), &raw); err != nil {
				// Unmarshal failed — return raw string as data
				response.OK(c, s)
				return
			}

			if raw.Code != nil {
				// Wrapped response: parse code/message/data
				var downstream struct {
					Code    int         `json:"code"`
					Message string      `json:"message"`
					Data    interface{} `json:"data"`
				}
				if err := json.Unmarshal([]byte(s), &downstream); err != nil {
					logger.Warnf("proxy: failed to unmarshal wrapped response: %v", err)
					var data interface{}
					json.Unmarshal([]byte(s), &data)
					response.OK(c, data)
					return
				}

				if downstream.Code != 0 {
					// Error response — pass through with correct HTTP status
					statusCode := http.StatusOK
					switch downstream.Code {
					case int(errcode.CodeUserUnauthorized):
						statusCode = http.StatusUnauthorized
					case int(errcode.CodeUserForbidden):
						statusCode = http.StatusForbidden
					default:
						if downstream.Code < 50000 {
							statusCode = http.StatusBadRequest
						} else {
							statusCode = http.StatusInternalServerError
						}
					}
					c.JSON(statusCode, response.R{
						Code:    downstream.Code,
						Message: downstream.Message,
						Data:    downstream.Data,
					})
					return
				}
				// Code == 0 (success wrapper): extract data field
				response.OK(c, downstream.Data)
				return
			}

			// Raw protobuf response (LoginResp, RegisterUserResp, etc.)
			// Wrap the entire response body as "data"
			var data interface{}
			if err := json.Unmarshal([]byte(s), &data); err != nil {
				data = s
			}
			response.OK(c, data)
			return
		}

		response.OK(c, resp)
	}
}

// =========================
// helpers
// =========================

// methodNameRegex validates that a method name contains only alphanumeric characters
// and starts with a letter. Prevents path traversal or injection via URL params.
var methodNameRegex = regexp.MustCompile(`^[A-Za-z][a-zA-Z0-9]*$`)

// toMethodName ensures the first letter of a method name is upper-case.
// Returns empty string if the method fails format validation.
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

// buildProtoPath returns {protoBase}/{service}/api/{service}.proto.
func buildProtoPath(protoBase, service string) string {
	return filepath.Join(protoBase, service, "api", service+".proto")
}

// extractServiceName parses the service name from an Etcd key.
// Key format: {prefix}{serviceName}/{addr}
func extractServiceName(key, prefix string) string {
	trimmed := strings.TrimPrefix(key, prefix)
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	return parts[0]
}

// retryRegister attempts to register a service up to maxRetries times with backoff.
// Returns the last error if all attempts fail.
func (p *Proxy) retryRegister(serviceName, protoPath string, resolver discovery.Resolver, maxRetries int) error {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if err := p.registerService(serviceName, protoPath, resolver); err != nil {
			lastErr = err
			if i < maxRetries-1 {
				time.Sleep(time.Duration(i+1) * time.Second)
			}
			continue
		}
		return nil
	}
	return lastErr
}

// newEtcdClient creates a minimal etcd v3 client.
func newEtcdClient(addrs []string) (*clientv3.Client, error) {
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   addrs,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to etcd %v: %w", addrs, err)
	}
	return cli, nil
}

// GetCircuitBreakerStatus returns a snapshot of all circuit breaker states.
// Useful for a debug/service-status endpoint.
func (p *Proxy) GetCircuitBreakerStatus() map[string]string {
	result := make(map[string]string)
	p.perServiceCB.Range(func(key, value interface{}) bool {
		cb := value.(*gobreaker.CircuitBreaker)
		result[key.(string)] = cb.State().String()
		return true
	})
	return result
}

// RemoveCircuitBreaker removes the circuit breaker for a service.
// Call this when a service is deregistered to prevent unbounded memory growth.
func (p *Proxy) RemoveCircuitBreaker(service string) {
	p.perServiceCB.Delete(service)
}
