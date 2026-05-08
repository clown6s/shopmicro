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

// Proxy 通用 gRPC 代理，基于 Kitex generic client 自动转发请求
type Proxy struct {
	mu            sync.RWMutex
	clients       map[string]genericclient.Client
	publicMethods map[string]bool
	jwtParser     *jwt.Parser
}

// Option 代理配置选项
type Option func(*Proxy)

// WithPublicMethods 设置不需要认证的 RPC 方法（PascalCase，如 RegisterUser、Login）
func WithPublicMethods(methods ...string) Option {
	return func(p *Proxy) {
		for _, m := range methods {
			p.publicMethods[m] = true
		}
	}
}

// WithJWT 启用 JWT 认证
func WithJWT(secret string) Option {
	return func(p *Proxy) {
		p.jwtParser = jwt.NewParser(secret)
	}
}

// New 创建代理实例
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

// RegisterService 注册下游 gRPC 服务
// destService: 服务发现名（etcd 注册名）
// protoPath: proto 文件路径，用于加载 JSON 描述符
func (p *Proxy) RegisterService(destService, protoPath string, opts ...client.Option) error {
	provider, err := generic.NewPbFileProviderWithDynamicGo(protoPath, context.Background(), dproto.Options{})
	if err != nil {
		return err
	}

	g, err := generic.JSONPbGeneric(provider)
	if err != nil {
		return err
	}

	cli, err := genericclient.NewClient(destService, g, opts...)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.clients[destService] = cli
	p.mu.Unlock()
	return nil
}

// DiscoverAndRegister 自动从 Etcd 发现已注册的 Kitex 服务并创建 generic client
// 新增服务零配置：只需启动下游服务注册到 Etcd，网关自动感知
//
// resolver: 服务发现 Resolver（已连接 Etcd）
// etcdAddrs: Etcd 地址列表，用于查询已注册的服务名
// protoBase: proto 文件基础路径（相对于网关工作目录），默认 "../"
//
//	proto 文件约定路径: {protoBase}{serviceName}/api/{serviceName}.proto
func (p *Proxy) DiscoverAndRegister(resolver discovery.Resolver, etcdAddrs []string, protoBase string) error {
	if protoBase == "" {
		protoBase = "../"
	}

	cli, err := etcdv3.New(etcdv3.Config{
		Endpoints:   etcdAddrs,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("create etcd client for discovery: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := cli.Get(ctx, kitexRegistryPrefix, etcdv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("discover services from etcd: %w", err)
	}

	services := make(map[string]bool)
	for _, kv := range resp.Kvs {
		rest := strings.TrimPrefix(string(kv.Key), kitexRegistryPrefix)
		if idx := strings.Index(rest, "/"); idx > 0 {
			services[rest[:idx]] = true
		} else if rest != "" {
			services[rest] = true
		}
	}

	if len(services) == 0 {
		logger.Warnf("no services discovered in etcd under %s", kitexRegistryPrefix)
		return nil
	}

	for name := range services {
		protoPath := protoBase + name + "/api/" + name + ".proto"
		opts := []client.Option{client.WithResolver(resolver)}
		if err := p.RegisterService(name, protoPath, opts...); err != nil {
			logger.Warnf("auto-register service %s failed (proto: %s): %v", name, protoPath, err)
			continue
		}
		logger.Infof("auto-discovered service: %s (proto: %s)", name, protoPath)
	}

	return nil
}

// Handler 返回通用的 Hertz 路由处理器
// 路由格式: /api/v1/:service/:method
//
//	URL method 使用 snake_case，自动转为 proto PascalCase RPC 方法名:
//	  /api/v1/user/register_user → RegisterUser
//	  /api/v1/user/login         → Login
//	  /api/v1/user/logout        → Logout
//
//	GET 请求的 query params 会自动转为 JSON body（snake_case → camelCase）
//	Authorization header 中的 token 会自动注入到 body 中（供 LoginOut 等接口使用）
func (p *Proxy) Handler() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		service := c.Param("service")
		method := toRPCMethod(c.Param("method"))

		// JWT 认证
		tokenStr := p.extractToken(c)
		if !p.publicMethods[method] && p.jwtParser != nil {
			if tokenStr == "" {
				response.JSON(c, errcode.ErrToken, nil)
				c.Abort()
				return
			}
			claims, err := p.jwtParser.Parse(tokenStr)
			if err != nil {
				// 不泄露具体错误细节
				response.JSON(c, errcode.ErrToken, nil)
				c.Abort()
				return
			}
			ctx = context.WithValue(ctx, keyUserID, claims.UserID)
			ctx = context.WithValue(ctx, keyPhone, claims.Phone)
		}

		p.mu.RLock()
		cli, ok := p.clients[service]
		p.mu.RUnlock()
		if !ok {
			response.JSON(c, errcode.ErrParam.WithMsg("service not found"), nil)
			c.Abort()
			return
		}

		body := p.buildRequestBody(c, tokenStr)

		// 将 token 注入 Kitex persistent metadata，供下游服务拦截器校验
		if tokenStr != "" {
			ctx = metainfo.WithPersistentValue(ctx, "token", tokenStr)
		}

		resp, err := cli.GenericCall(ctx, method, string(body))
		if err != nil {
			// 从 gRPC remote error 中解析业务错误
			if bizErr, ok := errcode.ParseBizError(err); ok {
				response.JSON(c, errcode.FromBizError(bizErr), nil)
				return
			}
			logger.Errorf("gRPC call failed: service=%s method=%s err=%v", service, method, err)
			response.JSON(c, errcode.ErrServer.WithMsg("service unavailable"), nil)
			return
		}

		// GenericCall 返回 JSON 字符串，解析后直接返回
		var result map[string]interface{}
		if respStr, ok := resp.(string); ok {
			if len(respStr) > 0 {
				if err := json.Unmarshal([]byte(respStr), &result); err != nil {
					logger.Errorf("unmarshal gRPC response failed: %v", err)
					response.JSON(c, errcode.ErrResponse, nil)
					return
				}
			}
		}
		response.JSON(c, errcode.Success, result)
	}
}

// extractToken 从 Authorization header 或 Token header 提取 token
func (p *Proxy) extractToken(c *app.RequestContext) string {
	auth := string(c.GetHeader("Authorization"))
	if strings.HasPrefix(auth, "Bearer ") || strings.HasPrefix(auth, "bearer ") {
		return strings.TrimPrefix(strings.TrimPrefix(auth, "Bearer "), "bearer ")
	}
	return string(c.GetHeader("Token"))
}

// buildRequestBody 构建转发给 gRPC 的请求体
// GET 请求: query params → JSON（snake_case 转 camelCase）
// POST/PUT/DELETE: 读取 body
// 如果有 token，注入到 body 中
func (p *Proxy) buildRequestBody(c *app.RequestContext, tokenStr string) json.RawMessage {
	var body json.RawMessage

	if string(c.Method()) == "GET" {
		params := make(map[string]interface{})
		c.QueryArgs().VisitAll(func(key, value []byte) {
			v := string(value)
			// 数字字符串自动转为 int/float，匹配 proto int32/int64 类型
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				params[toCamelCase(string(key))] = n
			} else if f, err := strconv.ParseFloat(v, 64); err == nil {
				params[toCamelCase(string(key))] = f
			} else {
				params[toCamelCase(string(key))] = v
			}
		})
		body, _ = json.Marshal(params)
	} else {
		body, _ = c.Body()
	}

	// 将 token 注入 body，供下游 LoginOut 等接口使用
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

// toRPCMethod snake_case → PascalCase: login_out → LoginOut
func toRPCMethod(s string) string {
	parts := strings.Split(s, "_")
	for i := range parts {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// toCamelCase snake_case → camelCase: page_size → pageSize
func toCamelCase(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}
