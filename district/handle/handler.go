package handle

import (
	"context"
	"fmt"

	"shopmicro/district/data"
	v1 "shopmicro/district/kitex_gen/api/district/v1"
	"shopmicro/district/module"
)

// DistrictServiceImpl 实现了 IDL 中定义的最后一个服务接口。
type DistrictServiceImpl struct {
	districtDal *data.DistrictDal
}

func NewDistrictHandler(d *data.Data) *DistrictServiceImpl {
	return &DistrictServiceImpl{
		districtDal: d.District(),
	}
}

// GetProvinceTree 返回行政区划树。
// adCode 为空时返回所有省级节点（一层）；
// adCode 非空时返回该行政区划及其子级树（最多展开到区/县级）。
func (s *DistrictServiceImpl) GetProvinceTree(ctx context.Context, req *v1.GetProvinceTreeReq) (*v1.GetProvinceTreeResp, error) {
	adCode := req.GetAdCode()

	fmt.Print("adCode:", adCode)

	// 查询指定 adCode 的子级树（最多展开 2 层：市 → 区/县）
	return nil, nil
}

func toTreeList(list []*module.District) []*v1.DistrictTree {
	if len(list) == 0 {
		return nil
	}
	out := make([]*v1.DistrictTree, 0, len(list))
	for _, d := range list {
		out = append(out, toTree(d))
	}
	return out
}

func toTree(d *module.District) *v1.DistrictTree {
	return &v1.DistrictTree{
		Id:           d.ID,
		ParentAdCode: d.ParentAdCode,
		AdCode:       d.AdCode,
		Name:         d.Name,
		Level:        d.Level,
		Center:       d.Center,
	}
}
