package middleware

import (
	"context"
	"fmt"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/kitex/pkg/endpoint"
	"github.com/cloudwego/kitex/pkg/rpcinfo"

	"shopmicro/pkg/logger"
)

// UnaryServiceAuthInterceptor 创建基于服务间 API Key 的拦截器。
// 用于微服务间通信鉴权，token 从 Kitex metainfo（key: "x-internal-token"）中提取。
// 如果 allowedToken 为空，则跳过校验。
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
				var caller string
				if ri := rpcinfo.GetRPCInfo(ctx); ri != nil {
					caller = ri.From().ServiceName()
				}
				logger.Warnf("service auth failed: invalid internal token, caller=%s", caller)
				return fmt.Errorf("service authentication failed")
			}

			return next(ctx, req, resp)
		}
	}
}
