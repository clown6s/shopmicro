package handle

import (
	"context"
	"shopmicro/district/data"
	v1 "shopmicro/district/kitex_gen/api/district/v1"
	"shopmicro/pkg/logger"
	"shopmicro/pkg/redis"

	"go.uber.org/zap"
)

// DistrictServiceImpl implements the last service interface defined in the IDL.
type DistrictServiceImpl struct {
	districtDal *data.DistrictDal
	rdb         *redis.Client
}

func NewDistrictHandler(d *data.Data) *DistrictServiceImpl {
	return &DistrictServiceImpl{
		rdb:         d.Rdb,
		districtDal: d.District(),
	}
}

// GetProvinceTree implements the DistrictServiceImpl interface.
func (s *DistrictServiceImpl) GetProvinceTree(ctx context.Context, req *v1.GetProvinceTreeReq) (resp *v1.GetProvinceTreeResp, err error) {
	logger.Info("调用了", zap.String("adCode", req.AdCode))
	// TODO: Your code here...
	return
}
