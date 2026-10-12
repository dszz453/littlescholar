package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
)

// JSONMap 用于存储灵活的 JSON 字段
type JSONMap map[string]interface{}

func (j JSONMap) Value() (driver.Value, error) {
	if j == nil {
		return "{}", nil
	}
	return json.Marshal(j)
}

func (j *JSONMap) Scan(value interface{}) error {
	if value == nil {
		*j = JSONMap{}
		return nil
	}
	var b []byte
	switch v := value.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return errors.New("unsupported type for JSONMap")
	}
	if len(b) == 0 {
		*j = JSONMap{}
		return nil
	}
	return json.Unmarshal(b, j)
}

// StringArray 字符串数组，存 JSON
type StringArray []string

func (s StringArray) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	return json.Marshal(s)
}

func (s *StringArray) Scan(value interface{}) error {
	if value == nil {
		*s = StringArray{}
		return nil
	}
	var b []byte
	switch v := value.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return errors.New("unsupported type for StringArray")
	}
	if len(b) == 0 {
		*s = StringArray{}
		return nil
	}
	return json.Unmarshal(b, s)
}

// Base 通用基础字段
type Base struct {
	ID        uint64         `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}
