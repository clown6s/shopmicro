package data

import (
	"database/sql"
	"net/http"
	"time"

	"shopmicro/pkg/logger"
	"shopmicro/pkg/mysql"
)

type Data struct {
	DB          *sql.DB
	districtDal *DistrictDal // 缓存以避免每次调用重新分配
}

func New(cfg mysql.Config, amapKey string, amapTimeout time.Duration) (*Data, func(), error) {
	// 使用 pkg/mysql 进行数据库连接（OTEL 配置集中管理）
	db, dbCleanup, err := mysql.New(cfg)
	if err != nil {
		return nil, nil, err
	}

	cleanup := func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf("db cleanup panic during shutdown (recovered): %v", r)
			}
		}()
		dbCleanup()
	}

	d := &Data{
		DB: db,
	}
	d.districtDal = &DistrictDal{
		db:          db,
		amapKey:     amapKey,
		amapTimeout: amapTimeout,
		// P1-02: 独立 HTTP 客户端，避免共享 http.DefaultClient（无超时、可被全局修改）。
		// 不设置 Client.Timeout —— 由请求级 context.WithTimeout 控制。
		httpClient: &http.Client{},
	}
	return d, cleanup, nil
}

// District 返回缓存的 DistrictDal 实例。
// 线程安全 —— DistrictDal 是无状态的（仅持有 *sql.DB）。
func (d *Data) District() *DistrictDal {
	return d.districtDal
}
