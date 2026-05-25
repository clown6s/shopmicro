package data

import (
	"context"
	"database/sql"
	"fmt"

	v1 "shopmicro/product/kitex_gen/api/product/v1"

	"shopmicro/pkg/logger"
)

// CategoryDal 商品分类数据访问层
type CategoryDal struct {
	db *sql.DB
}

// 编译时接口满足性检查
var _ categoryStoreCheck = (*CategoryDal)(nil)

type categoryStoreCheck interface {
	CreateCategory(ctx context.Context, name string, parentCid int64, showStatus int32, sort int32, icon string) (int64, error)
	GetCategoryTree(ctx context.Context) ([]*v1.CategoryTreeNode, error)
	UpdateCategory(ctx context.Context, catId int64, name string, showStatus int32, sort int32, icon string) error
	DeleteCategory(ctx context.Context, catId int64) error
}

// CreateCategory 新增分类，返回自增 cat_id
// parentCid=0 时 catLevel=1（一级），否则为父级 catLevel+1
func (d *CategoryDal) CreateCategory(ctx context.Context, name string, parentCid int64, showStatus int32, sort int32, icon string) (int64, error) {
	if name == "" {
		return 0, fmt.Errorf("name is required")
	}

	// 计算层级
	catLevel := int32(1)
	if parentCid != 0 {
		var parentLevel int32
		err := d.db.QueryRowContext(ctx,
			"SELECT cat_level FROM category WHERE cat_id = ? AND deleted_at IS NULL", parentCid,
		).Scan(&parentLevel)
		if err != nil {
			return 0, fmt.Errorf("parent category not found (parentCid=%d): %w", parentCid, err)
		}
		catLevel = parentLevel + 1
		if catLevel > 3 {
			return 0, fmt.Errorf("category level exceeds max depth 3 (parentCid=%d, parentLevel=%d)", parentCid, parentLevel)
		}
	}

	const query = `
		INSERT INTO category (name, parent_cid, cat_level, show_status, sort, icon)
		VALUES (?, ?, ?, ?, ?, ?)
	`
	result, err := d.db.ExecContext(ctx, query, name, parentCid, catLevel, showStatus, sort, icon)
	if err != nil {
		return 0, fmt.Errorf("create category: %w", err)
	}

	catId, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create category: get last insert id: %w", err)
	}

	logger.Infof("category created: cat_id=%d, name=%q, parentCid=%d, level=%d", catId, name, parentCid, catLevel)
	return catId, nil
}

// ListCategories 查询全部分类（排除软删除），按 sort 升序、cat_id 升序
func (d *CategoryDal) ListCategories(ctx context.Context) ([]*v1.CategoryNode, error) {
	const query = `
		SELECT cat_id, name, parent_cid, cat_level, show_status, sort, icon
		FROM category
		WHERE deleted_at IS NULL
		ORDER BY sort ASC, cat_id ASC
	`

	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanCategoryNodes(rows)
}

// GetCategoryTree 获取嵌套树结构
// 一次查出所有分类，在内存中构建树，避免 N+1 查询
func (d *CategoryDal) GetCategoryTree(ctx context.Context) ([]*v1.CategoryTreeNode, error) {
	all, err := d.ListCategories(ctx)
	if err != nil {
		return nil, err
	}

	// 构建映射 + 子节点索引
	nodeMap := make(map[int64]*v1.CategoryTreeNode)
	childrenMap := make(map[int64][]*v1.CategoryTreeNode)
	for _, item := range all {
		tree := &v1.CategoryTreeNode{
			CatId:      item.CatId,
			Name:       item.Name,
			ParentCid:  item.ParentCid,
			CatLevel:   item.CatLevel,
			ShowStatus: item.ShowStatus,
			Sort:       item.Sort,
			Icon:       item.Icon,
			Children:   nil,
		}
		nodeMap[item.CatId] = tree
		childrenMap[item.ParentCid] = append(childrenMap[item.ParentCid], tree)
	}

	// 组装 children
	var roots []*v1.CategoryTreeNode
	for _, node := range nodeMap {
		if children, ok := childrenMap[node.CatId]; ok {
			node.Children = children
		}
		if node.ParentCid == 0 {
			roots = append(roots, node)
		}
	}

	if roots == nil {
		roots = []*v1.CategoryTreeNode{}
	}
	return roots, nil
}

// UpdateCategory 更新分类信息（仅更新非零值字段）
func (d *CategoryDal) UpdateCategory(ctx context.Context, catId int64, name string, showStatus int32, sort int32, icon string) error {
	if catId == 0 {
		return fmt.Errorf("cat_id is required")
	}

	// 构建 SET 子句，仅更新非零值字段
	type field struct {
		col string
		val interface{}
	}
	var fields []field

	if name != "" {
		fields = append(fields, field{"name", name})
	}
	if showStatus != 0 {
		fields = append(fields, field{"show_status", showStatus})
	}
	if sort != 0 {
		fields = append(fields, field{"sort", sort})
	}
	if icon != "" {
		fields = append(fields, field{"icon", icon})
	}

	if len(fields) == 0 {
		return fmt.Errorf("no fields to update")
	}

	query := "UPDATE category SET "
	args := make([]interface{}, 0, len(fields)+1)
	for i, f := range fields {
		if i > 0 {
			query += ", "
		}
		query += f.col + " = ?"
		args = append(args, f.val)
	}
	query += " WHERE cat_id = ? AND deleted_at IS NULL"
	args = append(args, catId)

	result, err := d.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update category: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update category: failed to get rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("category not found or already deleted (cat_id=%d)", catId)
	}

	return nil
}

// DeleteCategory 软删除分类（级联删除所有子分类）
// 使用 CTE 找出所有子孙节点，一次 UPDATE 搞定
func (d *CategoryDal) DeleteCategory(ctx context.Context, catId int64) error {
	if catId == 0 {
		return fmt.Errorf("cat_id is required")
	}

	const query = `
		UPDATE category
		SET deleted_at = UTC_TIMESTAMP()
		WHERE deleted_at IS NULL
		  AND (
		      cat_id = ?
		      OR cat_id IN (
		          WITH RECURSIVE sub_tree AS (
		              SELECT cat_id FROM category WHERE parent_cid = ? AND deleted_at IS NULL
		              UNION ALL
		              SELECT c.cat_id FROM category c
		              INNER JOIN sub_tree st ON c.parent_cid = st.cat_id
		              WHERE c.deleted_at IS NULL
		          )
		          SELECT cat_id FROM sub_tree
		      )
		  )
	`

	result, err := d.db.ExecContext(ctx, query, catId, catId)
	if err != nil {
		return fmt.Errorf("delete category: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete category: failed to get rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("category not found or already deleted (cat_id=%d)", catId)
	}

	logger.Infof("category soft-deleted: cat_id=%d, total_affected=%d", catId, n)
	return nil
}

func scanCategoryNodes(rows *sql.Rows) ([]*v1.CategoryNode, error) {
	var nodes []*v1.CategoryNode
	for rows.Next() {
		item := &v1.CategoryNode{}
		var icon *string
		if err := rows.Scan(
			&item.CatId,
			&item.Name,
			&item.ParentCid,
			&item.CatLevel,
			&item.ShowStatus,
			&item.Sort,
			&icon,
		); err != nil {
			logger.Errorf("categoryRepo scan nodes failed: %v", err)
			return nil, fmt.Errorf("scan category nodes failed: %w", err)
		}
		if icon != nil {
			item.Icon = *icon
		}
		nodes = append(nodes, item)
	}
	if err := rows.Err(); err != nil {
		logger.Errorf("categoryRepo nodes rows error: %v", err)
		return nil, fmt.Errorf("category nodes rows error: %w", err)
	}
	if nodes == nil {
		nodes = []*v1.CategoryNode{}
	}
	return nodes, nil
}
