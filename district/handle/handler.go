package handle

import (
	"context"

	"golang.org/x/sync/singleflight"

	"shopmicro/district/data"
	v1 "shopmicro/district/kitex_gen/api/district/v1"
)

type DistrictServiceImpl struct {
	districtDal *data.DistrictDal
	sf          singleflight.Group
}

func NewDistrictHandler(d *data.Data) *DistrictServiceImpl {
	return &DistrictServiceImpl{
		districtDal: d.District(),
	}
}

// GetProvinces —— 返回省级行政区列表（扁平，无 children）
func (s *DistrictServiceImpl) GetProvinces(ctx context.Context, req *v1.GetProvincesReq) (*v1.GetProvincesResp, error) {

	result, err, _ := s.sf.Do("provinces", func() (interface{}, error) {
		nodes, err := s.districtDal.GetProvinces(ctx)
		if err != nil {
			return nil, err
		}
		return &v1.GetProvincesResp{List: nodes}, nil
	})

	if err != nil {
		return nil, err
	}

	return result.(*v1.GetProvincesResp), nil
}

// GetChildren —— 返回指定行政区划的直接下级子节点
func (s *DistrictServiceImpl) GetChildren(ctx context.Context, req *v1.GetChildrenReq) (*v1.GetChildrenResp, error) {

	parentAdCode := req.GetParentAdCode()
	if parentAdCode == "" {
		return &v1.GetChildrenResp{List: []*v1.DistrictNode{}}, nil
	}

	result, err, _ := s.sf.Do("children:"+parentAdCode, func() (interface{}, error) {
		nodes, err := s.districtDal.GetChildrenByParent(ctx, parentAdCode)
		if err != nil {
			return nil, err
		}
		return &v1.GetChildrenResp{List: nodes}, nil
	})

	if err != nil {
		return nil, err
	}

	return result.(*v1.GetChildrenResp), nil
}

// UpdateDistrict 从高德地图拉取行政区划数据，全量替换数据库。
func (s *DistrictServiceImpl) UpdateDistrict(ctx context.Context, req *v1.UpdateDistrictReq) (*v1.UpdateDistrictResp, error) {
	if err := s.districtDal.UpdateDistrict(ctx); err != nil {
		return nil, err
	}
	return &v1.UpdateDistrictResp{}, nil
}
