package model

import (
	"fmt"
	"strings"
)

// Category 目录（自引用树 + 物化路径）
// 支持任意层级：乐学英语 / 新概念英语 / 第一册 ...
type Category struct {
	Base
	ParentID    *uint64 `gorm:"index" json:"parentId"` // nil = 根
	Name        string  `gorm:"size:100;not null" json:"name"`
	Icon        string  `gorm:"size:200" json:"icon"`        // 图标标识，如 english
	IconURL     string  `gorm:"size:500" json:"iconUrl"`     // 自定义图标 URL
	Color       string  `gorm:"size:30" json:"color"`        // 主题色，子供儿童化
	Description string  `gorm:"size:500" json:"description"`
	SortOrder   int     `gorm:"default:0" json:"sortOrder"`
	Level       int     `gorm:"default:1" json:"level"`
	Path        string  `gorm:"size:500;index" json:"path"` // 物化路径 /1/12/35/
	IsPublic    bool    `gorm:"default:false" json:"isPublic"` // 公开目录无需授权

	Children []*Category `gorm:"-" json:"children,omitempty"`

	// 非持久化字段
	ResourceCount int64 `gorm:"-" json:"resourceCount"`
}

func (Category) TableName() string { return "categories" }

// BuildPath 构建物化路径
// 根节点：/1/
// 子节点：/1/12/
func BuildPath(parentPath string, id uint64) string {
	if parentPath == "" {
		return fmt.Sprintf("/%d/", id)
	}
	return fmt.Sprintf("%s%d/", parentPath, id)
}

// PathPrefixToChildren 返回用于查询所有子节点的 LIKE 前缀
func (c *Category) PathPrefixToChildren() string {
	return c.Path + "%"
}

// DepthFromPath 由路径推导层级
func DepthFromPath(path string) int {
	if path == "" {
		return 0
	}
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "/"))
}

// BuildTree 把扁平列表构建成树
func BuildTree(list []*Category, parentID *uint64) []*Category {
	var out []*Category
	for _, c := range list {
		if samePtr(c.ParentID, parentID) {
			c.Children = BuildTree(list, &c.ID)
			out = append(out, c)
		}
	}
	return out
}

func samePtr(a, b *uint64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
