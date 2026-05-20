package data

import (
	"database/sql"

	"shopmicro/pkg/logger"
	"shopmicro/pkg/mysql"
)

type Data struct {
	DB          *sql.DB
	categoryDal *CategoryDal
}

func New(cfg mysql.Config) (*Data, func(), error) {
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
	d.categoryDal = &CategoryDal{
		db: db,
	}
	return d, cleanup, nil
}

// Category 返回缓存的 CategoryDal 实例
func (d *Data) Category() *CategoryDal {
	return d.categoryDal
}
