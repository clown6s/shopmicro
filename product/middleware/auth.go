package middleware

import (
	"context"
	"fmt"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/rpcinfo"

	"shopmicro/pkg/jwt"
	"shopmicro/pkg/logger"
)

// UnaryJWTInterceptor 创建 Kitex gRPC 拦截器，校验 JWT token。
// token 从 Kitex metainfo（key: "token"）中提取，由 gateway 通过
// metainfo.WithPersistentValue 转发。
// 解析成功后将 Claims 注入 context（key: contextKeyClaims）。
// 如果 jwtParser 为 nil，则跳过校验（dev 模式下可禁用鉴权）。
func UnaryJWTInterceptor(jwtParser *jwt.Parser) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, req, resp interface{}) (err error) {
			if jwtParser == nil {
				return next(ctx, req, resp)
			}

			token, ok := metainfo.GetPersistentValue(ctx, "token")
			if !ok || token == "" {
				return fmt.Errorf("authentication required: missing token in metainfo")
			}

			claims, err := jwtParser.Parse(token)
			if err != nil {
				return fmt.Errorf("authentication failed: %w", err)
			}

			ctx = context.WithValue(ctx, contextKeyClaims{}, claims)
			logger.Debugf("jwt auth ok: user_id=%d", claims.UserID)
			return next(ctx, req, resp)
		}
	}
}

type contextKeyClaims struct{}

// ClaimsFromContext 从 context 中提取 JWT Claims（由 UnaryJWTInterceptor 注入）。
func ClaimsFromContext(ctx context.Context) *jwt.Claims {
	if v, ok := ctx.Value(contextKeyClaims{}).(*jwt.Claims); ok {
		return v
	}
	return nil
}

// UnaryServiceAuthInterceptor 创建基于服务间 API Key 的拦截器。
// token 从 Kitex metainfo（key: "x-internal-token"）中提取。
func UnaryServiceAuthInterceptor(allowedToken string) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, req, resp interface{}) (err error) {
			if allowedToken == "" {
				return next(ctx, req, resp)
			}

			token, ok := metainfo.GetPersistentValue(ctx, "x-internal-token")
			if !ok || token == "" {
				return fmt.Errorf("service authentication required: missing x-internal-token in metainfo")
			}

			if token != allowedToken {
				logger.Warnf("service auth failed: invalid internal token, caller=%s",
					rpcinfo.GetRPCInfo(ctx).From().ServiceName())
				return fmt.Errorf("service authentication failed")
			}

			return next(ctx, req, resp)
		}
	}
}
