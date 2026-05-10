package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"shopmicro/user/model"
)

type UserDal struct {
	db *sql.DB
}

func (d *UserDal) Create(ctx context.Context, user *model.User) error {
	result, err := d.db.ExecContext(ctx,
		`INSERT INTO user (password, real_name, avatar, gender, age, phone, email, remark)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		user.Password, user.RealName, user.Avatar, user.Gender, user.Age,
		user.Phone, user.Email, user.Remark,
	)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	user.ID = id
	return nil
}

func (d *UserDal) GetByID(ctx context.Context, id int64) (*model.User, error) {
	return scanUser(d.db.QueryRowContext(ctx,
		`SELECT id, password, COALESCE(real_name,''), COALESCE(avatar,''), gender, COALESCE(age,0),
		        phone, COALESCE(email,''), create_time, update_time, COALESCE(remark,''), COALESCE(deleted_at,'')
		 FROM user WHERE id = ? AND deleted_at IS NULL`, id,
	))
}

func (d *UserDal) GetByPhone(ctx context.Context, phone string) (*model.User, error) {
	return scanUser(d.db.QueryRowContext(ctx,
		`SELECT id, password, COALESCE(real_name,''), COALESCE(avatar,''), gender, COALESCE(age,0),
		        phone, COALESCE(email,''), create_time, update_time, COALESCE(remark,''), COALESCE(deleted_at,'')
		 FROM user WHERE phone = ? AND deleted_at IS NULL`, phone,
	))
}

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
	for k, v := range updates {
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
	now := time.Now().Format("2006-01-02 15:04:05")
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

func (d *UserDal) List(ctx context.Context, page, pageSize int32, name, phone string) ([]*model.User, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
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
	// P1-08: List query does NOT select password column — no need to load hashes into memory
	querySQL := fmt.Sprintf(
		`SELECT id, COALESCE(real_name,''), COALESCE(avatar,''), gender, COALESCE(age,0),
		        phone, COALESCE(email,''), create_time, update_time, COALESCE(remark,'')
		 FROM user WHERE %s
		 ORDER BY id DESC LIMIT ? OFFSET ?`, where)
	queryArgs := append(args, pageSize, offset)
	rows, err := d.db.QueryContext(ctx, querySQL, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	users := make([]*model.User, 0)
	for rows.Next() {
		u := &model.User{}
		if err := rows.Scan(&u.ID, &u.RealName, &u.Avatar, &u.Gender, &u.Age,
			&u.Phone, &u.Email, &u.CreateTime, &u.UpdateTime, &u.Remark); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

// scanUser 扫描一行用户记录
func scanUser(row *sql.Row) (*model.User, error) {
	u := &model.User{}
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
