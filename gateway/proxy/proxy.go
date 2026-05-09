package proxy

import (
	"context"
	"encoding/json"
	"fmt"
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

	"go.etcd.io/etcd/api/v3/mvccpb"
	etcdv3 "go.etcd.io/etcd/client/v3"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/jwt"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/response"
)

// contextKey 类型化 context key，避免跨包冲突
type contextKey string

const (
	keyUserID           contextKey = "user_id"
	keyPhone            contextKey = "phone"
	kitexRegistryPrefix            = "kitex/registry-etcd/"
)

// Proxy 通用 gRPC 代理
type Proxy struct {
	mu            sync.RWMutex
	clients       map[string]genericclient.Client
	publicMethods map[string]bool
	jwtParser     *jwt.Parser

	// etcd client
	etcdCli *etcdv3.Client
}

// Option 配置项
type Option func(*Proxy)

// WithPublicMethods 设置免认证方法
func WithPublicMethods(methods ...string) Option {
	return func(p *Proxy) {
		for _, m := range methods {
			p.publicMethods[m] = true
		}
	}
}

// WithJWT 启用 JWT
func WithJWT(secret string) Option {
	return func(p *Proxy) {
		p.jwtParser = jwt.NewParser(secret)
	}
}

// New 创建 Proxy
func New(opts ...Option) *Proxy {
	p := &Proxy{
		clients:       make(map[string]genericclient.Client),
		publicMethods: make(map[string]bool),
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// RegisterService 注册服务
func (p *Proxy) RegisterService(
	destService,
	protoPath string,
	opts ...client.Option,
) error {

	provider, err := generic.NewPbFileProviderWithDynamicGo(
		protoPath,
		context.Background(),
		dproto.Options{},
	)
	if err != nil {
		return err
	}

	g, err := generic.JSONPbGeneric(provider)
	if err != nil {
		return err
	}

	cli, err := genericclient.NewClient(
		destService,
		g,
		opts...,
	)
	if err != nil {
		return err
	}

	p.mu.Lock()
	p.clients[destService] = cli
	p.mu.Unlock()

	return nil
}

// DiscoverAndRegister
//
// 启动时扫描 etcd 已有服务
func (p *Proxy) DiscoverAndRegister(
	resolver discovery.Resolver,
	etcdAddrs []string,
	protoBase string,
) error {

	if protoBase == "" {
		protoBase = "../"
	}

	cli, err := etcdv3.New(etcdv3.Config{
		Endpoints:   etcdAddrs,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return fmt.Errorf(
			"create etcd client for discovery: %w",
			err,
		)
	}

	// 保存 etcd client 给 watch 使用
	p.etcdCli = cli

	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	resp, err := cli.Get(
		ctx,
		kitexRegistryPrefix,
		etcdv3.WithPrefix(),
	)
	if err != nil {
		return fmt.Errorf(
			"discover services from etcd: %w",
			err,
		)
	}

	services := make(map[string]bool)

	for _, kv := range resp.Kvs {

		key := string(kv.Key)

		rest := strings.TrimPrefix(
			key,
			kitexRegistryPrefix,
		)

		idx := strings.Index(rest, "/")
		if idx <= 0 {
			continue
		}

		serviceName := rest[:idx]

		services[serviceName] = true
	}

	if len(services) == 0 {

		logger.Infof(
			"no services discovered yet under %s",
			kitexRegistryPrefix,
		)

		return nil
	}

	for serviceName := range services {

		// 已存在
		p.mu.RLock()
		_, exists := p.clients[serviceName]
		p.mu.RUnlock()

		if exists {
			continue
		}

		protoPath :=
			protoBase +
				serviceName +
				"/api/" +
				serviceName +
				".proto"

		opts := []client.Option{
			client.WithResolver(resolver),

			// RPC 超时
			client.WithRPCTimeout(3 * time.Second),
		}

		if err := p.RegisterService(
			serviceName,
			protoPath,
			opts...,
		); err != nil {

			logger.Warnf(
				"auto-register service %s failed (proto: %s): %v",
				serviceName,
				protoPath,
				err,
			)

			continue
		}

		logger.Infof(
			"auto-discovered service: %s",
			serviceName,
		)
	}

	return nil
}

// WatchServices
//
// 动态监听 etcd 服务变化
func (p *Proxy) WatchServices(
	resolver discovery.Resolver,
	protoBase string,
) {

	if protoBase == "" {
		protoBase = "../"
	}

	if p.etcdCli == nil {
		logger.Errorf("etcd client is nil")
		return
	}

	watchChan := p.etcdCli.Watch(
		context.Background(),
		kitexRegistryPrefix,
		etcdv3.WithPrefix(),
	)

	go func() {

		logger.Infof(
			"watching etcd services under %s",
			kitexRegistryPrefix,
		)

		for resp := range watchChan {

			for _, event := range resp.Events {

				switch event.Type {

				// 服务上线
				case mvccpb.PUT:

					key := string(event.Kv.Key)

					rest := strings.TrimPrefix(
						key,
						kitexRegistryPrefix,
					)

					idx := strings.Index(rest, "/")
					if idx <= 0 {
						continue
					}

					serviceName := rest[:idx]

					// 已存在
					p.mu.RLock()
					_, exists := p.clients[serviceName]
					p.mu.RUnlock()

					if exists {
						continue
					}

					protoPath :=
						protoBase +
							serviceName +
							"/api/" +
							serviceName +
							".proto"

					opts := []client.Option{
						client.WithResolver(resolver),
						client.WithRPCTimeout(3 * time.Second),
					}

					if err := p.RegisterService(
						serviceName,
						protoPath,
						opts...,
					); err != nil {

						logger.Warnf(
							"watch auto-register failed: %s err=%v",
							serviceName,
							err,
						)

						continue
					}

					logger.Infof(
						"new service discovered: %s",
						serviceName,
					)

				// 服务下线
				case mvccpb.DELETE:

					key := string(event.Kv.Key)

					logger.Infof(
						"service instance offline: %s",
						key,
					)
				}
			}
		}
	}()
}

// Handler
//
// 通用路由:
// /api/v1/:service/:method
func (p *Proxy) Handler() app.HandlerFunc {

	return func(ctx context.Context, c *app.RequestContext) {

		service := c.Param("service")
		method := toRPCMethod(c.Param("method"))

		// JWT
		tokenStr := p.extractToken(c)

		if !p.publicMethods[method] &&
			p.jwtParser != nil {

			if tokenStr == "" {

				response.JSON(
					c,
					errcode.ErrToken,
					nil,
				)

				c.Abort()
				return
			}

			claims, err := p.jwtParser.Parse(tokenStr)
			if err != nil {

				response.JSON(
					c,
					errcode.ErrToken,
					nil,
				)

				c.Abort()
				return
			}

			ctx = context.WithValue(
				ctx,
				keyUserID,
				claims.UserID,
			)

			ctx = context.WithValue(
				ctx,
				keyPhone,
				claims.Phone,
			)
		}

		// 查找 client
		p.mu.RLock()
		cli, ok := p.clients[service]
		p.mu.RUnlock()

		if !ok {

			logger.Warnf(
				"service not found: %s",
				service,
			)

			response.JSON(
				c,
				errcode.ErrParam.WithMsg("service not found"),
				nil,
			)

			c.Abort()
			return
		}

		body := p.buildRequestBody(c, tokenStr)

		// token 透传
		if tokenStr != "" {
			ctx = metainfo.WithPersistentValue(
				ctx,
				"token",
				tokenStr,
			)
		}

		// trace id
		traceID := string(c.GetHeader("X-Trace-Id"))
		if traceID != "" {
			ctx = metainfo.WithPersistentValue(
				ctx,
				"trace_id",
				traceID,
			)
		}

		resp, err := cli.GenericCall(
			ctx,
			method,
			string(body),
		)

		if err != nil {

			if bizErr, ok := errcode.ParseBizError(err); ok {

				response.JSON(
					c,
					errcode.FromBizError(bizErr),
					nil,
				)

				return
			}

			logger.Errorf(
				"gRPC call failed: service=%s method=%s err=%v",
				service,
				method,
				err,
			)

			response.JSON(
				c,
				errcode.ErrServer.WithMsg("service unavailable"),
				nil,
			)

			return
		}

		// 解析 JSON
		var result map[string]interface{}

		if respStr, ok := resp.(string); ok {

			if len(respStr) > 0 {

				if err := json.Unmarshal(
					[]byte(respStr),
					&result,
				); err != nil {

					logger.Errorf(
						"unmarshal gRPC response failed: %v",
						err,
					)

					response.JSON(
						c,
						errcode.ErrResponse,
						nil,
					)

					return
				}
			}
		}

		response.JSON(
			c,
			errcode.Success,
			result,
		)
	}
}

// extractToken
func (p *Proxy) extractToken(
	c *app.RequestContext,
) string {

	auth := string(c.GetHeader("Authorization"))

	if strings.HasPrefix(auth, "Bearer ") ||
		strings.HasPrefix(auth, "bearer ") {

		return strings.TrimPrefix(
			strings.TrimPrefix(auth, "Bearer "),
			"bearer ",
		)
	}

	return string(c.GetHeader("Token"))
}

// buildRequestBody
func (p *Proxy) buildRequestBody(
	c *app.RequestContext,
	tokenStr string,
) json.RawMessage {

	var body json.RawMessage

	// GET -> query params
	if string(c.Method()) == "GET" {

		params := make(map[string]interface{})

		c.QueryArgs().VisitAll(
			func(key, value []byte) {

				v := string(value)

				if n, err := strconv.ParseInt(
					v,
					10,
					64,
				); err == nil {

					params[toCamelCase(string(key))] = n

				} else if f, err := strconv.ParseFloat(
					v,
					64,
				); err == nil {

					params[toCamelCase(string(key))] = f

				} else {

					params[toCamelCase(string(key))] = v
				}
			},
		)

		body, _ = json.Marshal(params)

	} else {

		body, _ = c.Body()
	}

	// token 注入 body
	if tokenStr != "" {

		var bodyMap map[string]interface{}

		if len(body) > 0 {
			_ = json.Unmarshal(body, &bodyMap)
		}

		if bodyMap == nil {
			bodyMap = make(map[string]interface{})
		}

		bodyMap["token"] = tokenStr

		body, _ = json.Marshal(bodyMap)
	}

	return body
}

// toRPCMethod
//
// snake_case -> PascalCase
func toRPCMethod(s string) string {

	parts := strings.Split(s, "_")

	for i := range parts {

		if len(parts[i]) > 0 {

			parts[i] =
				strings.ToUpper(parts[i][:1]) +
					parts[i][1:]
		}
	}

	return strings.Join(parts, "")
}

// toCamelCase
//
// snake_case -> camelCase
func toCamelCase(s string) string {

	parts := strings.Split(s, "_")

	for i := 1; i < len(parts); i++ {

		if len(parts[i]) > 0 {

			parts[i] =
				strings.ToUpper(parts[i][:1]) +
					parts[i][1:]
		}
	}

	return strings.Join(parts, "")
}
