package middleware

import (
	"context"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/klog"
	"github.com/cloudwego/kitex/pkg/rpcinfo"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/jwt"
)

var publicRPCs = map[string]bool{
	"RegisterUser": true,
	"Login":        true,
}

// AuthMiddleware gRPC 认证中间件，防止绕过 Gateway 直连
// 白名单外的 RPC 必须在 metadata 中携带有效 token
func AuthMiddleware(parser *jwt.Parser) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, req, resp interface{}) (err error) {
			ri := rpcinfo.GetRPCInfo(ctx)
			method := ri.To().Method()

			if publicRPCs[method] {
				return next(ctx, req, resp)
			}

			token, ok := metainfo.GetPersistentValue(ctx, "token")
			if !ok || token == "" {
				klog.CtxErrorf(ctx, "auth middleware: no token in metadata")
				return errcode.NewBizError(errcode.CodeUserTokenRequired, "unauthorized: missing token")
			}

			if _, err := parser.Parse(token); err != nil {
				// 不泄露具体错误细节给下游
				return errcode.NewBizError(errcode.CodeUserTokenInvalid, "unauthorized: invalid or expired token")
			}

			return next(ctx, req, resp)
		}
	}
}
