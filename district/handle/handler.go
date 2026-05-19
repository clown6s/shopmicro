package handle

import (
	"context"
	"fmt"
	"strconv"

	"golang.org/x/sync/singleflight"

	v1 "shopmicro/district/kitex_gen/api/district/v1"
)

// DistrictServiceImpl 行政区划服务实现
// R4-P2-10: 使用 DistrictStore 接口替代具体类型，提升可测试性
type DistrictServiceImpl struct {
	store DistrictStore
	sf    singleflight.Group
}

// R4-P2-10: NewDistrictHandler 接受 DistrictStore 接口
func NewDistrictHandler(store DistrictStore) (*DistrictServiceImpl, error) {
	if store == nil {
		return nil, fmt.Errorf("district: store must not be nil")
	}
	return &DistrictServiceImpl{
		store: store,
	}, nil
}

// GetProvinces —— 返回省级行政区列表（扁平，无 children）
func (s *DistrictServiceImpl) GetProvinces(ctx context.Context, req *v1.GetProvincesReq) (*v1.GetProvincesResp, error) {

	result, err, _ := s.sf.Do("provinces", func() (interface{}, error) {
		nodes, err := s.store.GetProvinces(ctx)
		if err != nil {
			return nil, err
		}
		return &v1.GetProvincesResp{List: nodes}, nil
	})

	if err != nil {
		return nil, err
	}

	resp, ok := result.(*v1.GetProvincesResp)
	if !ok {
		return nil, fmt.Errorf("singleflight: unexpected type %T for GetProvincesResp", result)
	}
	return resp, nil
}

// GetChildren —— 返回指定行政区划的直接下级子节点
func (s *DistrictServiceImpl) GetChildren(ctx context.Context, req *v1.GetChildrenReq) (*v1.GetChildrenResp, error) {

	parentAdCode := req.GetParentAdCode()
	if parentAdCode == 0 {
		return nil, fmt.Errorf("parentAdCode is required")
	}

	code := strconv.FormatUint(uint64(parentAdCode), 10)
	result, err, _ := s.sf.Do("children:"+code, func() (interface{}, error) {
		nodes, err := s.store.GetChildrenByParent(ctx, parentAdCode)
		if err != nil {
			return nil, err
		}
		return &v1.GetChildrenResp{List: nodes}, nil
	})

	if err != nil {
		return nil, err
	}

	resp, ok := result.(*v1.GetChildrenResp)
	if !ok {
		return nil, fmt.Errorf("singleflight: unexpected type %T for GetChildrenResp", result)
	}
	return resp, nil
}

// UpdateDistrict 从高德地图拉取行政区划数据，全量替换数据库。
// R4-P2-11: 使用 singleflight 防止并发全量替换导致锁竞争或数据不一致
func (s *DistrictServiceImpl) UpdateDistrict(ctx context.Context, req *v1.UpdateDistrictReq) (*v1.UpdateDistrictResp, error) {
	_, err, _ := s.sf.Do("update-district", func() (interface{}, error) {
		return nil, s.store.UpdateDistrict(ctx)
	})
	if err != nil {
		return nil, err
	}
	return &v1.UpdateDistrictResp{}, nil
}
