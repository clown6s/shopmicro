package handle

import (
	"context"
	v1 "shopmicro/district/kitex_gen/api/district/v1"
)

type DistrictStore interface {
	GetList(ctx context.Context, adCode string) ([]*v1.DistrictTree, error)
}
