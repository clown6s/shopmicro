package proxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/kitex/client/callopt"
	"github.com/cloudwego/kitex/pkg/discovery"

	"shopmicro/pkg/logger"
)

// Call 对指定的服务和方法执行泛化 RPC 调用。
// req 必须是 JSON 编码的字符串（用于 JSONPbGeneric）。
// method 必须是 CamelCase 的 protobuf RPC 名称。
//
// 对可重试的瞬态错误最多重试 2 次，使用指数退避。
// 如果配置了熔断器，在熔断打开时快速失败返回 503。
func (p *Proxy) Call(
	ctx context.Context,
	service string,
	method string,
	req interface{},
	callOpts ...callopt.Option,
) (interface{}, error) {
	cli, ok := p.getClient(service)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotFound, service)
	}

	rpcFn := func() (interface{}, error) {
		const maxRetries = 2
		var lastErr error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			resp, err := cli.Client.GenericCall(ctx, method, req, callOpts...)
			if err == nil {
				return resp, nil
			}
			lastErr = err
			// P2-01: 仅对明确的瞬态错误重试
			errMsg := err.Error()
			isTransient := strings.Contains(errMsg, "connection reset by peer") ||
				strings.Contains(errMsg, "connection refused") ||
				strings.Contains(errMsg, "connection shutdown") ||
				strings.Contains(errMsg, "i/o timeout") ||
				strings.Contains(errMsg, "deadline exceeded") ||
				errors.Is(err, context.DeadlineExceeded)
			if !isTransient || attempt == maxRetries {
				return nil, err
			}
			// 指数退避：100ms, 200ms
			backoff := time.Duration(1<<(attempt+1)) * 50 * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			logger.Warnf("rpc transient error, retrying %d/%d: service=%s method=%s err=%v", attempt+1, maxRetries, service, method, err)
		}
		return nil, lastErr
	}

	cbKey := service + "/" + method
	if p.cb != nil {
		return p.cb.Execute(cbKey, rpcFn)
	}
	return rpcFn()
}

// retryRegister 尝试注册服务，最多重试 maxRetries 次，带退避。
func (p *Proxy) retryRegister(ctx context.Context, serviceName, protoPath string, resolver discovery.Resolver, maxRetries int) error {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if err := p.registerService(ctx, serviceName, protoPath, resolver); err != nil {
			lastErr = err
			if i < maxRetries-1 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(i+1) * time.Second):
				}
			}
			continue
		}
		return nil
	}
	return lastErr
}
