package handler

import (
	"context"

	v1 "shopmicro/product/kitex_gen/api/product/v1"
)

// CategoryStore 商品分类数据访问接口
type CategoryStore interface {
	CreateCategory(ctx context.Context, name string, parentCid int64, showStatus int32, sort int32, icon string) (int64, error)
	GetCategoryTree(ctx context.Context) ([]*v1.CategoryTreeNode, error)
	UpdateCategory(ctx context.Context, catId int64, name string, showStatus int32, sort int32, icon string) error
	DeleteCategory(ctx context.Context, catId int64) error
}
