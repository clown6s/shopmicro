package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"shopmicro/user/module"

	"shopmicro/pkg/logger"
)

type UserDal struct {
	db *sql.DB
}

// UserUpdate holds typed fields for updating a user record.
// Zero-value fields are skipped (not included in the UPDATE statement).
type UserUpdate struct {
	RealName *string
	Avatar   *string
	Gender   *int8
	Age      *int16
	Phone    *string
	Email    *string
	Remark   *string
}

// SetRealName is a convenience helper that sets RealName if value is non-empty.
func (u *UserUpdate) SetRealName(v string) *UserUpdate {
	u.RealName = &v
	return u
}

// SetAvatar is a convenience helper.
func (u *UserUpdate) SetAvatar(v string) *UserUpdate {
	u.Avatar = &v
	return u
}

// SetGender is a convenience helper.
func (u *UserUpdate) SetGender(v int8) *UserUpdate {
	u.Gender = &v
	return u
}

// SetAge is a convenience helper.
func (u *UserUpdate) SetAge(v int16) *UserUpdate {
	u.Age = &v
	return u
}

// SetPhone is a convenience helper.
func (u *UserUpdate) SetPhone(v string) *UserUpdate {
	u.Phone = &v
	return u
}

// SetEmail is a convenience helper.
func (u *UserUpdate) SetEmail(v string) *UserUpdate {
	u.Email = &v
	return u
}

// SetRemark is a convenience helper.
func (u *UserUpdate) SetRemark(v string) *UserUpdate {
	u.Remark = &v
	return u
}

// Fields returns the non-nil update entries as a map for backward compatibility.
// This allows gradual migration without changing all call sites at once.
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

// Update applies the given updates to the user with the given id.
// The internal conversion to map preserves the whitelist/blocklist safety.
func (d *UserDal) Update(ctx context.Context, id int64, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}

	setParts := make([]string, 0, len(updates))
	args := make([]interface{}, 0, len(updates)+1)
	colMap := map[string]string{
		"real_name": "real_name",
		"avatar":    "avatar",
		"gender":    "gender",
		"age":       "age",
		"phone":     "phone",
		"email":     "email",
		"remark":    "remark",
	}
	// P2-02: Explicit blocklist — these columns can NEVER be updated via this method
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
		col, ok := colMap[k]
		if !ok {
			continue
		}
		setParts = append(setParts, fmt.Sprintf("`%s` = ?", col))
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
	// R4-SEC-05: 使用 UTC 时间避免时区问题，确保数据库与服务器时区一致
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	result, err := d.db.ExecContext(ctx,
		`UPDATE user SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, now, id,
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

func (d *UserDal) List(ctx context.Context, page, pageSize int32, name, phone string) ([]*module.User, int64, error) {
	if page <= 0 {
		page = 1
	}
	// P2-SEC-05 fix: 降低 pageSize 上限到 20，防止数据遍历攻击
	if pageSize <= 0 || pageSize > 20 {
		pageSize = 20
	}

	conditions := []string{"deleted_at IS NULL"}
	args := make([]interface{}, 0, 2)
	if name != "" {
		conditions = append(conditions, "real_name LIKE ?")
		args = append(args, "%"+name+"%")
	}
	if phone != "" {
		conditions = append(conditions, "phone LIKE ?")
		args = append(args, "%"+phone+"%")
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
