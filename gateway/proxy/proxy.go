package proxy

import (
	"context"
	"encoding/json"
	"strings"

	dproto "github.com/cloudwego/dynamicgo/proto"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/client/genericclient"
	"github.com/cloudwego/kitex/pkg/generic"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/jwt"
	"shopmicro/pkg/response"
)

// Proxy 通用 gRPC 代理，基于 Kitex generic client 自动转发请求
type Proxy struct {
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
	p.clients[destService] = cli
	return nil
}

// Handler 返回通用的 Hertz 路由处理器
// 路由格式: /api/v1/:service/:method
//
//	URL method 使用 snake_case，自动转为 proto PascalCase RPC 方法名:
//	  /api/v1/user/register_user → RegisterUser
//	  /api/v1/user/login         → Login
//	  /api/v1/user/login_out     → LoginOut
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
				response.JSON(c, errcode.ErrToken.WithMsg(err.Error()), nil)
				c.Abort()
				return
			}
			ctx = context.WithValue(ctx, "user_id", claims.UserID)
			ctx = context.WithValue(ctx, "phone", claims.Phone)
		}

		cli, ok := p.clients[service]
		if !ok {
			response.JSON(c, errcode.ErrParam.WithMsg("service not found: "+service), nil)
			c.Abort()
			return
		}

		body := p.buildRequestBody(c, tokenStr)

		resp, err := cli.GenericCall(ctx, method, string(body))
		if err != nil {
			response.JSON(c, errcode.ErrServer.WithMsg(err.Error()), nil)
			return
		}

		// GenericCall 返回 JSON 字符串，解析为对象再返回
		var data interface{}
		if respStr, ok := resp.(string); ok {
			_ = json.Unmarshal([]byte(respStr), &data)
		} else {
			data = resp
		}
		response.JSON(c, errcode.Success, data)
	}
}

// extractToken 从 Authorization header 或 Token header 提取 token
func (p *Proxy) extractToken(c *app.RequestContext) string {
	auth := string(c.GetHeader("Authorization"))
	if auth != "" {
		token := strings.TrimPrefix(auth, "Bearer ")
		if token != auth {
			return token
		}
		token = strings.TrimPrefix(auth, "bearer ")
		if token != auth {
			return token
		}
		return auth
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
			params[toCamelCase(string(key))] = string(value)
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
		} else {
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
