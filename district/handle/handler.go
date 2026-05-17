package handle

import (
	"context"

	"shopmicro/district/data"
	v1 "shopmicro/district/kitex_gen/api/district/v1"
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
// adCode 为空时返回全部行政区（以省级为根），非空时返回该区划所属的完整子树。
func (s *DistrictServiceImpl) GetProvinceTree(ctx context.Context, req *v1.GetProvinceTreeReq) (*v1.GetProvinceTreeResp, error) {
	adCode := req.GetAdCode()

	flatList, err := s.districtDal.GetList(ctx, adCode)
	if err != nil {
		return nil, err
	}

	tree := buildTree(flatList)

	return &v1.GetProvinceTreeResp{Data: tree}, nil
}

// buildTree 将平铺的行政区列表组装为树结构。
// 算法：先构建 adCode → node 映射，再将每个节点挂到其父节点的 children 下，
// 最后收集所有根节点（parentAdCode 为空或在列表中找不到父节点的节点）。
func buildTree(list []*v1.DistrictTree) []*v1.DistrictTree {
	if len(list) == 0 {
		return list
	}

	nodeMap := make(map[string]*v1.DistrictTree, len(list))
	for _, node := range list {
		if node != nil {
			nodeMap[node.AdCode] = node
		}
	}

	var roots []*v1.DistrictTree
	for _, node := range list {
		if node == nil {
			continue
		}
		if node.ParentAdCode == "" {
			// 无父级 → 根节点
			roots = append(roots, node)
			continue
		}
		if parent, ok := nodeMap[node.ParentAdCode]; ok {
			parent.Children = append(parent.Children, node)
		} else {
			// 父节点不在当前结果集中 → 也视为根节点
			roots = append(roots, node)
		}
	}

	return roots
}
