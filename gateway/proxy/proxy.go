package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
		clients:      make(map[string]*ServiceClient),
		publicMethod: make(map[string]bool),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
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

	// JSONPbGeneric: WriteJSON.Write calls LookupMethodByName(method)
	// which expects CamelCase method name, NOT gRPC full path.
	// Use method directly; the generic client already knows the service.
	resp, err := cli.Client.GenericCall(ctx, method, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
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
		if err := p.registerService(svcName, protoPath, resolver); err != nil {
			logger.Warnf("failed to register service %s: %v", svcName, err)
		}
	}
	return nil
}

// WatchServices watches Etcd for newly registered services and registers them
// automatically. It runs in a background goroutine and returns immediately.
func (p *Proxy) WatchServices(
	resolver discovery.Resolver,
	etcdAddrs []string,
	protoBase string,
) {
	go func() {
		etcdClient, err := newEtcdClient(etcdAddrs)
		if err != nil {
			logger.Errorf("WatchServices: connect to etcd: %v", err)
			return
		}
		defer etcdClient.Close()

		prefix := etcdRegistryPrefix + "/"
		watchCh := etcdClient.Watch(context.Background(), prefix, clientv3.WithPrefix())

		for wresp := range watchCh {
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
				if err := p.registerService(svcName, protoPath, resolver); err != nil {
					logger.Warnf("WatchServices: failed to register %s: %v", svcName, err)
				}
			}
		}
	}()
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

		// JWT authentication — skip for public methods.
		if p.jwtParser != nil && !p.publicMethod[method] {
			tokenStr := strings.TrimPrefix(
				string(c.GetHeader("Authorization")),
				"Bearer ",
			)
			if tokenStr == "" {
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

			// Verify token is still active in Redis (not logged out / superseded).
			if p.rdb != nil {
				tokenKey := fmt.Sprintf("%s%d", tokenPrefix, claims.UserID)
				stored, _ := p.rdb.Get(ctx, tokenKey)
				if stored != tokenStr {
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
