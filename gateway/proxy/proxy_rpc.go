package proxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/kitex/client/callopt"
	"github.com/cloudwego/kitex/pkg/discovery"
	"github.com/sony/gobreaker"

	"shopmicro/pkg/logger"
)

// Call 对指定的服务和方法执行泛化 RPC 调用。
// req 必须是 JSON 编码的字符串（用于 JSONPbGeneric）。
// method 必须是 CamelCase 的 protobuf RPC 名称。
//
// 每次重试独立通过熔断器，确保熔断状态变更能被及时感知。
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

	cbKey := service + "/" + method
	maxRetries := p.maxRetries
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// 每次重试创建独立的超时 context，避免前一次耗时过长导致后续重试无有效时间
		var perCtx context.Context
		var perCancel context.CancelFunc
		if deadline, ok := ctx.Deadline(); ok {
			perCtx, perCancel = context.WithDeadline(ctx, deadline)
		} else {
			perCtx, perCancel = context.WithCancel(ctx)
		}

		// 每次重试独立通过熔断器，确保熔断状态变更能被及时感知。
		rpcFn := func() (interface{}, error) {
			return cli.Client.GenericCall(perCtx, method, req, callOpts...)
		}

		var resp interface{}
		var err error
		if p.cb != nil {
			resp, err = p.cb.Execute(cbKey, rpcFn)
		} else {
			resp, err = rpcFn()
		}
		perCancel()

		if err == nil {
			return resp, nil
		}
		lastErr = err

		// 熔断器打开或半开状态请求过多，快速失败不重试。
		if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
			return nil, err
		}

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
