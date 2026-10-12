package util

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

// GetUintParam 从路径参数取 uint64
func GetUintParam(c *gin.Context, name string) (uint64, bool) {
	v := c.Param(name)
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// GetUintQuery 从 query 取 uint64
func GetUintQuery(c *gin.Context, name string, def uint64) uint64 {
	v := c.Query(name)
	if v == "" {
		return def
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

// GetIntQuery 从 query 取 int
func GetIntQuery(c *gin.Context, name string, def int) int {
	v := c.Query(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// GetPage 取分页参数
func GetPage(c *gin.Context) (page, pageSize int) {
	page = GetIntQuery(c, "page", 1)
	pageSize = GetIntQuery(c, "pageSize", 20)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	return
}

// CurrentUserID 从上下文取当前用户 ID
func CurrentUserID(c *gin.Context) uint64 {
	if v, ok := c.Get("userID"); ok {
		if id, ok := v.(uint64); ok {
			return id
		}
	}
	return 0
}

// CurrentRole 从上下文取当前角色
func CurrentRole(c *gin.Context) string {
	if v, ok := c.Get("role"); ok {
		if r, ok := v.(string); ok {
			return r
		}
	}
	return ""
}

// IsAdmin 当前是否管理员
func IsAdmin(c *gin.Context) bool {
	return CurrentRole(c) == "admin"
}
