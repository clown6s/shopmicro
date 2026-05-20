package handler

import (
	"context"
	v1 "shopmicro/product/kitex_gen/api/product/v1"
)

// ProductServiceImpl implements the last service interface defined in the IDL.
type ProductServiceImpl struct{}

// GetCategory implements the ProductServiceImpl interface.
func (s *ProductServiceImpl) GetCategory(ctx context.Context, req *v1.GetCategoryReq) (resp *v1.GetProvincesResp, err error) {
	// TODO: Your code here...
	return
}

// UpdateCategory implements the ProductServiceImpl interface.
func (s *ProductServiceImpl) UpdateCategory(ctx context.Context, req *v1.UpdateCategoryReq) (resp *v1.UpdateCategoryResp, err error) {
	// TODO: Your code here...
	return
}
