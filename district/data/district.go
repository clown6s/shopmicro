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

func (d *DistrictDal) GetList(
	ctx context.Context,
	adCode string,
) ([]*v1.DistrictTree, error) {

	var (
		query string
		args  []any
	)

	// 全国
	if adCode == "" {

		query = `
			SELECT
				id,
				parent_adcode,
				adcode,
				name,
				level,
				center
			FROM district
			WHERE level IN (
				'province',
				'city',
				'district',
				'street'
			)
			ORDER BY adcode
		`

	} else {

		// 指定节点+所有下辖

		query = `
		WITH RECURSIVE district_tree AS (

			SELECT
				id,
				parent_adcode,
				adcode,
				name,
				level,
				center
			FROM district
			WHERE adcode = ?

			UNION ALL

			SELECT
				d.id,
				d.parent_adcode,
				d.adcode,
				d.name,
				d.level,
				d.center
			FROM district d
			JOIN district_tree t
				ON d.parent_adcode=t.adcode
		)

		SELECT *
		FROM district_tree
		ORDER BY adcode
		`

		args =
			append(
				args,
				adCode,
			)
	}

	rows, err :=
		d.db.QueryContext(
			ctx,
			query,
			args...,
		)

	if err != nil {
		return nil, err
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
