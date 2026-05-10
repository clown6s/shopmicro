package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	// P1-12: Allowed service names (nil = allow all discovered)
	allowedServices map[string]bool
	// P1-12: Allowed methods per service (nil = allow all methods)
	allowedMethods map[string]map[string]bool
}

// Option configures Proxy at construction time.
type Option func(*Proxy)

// WithJWT sets the JWT secret used to validate inbound tokens.
func WithJWT(secret string) Option {
	return func(p *Proxy) {
		p.jwtParser = pkgjwt.NewParser(secret)
	}
}

// WithRedis sets the Redis client for token validation.
func WithRedis(rdb *redis.Client) Option {
	return func(p *Proxy) {
		p.rdb = rdb
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

// =========================
// call RPC
// =========================

// Call performs a generic RPC to the named service and method.
// req must be a JSON-encoded string (for JSONPbGeneric).
// method must be the CamelCase protobuf RPC name (e.g. "ListUser", NOT
// a gRPC full path like "api.user.v1.UserService/ListUser").
//
// P2-07: Manual retry (max 2 retries, 3 attempts total) only on timeout/network
// errors — NOT on business errors (InvalidArgument, NotFound, etc.).
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

	const maxRetries = 2
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Brief backoff before retry
			time.Sleep(time.Duration(attempt*50) * time.Millisecond)
		}

		// JSONPbGeneric: WriteJSON.Write calls LookupMethodByName(method)
		// which expects CamelCase method name, NOT gRPC full path.
		// Use method directly; the generic client already knows the service.
		resp, err := cli.Client.GenericCall(ctx, method, req)
		if err == nil {
			return resp, nil
		}

		lastErr = err

		// Only retry on timeout/network errors; business errors are final
		if !shouldRetryGRPC(err) {
			return nil, err
		}

		logger.Warnf("rpc call failed (attempt %d/%d), retrying: service=%s method=%s err=%v",
			attempt+1, maxRetries+1, service, method, err)
	}

	return nil, lastErr
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
			time.Sleep(backoff)
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

			if err := p.registerService(svcName, protoPath, resolver); err != nil {
				logger.Warnf("WatchServices: failed to register %s: %v", svcName, err)
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
					response.Fail(c, http.StatusServiceUnavailable, 503, "service temporarily unavailable")
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
			// Try to parse downstream BizError for proper status code.
			if biz, ok := errcode.ParseBizError(err); ok {
				response.BizErr(c, biz)
				return
			}
			// Internal / network error — never expose to client.
			logger.Errorf("rpc call failed: service=%s method=%s err=%v", service, method, err)
			response.Fail(c, http.StatusInternalServerError, 500, "internal server error")
			return
		}

		// GenericCall returns the JSON string from downstream.
		// Parse it back to an object so the wrapper marshal produces
		// {"data":{...}} instead of {"data":"{...}"}.
		var data interface{}
		if s, ok := resp.(string); ok && s != "" {
			if err := json.Unmarshal([]byte(s), &data); err == nil {
				// successfully parsed
			} else {
				// not valid JSON, keep original
				data = resp
			}
		} else {
			data = resp
		}

		response.OK(c, data)
	}
}

// =========================
// helpers
// =========================

// toMethodName ensures the first letter of a method name is upper-case.
func toMethodName(method string) string {
	if method == "" {
		return method
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

// shouldRetryGRPC decides whether a gRPC call error is retryable.
// Only timeout and network errors are retryable; business errors (InvalidArgument,
// NotFound, etc.) are NOT retryable because retrying them is useless and may cause duplicates.
func shouldRetryGRPC(err error) bool {
	if err == nil {
		return false
	}

	// Timeout errors — always retryable
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	errStr := strings.ToLower(err.Error())

	// Network/connection errors — retryable
	retryableKeywords := []string{
		"timeout",
		"deadline exceeded",
		"connection refused",
		"connection reset",
		"no such host",
		"i/o timeout",
		"temporary failure",
		"unavailable",
	}
	for _, kw := range retryableKeywords {
		if strings.Contains(errStr, kw) {
			return true
		}
	}

	// gRPC status codes that are NOT retryable (business errors)
	nonRetryableKeywords := []string{
		"invalid argument",
		"not found",
		"already exists",
		"permission denied",
		"unauthenticated",
		"unimplemented",
		"out of range",
		"failed precondition",
	}
	for _, kw := range nonRetryableKeywords {
		if strings.Contains(errStr, kw) {
			return false
		}
	}

	// For unknown errors: don't retry (safe default)
	return false
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
