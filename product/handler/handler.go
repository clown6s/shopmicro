package handler

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/singleflight"

	"shopmicro/pkg/errcode"
	"shopmicro/pkg/logger"
	v1 "shopmicro/product/kitex_gen/api/product/v1"
)

// newBiz 创建业务错误（无精确 HTTP 状态码，由 gateway 按错误码范围推断）
func newBiz(code uint32, msg string) *errcode.BizError { return errcode.NewBizError(code, msg) }

// newBizHTTP 创建附带 HTTP 状态码的业务错误
func newBizHTTP(code uint32, msg string, httpStatus int) *errcode.BizError {
	return errcode.BizErrorHTTPStatus(code, msg, httpStatus)
}

var (
	errProductNameRequired = newBiz(errcode.CodeProductParam, "name is required")
	errProductNameTooLong  = newBiz(errcode.CodeProductParam, "name too long (max 64 chars)")
	errProductIconTooLong  = newBiz(errcode.CodeProductParam, "icon too long (max 512 chars)")
	errProductCatIdReq     = newBiz(errcode.CodeProductParam, "cat_id is required")
	errProductInternal     = newBizHTTP(errcode.CodeProductInternal, "internal error", 500)
)

// ProductServiceImpl 商品服务实现
type ProductServiceImpl struct {
	store CategoryStore
	sf    *singleflight.Group
	sfMu  sync.RWMutex // 保护 sf 字段的并发读写
}

// NewProductHandler 创建商品服务 handler
func NewProductHandler(store CategoryStore) (*ProductServiceImpl, error) {
	if store == nil {
		return nil, fmt.Errorf("product: store must not be nil")
	}
	return &ProductServiceImpl{
		store: store,
		sf:    &singleflight.Group{},
	}, nil
}

// getSF 返回当前 singleflight.Group（读锁保护）。
func (s *ProductServiceImpl) getSF() *singleflight.Group {
	s.sfMu.RLock()
	defer s.sfMu.RUnlock()
	return s.sf
}

// resetSF 创建新的 singleflight.Group 以清除缓存（写锁保护）。
func (s *ProductServiceImpl) resetSF() {
	s.sfMu.Lock()
	defer s.sfMu.Unlock()
	s.sf = &singleflight.Group{}
}

// CreateCategory 新增分类
func (s *ProductServiceImpl) CreateCategory(ctx context.Context, req *v1.CreateCategoryReq) (*v1.CreateCategoryResp, error) {
	name := req.GetName()
	if name == "" {
		return nil, errProductNameRequired
	}
	if len(name) > 64 {
		return nil, errProductNameTooLong
	}
	if len(req.GetIcon()) > 512 {
		return nil, errProductIconTooLong
	}

	catId, err := s.store.CreateCategory(ctx, name, req.GetParentCid(), req.GetShowStatus(), req.GetSort(), req.GetIcon())
	if err != nil {
		return nil, err
	}

	// 新增成功，清除查询缓存
	s.resetSF()
	logger.Infof("product: category created (cat_id=%d, name=%q), query cache cleared", catId, name)

	return &v1.CreateCategoryResp{CatId: catId}, nil
}

// GetCategoryTree 获取全部分类树（嵌套 children）
func (s *ProductServiceImpl) GetCategoryTree(ctx context.Context, req *v1.GetCategoryTreeReq) (*v1.GetCategoryTreeResp, error) {
	result, err, shared := s.getSF().Do("category_tree", func() (interface{}, error) {
		tree, err := s.store.GetCategoryTree(ctx)
		if err != nil {
			return nil, err
		}
		return &v1.GetCategoryTreeResp{List: tree}, nil
	})
	if shared {
		logger.Debugf("product: GetCategoryTree request coalesced by singleflight")
	}

	if err != nil {
		return nil, err
	}

	resp, ok := result.(*v1.GetCategoryTreeResp)
	if !ok {
		return nil, errProductInternal
	}
	return resp, nil
}

// UpdateCategory 更新分类信息
func (s *ProductServiceImpl) UpdateCategory(ctx context.Context, req *v1.UpdateCategoryReq) (*v1.UpdateCategoryResp, error) {
	catId := req.GetCatId()
	if catId == 0 {
		return nil, errProductCatIdReq
	}
	if req.GetName() != "" && len(req.GetName()) > 64 {
		return nil, errProductNameTooLong
	}
	if req.GetIcon() != "" && len(req.GetIcon()) > 512 {
		return nil, errProductIconTooLong
	}

	if err := s.store.UpdateCategory(ctx, catId, req.GetName(), req.GetShowStatus(), req.GetSort(), req.GetIcon()); err != nil {
		return nil, err
	}

	// 更新成功，清除查询缓存
	s.resetSF()
	logger.Infof("product: category updated (cat_id=%d), query cache cleared", catId)

	return &v1.UpdateCategoryResp{}, nil
}

// DeleteCategory 删除分类（软删除，级联删除所有子分类）
func (s *ProductServiceImpl) DeleteCategory(ctx context.Context, req *v1.DeleteCategoryReq) (*v1.DeleteCategoryResp, error) {
	catId := req.GetCatId()
	if catId == 0 {
		return nil, errProductCatIdReq
	}

	if err := s.store.DeleteCategory(ctx, catId); err != nil {
		return nil, err
	}

	// 删除成功，清除查询缓存
	s.resetSF()
	logger.Infof("product: category deleted (cat_id=%d), query cache cleared", catId)

	return &v1.DeleteCategoryResp{}, nil
}
