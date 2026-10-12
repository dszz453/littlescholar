package util

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 业务错误码
const (
	CodeSuccess      = 0
	CodeBadRequest   = 400
	CodeUnauthorized = 401
	CodeForbidden    = 403
	CodeNotFound     = 404
	CodeTooMany      = 429
	CodeServerError  = 500
)

// Response 统一响应结构
type Response struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

// PageData 分页数据
type PageData struct {
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"pageSize"`
	List     interface{} `json:"list"`
}

// OK 成功响应
func OK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{Code: CodeSuccess, Message: "success", Data: data})
}

// OKWithMessage 成功响应带消息
func OKWithMessage(c *gin.Context, message string, data interface{}) {
	c.JSON(http.StatusOK, Response{Code: CodeSuccess, Message: message, Data: data})
}

// Page 分页响应
func Page(c *gin.Context, total int64, page, pageSize int, list interface{}) {
	OK(c, PageData{Total: total, Page: page, PageSize: pageSize, List: list})
}

// Fail 失败响应（HTTP 状态码始终 200，业务码放 body，便于前端统一处理）
func Fail(c *gin.Context, code int, message string) {
	c.JSON(http.StatusOK, Response{Code: code, Message: message, Data: nil})
	c.Abort()
}

// BadRequest 参数错误
func BadRequest(c *gin.Context, message string) {
	if message == "" {
		message = "参数错误"
	}
	Fail(c, CodeBadRequest, message)
}

// Unauthorized 未登录
func Unauthorized(c *gin.Context, message string) {
	if message == "" {
		message = "未登录或登录已失效"
	}
	Fail(c, CodeUnauthorized, message)
}

// Forbidden 无权限
func Forbidden(c *gin.Context, message string) {
	if message == "" {
		message = "无访问权限"
	}
	Fail(c, CodeForbidden, message)
}

// NotFound 不存在
func NotFound(c *gin.Context, message string) {
	if message == "" {
		message = "资源不存在"
	}
	Fail(c, CodeNotFound, message)
}

// ServerError 服务器错误
func ServerError(c *gin.Context, message string) {
	if message == "" {
		message = "服务器内部错误"
	}
	Fail(c, CodeServerError, message)
}
