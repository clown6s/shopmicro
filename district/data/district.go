package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	v1 "shopmicro/district/kitex_gen/api/district/v1"

	"shopmicro/pkg/logger"
)

type DistrictDal struct {
	db          *sql.DB
	amapKey     string
	amapTimeout time.Duration
	httpClient  *http.Client // 独立客户端，避免共享 http.DefaultClient
}

// 编译时接口满足性检查。
// 此处定义局部接口镜像 handle.DistrictStore 的签名，
// 确保 DistrictDal 在编译期满足 handle 层的接口契约。
// 不直接 import handle 包以保持 data 层对 handle 层的单向依赖（handle → data 不可行，但 data 不应感知 handle）。
var _ districtStoreCheck = (*DistrictDal)(nil)

type districtStoreCheck interface {
	GetProvinces(ctx context.Context) ([]*v1.DistrictNode, error)
	GetChildrenByParent(ctx context.Context, parentAdCode uint32) ([]*v1.DistrictNode, error)
	UpdateDistrict(ctx context.Context) error
}

// GetProvinces 查询所有省级行政区
func (d *DistrictDal) GetProvinces(ctx context.Context) ([]*v1.DistrictNode, error) {
	const query = `
		SELECT id, ad_code, name, level, center
		FROM district
		WHERE level = 'province'
		ORDER BY ad_code
	`

	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanNodes(rows)
}

// GetChildrenByParent 查询指定行政区划的直接下级子节点
func (d *DistrictDal) GetChildrenByParent(ctx context.Context, parentAdCode uint32) ([]*v1.DistrictNode, error) {
	if parentAdCode == 0 {
		return nil, fmt.Errorf("parentAdCode is required")
	}

	const query = `
		SELECT id, ad_code, name, level, center
		FROM district
		WHERE parent_adcode = ?
		ORDER BY ad_code
	`

	rows, err := d.db.QueryContext(ctx, query, parentAdCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanNodes(rows)
}

// UpdateDistrict 从高德地图拉取行政区划数据，全量替换 district 表。
func (d *DistrictDal) UpdateDistrict(ctx context.Context) error {

	if d.amapKey == "" {
		return fmt.Errorf("amap key is not configured, set AMAP_KEY env or config.yaml")
	}
	start := time.Now()
	amapResp, err := d.fetchAMapData(ctx)
	if err != nil {
		return fmt.Errorf("fetch amap data: %w", err)
	}

	records := flattenDistricts(amapResp.Districts, 0)
	if len(records) == 0 {
		return fmt.Errorf("no district data fetched from amap")
	}

	logger.Infof("district update: amap fetch took %v, %d records", time.Since(start), len(records))

	start2 := time.Now()
	if err := d.fullReplace(ctx, records); err != nil {
		return fmt.Errorf("full replace: %w", err)
	}

	logger.Infof("district update: full replace took %v", time.Since(start2))
	return nil
}

// ==================== 高德地图数据获取 ====================

const amapURL = "https://restapi.amap.com/v3/config/district"

type amapResponse struct {
	Status    string         `json:"status"`
	Count     string         `json:"count"`
	Districts []amapDistrict `json:"districts"`
}

type amapDistrict struct {
	AdCode    string          `json:"adcode"`
	Name      string          `json:"name"`
	Level     string          `json:"level"`
	Center    string          `json:"center"`
	Districts []amapDistrict  `json:"districts"`
	CityCode  json.RawMessage `json:"citycode"` // 类型不一致（string/array），不保存
}

// fetchAMapData 请求高德地图行政区划接口，subdistrict=4 获取到区县级。
func (d *DistrictDal) fetchAMapData(ctx context.Context) (*amapResponse, error) {
	timeout := d.amapTimeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}

	httpCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, http.MethodGet,
		fmt.Sprintf("%s?subdistrict=4&key=%s", amapURL, d.amapKey), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("amap api returned status %d", resp.StatusCode)
	}

	// R4-P2-13: 限制最大读取 10MB，防止 API 端异常返回大量数据导致 OOM
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var amapResp amapResponse
	if err := json.Unmarshal(body, &amapResp); err != nil {
		return nil, fmt.Errorf("unmarshal amap response: %w", err)
	}

	if amapResp.Status != "1" {
		return nil, fmt.Errorf("amap api error, status=%s", amapResp.Status)
	}

	return &amapResp, nil
}

// ==================== 数据扁平化 ====================

type districtRecord struct {
	AdCode       int
	Name         string
	Level        string
	Center       string
	ParentAdCode int
}

// flattenDistricts 递归将嵌套的高德数据展平为扁记录列表。
// parentAdCode=0 时表示顶级（country），跳过不存。
func flattenDistricts(districts []amapDistrict, parentAdCode int) []districtRecord {
	var records []districtRecord
	seen := make(map[int]bool)

	for _, d := range districts {
		// 跳过国家级（高德返回的根节点）
		if d.Level == "country" {
			records = append(records, flattenDistricts(d.Districts, 0)...)
			continue
		}

		adCode, err := strconv.Atoi(d.AdCode)
		if err != nil || adCode == 0 {
			logger.Warnf("skip invalid adcode: %s (%s)", d.AdCode, d.Name)
			continue
		}

		if seen[adCode] {
			logger.Warnf("skip duplicate adcode: %d (%s)", adCode, d.Name)
			continue
		}
		seen[adCode] = true

		records = append(records, districtRecord{
			AdCode:       adCode,
			Name:         d.Name,
			Level:        d.Level,
			Center:       d.Center,
			ParentAdCode: parentAdCode,
		})

		// 递归子节点
		if len(d.Districts) > 0 {
			records = append(records, flattenDistricts(d.Districts, adCode)...)
		}
	}

	return records
}

// ==================== 全量替换 ====================

// batchSize 全量替换时每批 INSERT 的记录数。
// 1000 条在 MySQL max_allowed_packet 默认 64MB 下安全，
// 且 batch 过大会导致锁表时间过长。
const batchSize = 1000

// R4-P1-06: 使用命名返回值，确保 tx.Commit() 失败时 defer 能看到错误并 rollback
func (d *DistrictDal) fullReplace(ctx context.Context, records []districtRecord) (err error) {
	// R7-P2-09: 前置条件检查，防止 nil/空 records 导致删全表不插入
	if len(records) == 0 {
		return fmt.Errorf("fullReplace: records must not be empty")
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	_, err = tx.ExecContext(
		ctx,
		"DELETE FROM district",
	)
	if err != nil {
		return err
	}

	// 累计实际写入行数，防止所有记录被过滤后数据库变空
	var totalAffected int64

	for i := 0; i < len(records); i += batchSize {

		end := i + batchSize
		if end > len(records) {
			end = len(records)
		}

		batch := records[i:end]

		values := make([]string, 0, len(batch))
		args := make([]any, 0, len(batch)*5)

		for _, r := range batch {
			values = append(values, "(?,?,?,?,?,NOW())")
			args = append(args, r.AdCode, r.Name, r.Level, r.Center, r.ParentAdCode)
		}

		query := fmt.Sprintf(
			"INSERT INTO district (ad_code, name, level, center, parent_adcode, created_at) VALUES %s",
			strings.Join(values, ","),
		)

		var result sql.Result
		result, err = tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}

		n, _ := result.RowsAffected()
		totalAffected += n
	}

	if totalAffected == 0 {
		return fmt.Errorf("fullReplace: all %d records were filtered (no valid data inserted), aborting to prevent empty table", len(records))
	}

	return tx.Commit()
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
