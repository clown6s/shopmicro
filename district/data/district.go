package data

import (
	"context"
	"database/sql"
	"fmt"

	v1 "shopmicro/district/kitex_gen/api/district/v1"

	"shopmicro/pkg/logger"
)

type DistrictDal struct {
	db *sql.DB
}

// GetProvinces 查询所有省级行政区
func (d *DistrictDal) GetProvinces(ctx context.Context) ([]*v1.DistrictNode, error) {
	const query = `
		SELECT id, adcode, name, level, center
		FROM district
		WHERE level = 'province'
		ORDER BY adcode
	`

	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanNodes(rows)
}

// maxAdCodeLen 行政区划代码最大长度（中国标准 adcode 6 位，留足扩展余量）
const maxAdCodeLen = 20

// GetChildrenByParent 查询指定行政区划的直接下级子节点
func (d *DistrictDal) GetChildrenByParent(ctx context.Context, parentAdCode string) ([]*v1.DistrictNode, error) {
	if len(parentAdCode) > maxAdCodeLen || parentAdCode == "" {
		return []*v1.DistrictNode{}, nil
	}

	const query = `
		SELECT id, adcode, name, level, center
		FROM district
		WHERE parent_adcode = ?
		ORDER BY adcode
	`

	rows, err := d.db.QueryContext(ctx, query, parentAdCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanNodes(rows)
}

func scanNodes(rows *sql.Rows) ([]*v1.DistrictNode, error) {
	var nodes []*v1.DistrictNode
	for rows.Next() {
		item := &v1.DistrictNode{}
		if err := rows.Scan(
			&item.Id,
			&item.AdCode,
			&item.Name,
			&item.Level,
			&item.Center,
		); err != nil {
			logger.Errorf("districtRepo scan nodes failed: %v", err)
			return nil, fmt.Errorf("scan district nodes failed: %w", err)
		}
		nodes = append(nodes, item)
	}
	if err := rows.Err(); err != nil {
		logger.Errorf("districtRepo nodes rows error: %v", err)
		return nil, fmt.Errorf("district nodes rows error: %w", err)
	}
	if nodes == nil {
		nodes = []*v1.DistrictNode{}
	}
	return nodes, nil
}
