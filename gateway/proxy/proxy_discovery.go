package proxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/cloudwego/kitex/pkg/discovery"

	clientv3 "go.etcd.io/etcd/client/v3"

	"shopmicro/pkg/logger"
)

// =========================
// 发现 & 监听服务
// =========================

// DiscoverAndRegister 扫描 Etcd 中所有当前已注册的 Kitex 服务，
// 并为每个服务注册一个泛化客户端。
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	prefix := etcdRegistryPrefix + "/"
	resp, err := etcdClient.Get(ctx, prefix, clientv3.WithPrefix(), clientv3.WithKeysOnly())
	if err != nil {
		return fmt.Errorf("etcd list services: %w", err)
	}

	seen := make(map[string]struct{})
	instanceKeysMap := make(map[string][]string) // service name → 实例 etcd key 列表
	var regFailures []string
	for _, kv := range resp.Kvs {
		keyStr := string(kv.Key)
		svcName := extractServiceName(keyStr, prefix)
		if svcName == "" {
			continue
		}
		instanceKeysMap[svcName] = append(instanceKeysMap[svcName], keyStr)

		if _, done := seen[svcName]; done {
			continue
		}
		seen[svcName] = struct{}{}

		protoPath := buildProtoPath(protoBase, svcName)

		if _, err := os.Stat(protoPath); err != nil {
			if os.IsNotExist(err) {
				// 警告并跳过该服务，允许 gateway 启动（服务可后补 proto 后再注册）
				logger.Warnf("proto file not found for service %q: %s — skipping registration, "+
					"service will be available once proto is in place", svcName, protoPath)
				continue
			}
			logger.Warnf("stat proto file for service %q: %v — skipping", svcName, err)
			continue
		}

		if err := p.registerService(ctx, svcName, protoPath, resolver); err != nil {
			regFailures = append(regFailures, svcName)
			logger.Warnf("failed to register service %s: %v", svcName, err)
		}
	}
	if len(regFailures) > 0 {
		return fmt.Errorf("failed to register services at startup: %v", regFailures)
	}
	// 用 etcd 中的实际实例 key 初始化 instanceKeys
	p.mu.Lock()
	for svcName, keys := range instanceKeysMap {
		if existing, ok := p.instanceKeys[svcName]; ok && len(keys) > 0 {
			for _, k := range keys {
				existing[k] = true
			}
		}
	}
	p.mu.Unlock()
	return nil
}

// WatchServices 监听 Etcd 中新注册的服务并自动注册。
// 它在后台协程中运行，立即返回。
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
				return
			}

			err := p.watchOnce(ctx, resolver, etcdAddrs, protoBase)

			if ctx.Err() != nil {
				return
			}

			if err != nil {
				logger.Warnf("WatchServices: watch closed, reconnecting in %v: %v", backoff, err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if errors.Is(err, errWatchClosed) {
					backoff = time.Second
				} else {
					backoff *= 2
					if backoff > maxBackoff {
						backoff = maxBackoff
					}
				}
			}
		}
	}()
}

// watchOnce 执行单次 etcd watch 循环，直到通道关闭或上下文取消。
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
	watchCh := etcdClient.Watch(ctx, prefix, clientv3.WithPrefix())

	for wresp := range watchCh {
		if wresp.Err() != nil {
			return fmt.Errorf("etcd watch error: %w", wresp.Err())
		}
		for _, ev := range wresp.Events {
			svcName := extractServiceName(string(ev.Kv.Key), prefix)
			if svcName == "" {
				continue
			}

			switch ev.Type {
			case clientv3.EventTypePut:
				p.mu.Lock()
				keyStr := string(ev.Kv.Key)
				if p.instanceKeys[svcName] == nil {
					p.instanceKeys[svcName] = make(map[string]bool)
				}
				if !p.instanceKeys[svcName][keyStr] {
					p.instanceKeys[svcName][keyStr] = true
				}
				p.mu.Unlock()

				if _, ok := p.getClient(svcName); ok {
					continue
				}
				protoPath := buildProtoPath(protoBase, svcName)

				if _, err := os.Stat(protoPath); err != nil {
					if os.IsNotExist(err) {
						logger.Warnf("WatchServices: proto file not found for %s: %s — service registered but not usable until proto is in place", svcName, protoPath)
						continue
					}
					logger.Warnf("WatchServices: stat proto for %s: %v", svcName, err)
					continue
				}

				regErr := p.retryRegister(ctx, svcName, protoPath, resolver, p.registerRetries)
				if regErr != nil {
					logger.Warnf("WatchServices: failed to register %s after retries: %v", svcName, regErr)
				}
			case clientv3.EventTypeDelete:
				p.mu.Lock()
				keyStr := string(ev.Kv.Key)
				var shouldRemove bool
				var remaining int
				if keys, ok := p.instanceKeys[svcName]; ok {
					delete(keys, keyStr)
					remaining = len(keys)
					shouldRemove = remaining == 0
				}
				p.mu.Unlock()

				if shouldRemove {
					p.mu.Lock()
					if sc, ok := p.clients[svcName]; ok {
						delete(p.clients, svcName)
						delete(p.instanceKeys, svcName)
						p.mu.Unlock()
						if err := sc.Generic.Close(); err != nil {
							logger.Warnf("WatchServices: close generic for %s: %v", svcName, err)
						}
						if sc.Provider != nil {
							if err := sc.Provider.Close(); err != nil {
								logger.Warnf("WatchServices: close provider for %s: %v", svcName, err)
							}
						}
						if err := sc.Client.Close(); err != nil {
							logger.Warnf("WatchServices: close client for %s: %v", svcName, err)
						}
						logger.Infof("WatchServices: removed client for %s (all instances deregistered)", svcName)
					} else {
						p.mu.Unlock()
					}
				} else if remaining > 0 {
					logger.Infof("WatchServices: instance deregistered for %s (remaining: %d)", svcName, remaining)
				}
			}
		}
	}

	// 区分 ctx 取消（正常关闭）和连接断开（异常关闭）
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errWatchClosed
}

// newEtcdClient 创建一个最小化的 etcd v3 客户端。
func newEtcdClient(addrs []string) (*clientv3.Client, error) {
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   addrs,
		DialTimeout: etcdDialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to etcd %v: %w", addrs, err)
	}
	return cli, nil
}
