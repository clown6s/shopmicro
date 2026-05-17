package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"shopmicro/user/module"

	"shopmicro/pkg/logger"
)

type UserDal struct {
	db *sql.DB
}

// UserUpdate 保存用于更新用户记录的类型化字段。
// 零值字段将被跳过（不包含在 UPDATE 语句中）。
type UserUpdate struct {
	RealName *string
	Avatar   *string
	Gender   *int8
	Age      *int16
	Phone    *string
	Email    *string
	Remark   *string
}

// SetRealName 是一个便捷方法，如果值非空则设置 RealName。
func (u *UserUpdate) SetRealName(v string) *UserUpdate {
	u.RealName = &v
	return u
}

// SetAvatar 是一个便捷方法。
func (u *UserUpdate) SetAvatar(v string) *UserUpdate {
	u.Avatar = &v
	return u
}

// SetGender 是一个便捷方法。
func (u *UserUpdate) SetGender(v int8) *UserUpdate {
	u.Gender = &v
	return u
}

// SetAge 是一个便捷方法。
func (u *UserUpdate) SetAge(v int16) *UserUpdate {
	u.Age = &v
	return u
}

// SetPhone 是一个便捷方法。
func (u *UserUpdate) SetPhone(v string) *UserUpdate {
	u.Phone = &v
	return u
}

// SetEmail 是一个便捷方法。
func (u *UserUpdate) SetEmail(v string) *UserUpdate {
	u.Email = &v
	return u
}

// SetRemark 是一个便捷方法。
func (u *UserUpdate) SetRemark(v string) *UserUpdate {
	u.Remark = &v
	return u
}

// Fields 返回非 nil 的更新条目作为 map，用于向后兼容。
// 这允许渐进式迁移，无需一次性修改所有调用点。
func (u *UserUpdate) Fields() map[string]interface{} {
	m := make(map[string]interface{})
	if u.RealName != nil {
		m["real_name"] = *u.RealName
	}
	if u.Avatar != nil {
		m["avatar"] = *u.Avatar
	}
	if u.Gender != nil {
		m["gender"] = *u.Gender
	}
	if u.Age != nil {
		m["age"] = *u.Age
	}
	if u.Phone != nil {
		m["phone"] = *u.Phone
	}
	if u.Email != nil {
		m["email"] = *u.Email
	}
	if u.Remark != nil {
		m["remark"] = *u.Remark
	}
	return m
}

func (d *UserDal) Create(ctx context.Context, user *module.User) error {
	result, err := d.db.ExecContext(ctx,
		`INSERT INTO user (password, real_name, avatar, gender, age, phone, email, remark)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		user.Password, user.RealName, user.Avatar, user.Gender, user.Age,
		user.Phone, user.Email, user.Remark,
	)
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		logger.Warnf("create user: LastInsertId failed: %v", err)
		user.ID = 0
	} else {
		user.ID = id
	}
	return nil
}

func (d *UserDal) GetByID(ctx context.Context, id int64) (*module.User, error) {
	return scanUser(d.db.QueryRowContext(ctx,
		`SELECT id, password, COALESCE(real_name,''), COALESCE(avatar,''), gender, COALESCE(age,0),
		        phone, COALESCE(email,''), create_time, update_time, COALESCE(remark,''), COALESCE(deleted_at,'')
		 FROM user WHERE id = ? AND deleted_at IS NULL`, id,
	))
}

func (d *UserDal) GetByPhone(ctx context.Context, phone string) (*module.User, error) {
	return scanUser(d.db.QueryRowContext(ctx,
		`SELECT id, password, COALESCE(real_name,''), COALESCE(avatar,''), gender, COALESCE(age,0),
		        phone, COALESCE(email,''), create_time, update_time, COALESCE(remark,''), COALESCE(deleted_at,'')
		 FROM user WHERE phone = ? AND deleted_at IS NULL`, phone,
	))
}

// Update 将给定的更新应用到指定 id 的用户。
// 内部转换为 map 保留了白名单/黑名单安全性。
func (d *UserDal) Update(ctx context.Context, id int64, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}

	setParts := make([]string, 0, len(updates))
	args := make([]interface{}, 0, len(updates)+1)
	allowedCols := map[string]bool{
		"real_name": true,
		"avatar":    true,
		"gender":    true,
		"age":       true,
		"phone":     true,
		"email":     true,
		"remark":    true,
	}
	// P2-02：显式黑名单 — 这些列永远不能通过此方法更新
	blockedCols := map[string]bool{
		"id":         true,
		"password":   true,
		"deleted_at": true,
	}
	for k, v := range updates {
		if blockedCols[k] {
			logger.Warnf("update: blocked column %s ignored (user_id in request)", k)
			continue
		}
		if !allowedCols[k] {
			continue
		}
		setParts = append(setParts, fmt.Sprintf("`%s` = ?", k))
		args = append(args, v)
	}
	if len(setParts) == 0 {
		return nil
	}

	args = append(args, id)
	query := fmt.Sprintf("UPDATE user SET %s WHERE id = ? AND deleted_at IS NULL", strings.Join(setParts, ", "))
	result, err := d.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (d *UserDal) Delete(ctx context.Context, id int64) error {
	// 使用 MySQL 的 UTC_TIMESTAMP() 确保 UTC 时区一致性，不受
	// 服务器本地时区设置影响。避免 Go 字符串格式化不匹配的问题。
	result, err := d.db.ExecContext(ctx,
		`UPDATE user SET deleted_at = UTC_TIMESTAMP() WHERE id = ? AND deleted_at IS NULL`, id,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// escapeLike 转义用户输入中的 LIKE 通配符字符。
// 如果不转义，用户可以通过注入 % 或 _ 来匹配任意模式。
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func (d *UserDal) List(ctx context.Context, page, pageSize int32, name, phone string) ([]*module.User, int64, error) {
	if page <= 0 {
		page = 1
	}
	// P2-SEC-05 修复：将 pageSize 上限降低到 20，防止数据遍历攻击
	if pageSize <= 0 || pageSize > 20 {
		pageSize = 20
	}

	conditions := []string{"deleted_at IS NULL"}
	args := make([]interface{}, 0, 2)
	if name != "" {
		conditions = append(conditions, "real_name LIKE ?")
		args = append(args, "%"+escapeLike(name)+"%")
	}
	if phone != "" {
		conditions = append(conditions, "phone LIKE ?")
		args = append(args, "%"+escapeLike(phone)+"%")
	}
	where := strings.Join(conditions, " AND ")

	var total int64
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM user WHERE %s", where)
	if err := d.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := int64(page-1) * int64(pageSize)
	querySQL := fmt.Sprintf(
		`SELECT id, COALESCE(real_name,''), COALESCE(avatar,''), gender, COALESCE(age,0),
		        phone, COALESCE(email,''), create_time, update_time, COALESCE(remark,'')
		 FROM user WHERE %s
		 ORDER BY id DESC LIMIT ? OFFSET ?`, where)
	queryArgs := make([]interface{}, len(args), len(args)+2)
	copy(queryArgs, args)
	queryArgs = append(queryArgs, pageSize, offset)
	rows, err := d.db.QueryContext(ctx, querySQL, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	users := make([]*module.User, 0)
	for rows.Next() {
		u := &module.User{}
		if err := rows.Scan(&u.ID, &u.RealName, &u.Avatar, &u.Gender, &u.Age,
			&u.Phone, &u.Email, &u.CreateTime, &u.UpdateTime, &u.Remark); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

// scanUser 扫描一行用户记录
func scanUser(row *sql.Row) (*module.User, error) {
	u := &module.User{}
	err := row.Scan(&u.ID, &u.Password, &u.RealName, &u.Avatar, &u.Gender, &u.Age,
		&u.Phone, &u.Email, &u.CreateTime, &u.UpdateTime, &u.Remark, &u.DeletedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	return u, nil
}
