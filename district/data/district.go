package data

import (
	"context"
	"database/sql"
	"fmt"
	v1 "shopmicro/district/kitex_gen/api/district/v1"

	"go.uber.org/zap"
)

type DistrictDal struct {
	db *sql.DB
}

func (d *DistrictDal) GetList(ctx context.Context, adCode string) ([]*v1.DistrictTree, error) {
	var (
		query string
		args  []any
	)

	if adCode != "" {
		// 递归查询指定行政区所属的完整树结构
		// 仅返回：province / city / district / street
		// 限制递归深度防止异常数据导致死循环
		query = `
WITH RECURSIVE ancestors AS (
    -- 从当前节点向上查找祖先节点
    SELECT
        id,
        parent_adcode,
        adcode,
        name,
        level,
        center,
        1 AS depth
    FROM district
    WHERE adcode = ?
      AND level IN ('province', 'city', 'district', 'street')

    UNION ALL

    SELECT
        d.id,
        d.parent_adcode,
        d.adcode,
        d.name,
        d.level,
        d.center,
        a.depth + 1
    FROM district d
    INNER JOIN ancestors a
        ON d.adcode = a.parent_adcode
    WHERE a.depth < 20
      AND d.level IN ('province', 'city', 'district', 'street')
),

root AS (
    -- 获取最顶层祖先节点
    -- 使用 depth DESC 保证拿到最高层级
    SELECT
        id,
        parent_adcode,
        adcode,
        name,
        level,
        center
    FROM ancestors
    ORDER BY depth DESC
    LIMIT 1
),

district_tree AS (
    -- 从根节点向下递归查询所有子节点
    SELECT
        r.id,
        r.parent_adcode,
        r.adcode,
        r.name,
        r.level,
        r.center,
        1 AS depth
    FROM root r

    UNION ALL

    SELECT
        d.id,
        d.parent_adcode,
        d.adcode,
        d.name,
        d.level,
        d.center,
        dt.depth + 1
    FROM district d
    INNER JOIN district_tree dt
        ON d.parent_adcode = dt.adcode
    WHERE dt.depth < 20
      AND d.level IN ('province', 'city', 'district', 'street')
)

SELECT DISTINCT
    id,
    parent_adcode,
    adcode,
    name,
    level,
    center
FROM district_tree
WHERE level IN ('province', 'city', 'district', 'street')
ORDER BY adcode
LIMIT 10000
`
		args = append(args, adCode)
	} else {
		// 返回全部行政区（不包含 country）
		query = `
SELECT
    id,
    parent_adcode,
    adcode,
    name,
    level,
    center
FROM district
WHERE level IN ('province', 'city', 'district', 'street')
ORDER BY adcode
`
	}

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		zap.L().Error(
			"districtRepo.Get query failed",
			zap.String("adCode", adCode),
			zap.Error(err),
		)
		return nil, fmt.Errorf("query district failed: %w", err)
	}
	defer rows.Close()

	return scanDistricts(rows)
}

// scanDistricts 扫描行政区查询结果
func scanDistricts(rows *sql.Rows) ([]*v1.DistrictTree, error) {
	var districts []*v1.DistrictTree
	for rows.Next() {
		item := &v1.DistrictTree{}
		if err := rows.Scan(
			&item.Id,
			&item.ParentAdCode,
			&item.AdCode,
			&item.Name,
			&item.Level,
			&item.Center,
		); err != nil {
			zap.L().Error("districtRepo scan failed", zap.Error(err))
			return nil, fmt.Errorf("scan district failed: %w", err)
		}
		districts = append(districts, item)
	}
	if err := rows.Err(); err != nil {
		zap.L().Error("districtRepo rows error", zap.Error(err))
		return nil, fmt.Errorf("district rows error: %w", err)
	}
	if districts == nil {
		districts = []*v1.DistrictTree{}
	}
	return districts, nil
}
