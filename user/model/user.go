package model

import "time"

type User struct {
	ID         int64     `json:"id"`
	Password   string    `json:"-"`
	RealName   string    `json:"real_name"`
	Avatar     string    `json:"avatar"`
	Gender     int8      `json:"gender"`
	Age        int16     `json:"age"`
	Phone      string    `json:"phone"`
	Email      string    `json:"email"`
	CreateTime time.Time `json:"create_time"`
	UpdateTime time.Time `json:"update_time"`
	Remark     string    `json:"remark"`
	DeletedAt  string    `json:"-"`
}
