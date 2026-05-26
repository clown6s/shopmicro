package handler

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"golang.org/x/sync/singleflight"

	v1 "shopmicro/district/kitex_gen/api/district/v1"
	"shopmicro/pkg/errcode"
	"shopmicro/pkg/logger"
)

// DistrictServiceImpl 行政区划服务实现
// R4-P2-10: 使用 DistrictStore 接口替代具体类型，提升可测试性
type DistrictServiceImpl struct {
	store    DistrictStore
	sfMu     sync.RWMutex        // 保护 sf 字段的并发读写
	sf       *singleflight.Group // 使用指针，UpdateDistrict 时替换清除所有缓存
	updateMu sync.Mutex          // 序列化全量更新操作，防止 singleflight 吞请求
}

// R4-P2-10: NewDistrictHandler 接受 DistrictStore 接口
func NewDistrictHandler(store DistrictStore) (*DistrictServiceImpl, error) {
	if store == nil {
		return nil, fmt.Errorf("district: store must not be nil")
	}
	return &DistrictServiceImpl{
		store: store,
		sf:    &singleflight.Group{},
	}, nil
}

// getSF 安全读取当前的 singleflight.Group
func (s *DistrictServiceImpl) getSF() *singleflight.Group {
	s.sfMu.RLock()
	sf := s.sf
	s.sfMu.RUnlock()
	return sf
}

// resetSF 原子替换 singleflight.Group，清除所有查询缓存
func (s *DistrictServiceImpl) resetSF() {
	s.sfMu.Lock()
	s.sf = &singleflight.Group{}
	s.sfMu.Unlock()
}

// GetProvinces —— 返回省级行政区列表（扁平，无 children）
func (s *DistrictServiceImpl) GetProvinces(ctx context.Context, req *v1.GetProvincesReq) (*v1.GetProvincesResp, error) {

	result, err, shared := s.getSF().Do("provinces", func() (interface{}, error) {
		nodes, err := s.store.GetProvinces(ctx)
		if err != nil {
			return nil, err
		}
		return &v1.GetProvincesResp{List: nodes}, nil
	})
	if shared {
		logger.Infof("district: GetProvinces request coalesced by singleflight")
	}

	if err != nil {
		return nil, err
	}

	resp, ok := result.(*v1.GetProvincesResp)
	if !ok {
		return nil, errcode.NewBizError(errcode.CodeDistrictInternal, "internal error")
	}
	return resp, nil
}

// GetChildren —— 返回指定行政区划的直接下级子节点
func (s *DistrictServiceImpl) GetChildren(ctx context.Context, req *v1.GetChildrenReq) (*v1.GetChildrenResp, error) {

	parentAdCode := req.GetParentAdCode()
	if parentAdCode == 0 {
		return nil, errcode.NewBizError(errcode.CodeDistrictParam, "parentAdCode is required")
	}

	code := strconv.FormatUint(uint64(parentAdCode), 10)
	result, err, shared := s.getSF().Do("children:"+code, func() (interface{}, error) {
		nodes, err := s.store.GetChildrenByParent(ctx, parentAdCode)
		if err != nil {
			return nil, err
		}
		return &v1.GetChildrenResp{List: nodes}, nil
	})
	if shared {
		logger.Infof("district: GetChildren[%s] request coalesced by singleflight", code)
	}

	if err != nil {
		return nil, err
	}

	resp, ok := result.(*v1.GetChildrenResp)
	if !ok {
		return nil, errcode.NewBizError(errcode.CodeDistrictInternal, "internal error")
	}
	return resp, nil
}

// UpdateDistrict 从高德地图拉取行政区划数据，全量替换数据库。
// 使用 mutex 序列化全量更新，避免 singleflight 吞掉重复请求。
func (s *DistrictServiceImpl) UpdateDistrict(ctx context.Context, req *v1.UpdateDistrictReq) (*v1.UpdateDistrictResp, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	if err := s.store.UpdateDistrict(ctx); err != nil {
		return nil, err
	}

	// 数据已更新，替换 singleflight Group 清除所有查询缓存
	// （包括 "provinces" 和所有 "children:xxx"），下次请求重新从数据库加载
	s.resetSF()
	logger.Info("district: singleflight query cache cleared after UpdateDistrict")
	return &v1.UpdateDistrictResp{}, nil
}
