package handle

import (
	"context"
	v1 "shopmicro/district/kitex_gen/api/district/v1"
)

type DistrictStore interface {
	GetProvinces(ctx context.Context) ([]*v1.DistrictNode, error)
	GetChildrenByParent(ctx context.Context, parentAdCode uint32) ([]*v1.DistrictNode, error)
	UpdateDistrict(ctx context.Context) error
}
