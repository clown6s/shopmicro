package handler

import (
	"context"
	"time"

	"shopmicro/user/module"
)

// UserStore 定义用户管理的数据访问操作。
// 由 *data.UserDal 实现；可在 handler 单元测试中 mock。
type UserStore interface {
	Create(ctx context.Context, user *module.User) error
	GetByID(ctx context.Context, id int64) (*module.User, error)
	GetByPhone(ctx context.Context, phone string) (*module.User, error)
	Update(ctx context.Context, id int64, updates map[string]interface{}) error
	Delete(ctx context.Context, id int64) error
	List(ctx context.Context, page, pageSize int32, name, phone string) ([]*module.User, int64, error)
}

// RedisOps 定义用户服务 handler 使用的 Redis 操作。
// 由 *redis.Client 实现；可在 handler 单元测试中 mock。
type RedisOps interface {
	Get(ctx context.Context, key string) (string, error)
	Del(ctx context.Context, keys ...string) error
	Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	SAddExpire(ctx context.Context, key string, ttl time.Duration, members ...interface{}) error
	SRem(ctx context.Context, key string, members ...interface{}) error
	GetDel(ctx context.Context, key string) (string, error)
	RunScript(ctx context.Context, src string, keys []string, args ...interface{}) (interface{}, error)
}

// 编译时接口满足性检查。
var (
	_ UserStore = (*mockUserStore)(nil)
	_ RedisOps  = (*mockRedisOps)(nil)
)

// mockUserStore 是一个空操作的 UserStore 实现，用于编译时验证。
// 实际测试中请替换为正式的 mock（例如 gomock、counterfeiter）。
type mockUserStore struct{}

func (m *mockUserStore) Create(_ context.Context, _ *module.User) error           { return nil }
func (m *mockUserStore) GetByID(_ context.Context, _ int64) (*module.User, error) { return nil, nil }
func (m *mockUserStore) GetByPhone(_ context.Context, _ string) (*module.User, error) {
	return nil, nil
}
func (m *mockUserStore) Update(_ context.Context, _ int64, _ map[string]interface{}) error {
	return nil
}
func (m *mockUserStore) Delete(_ context.Context, _ int64) error { return nil }
func (m *mockUserStore) List(_ context.Context, _, _ int32, _, _ string) ([]*module.User, int64, error) {
	return nil, 0, nil
}

// mockRedisOps 是一个空操作的 RedisOps 实现，用于编译时验证。
type mockRedisOps struct{}

func (m *mockRedisOps) Get(_ context.Context, _ string) (string, error) { return "", nil }
func (m *mockRedisOps) Del(_ context.Context, _ ...string) error        { return nil }
func (m *mockRedisOps) Set(_ context.Context, _ string, _ interface{}, _ time.Duration) error {
	return nil
}
func (m *mockRedisOps) SAddExpire(_ context.Context, _ string, _ time.Duration, _ ...interface{}) error {
	return nil
}
func (m *mockRedisOps) SRem(_ context.Context, _ string, _ ...interface{}) error { return nil }
func (m *mockRedisOps) GetDel(_ context.Context, _ string) (string, error)       { return "", nil }
func (m *mockRedisOps) RunScript(_ context.Context, _ string, _ []string, _ ...interface{}) (interface{}, error) {
	return nil, nil
}
