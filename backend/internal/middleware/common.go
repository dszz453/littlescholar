package middleware

import (
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Recovery 统一 panic 恢复
func Recovery(logger *zap.Logger) gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		logger.Error("panic recovered",
			zap.Any("error", recovered),
			zap.String("path", c.Request.URL.Path),
			zap.String("method", c.Request.Method),
		)
		c.AbortWithStatusJSON(500, gin.H{
			"code":    500,
			"message": "服务器内部错误",
			"data":    nil,
		})
	})
}

// AccessLog 访问日志
func AccessLog(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		status := c.Writer.Status()
		if status >= 400 || c.Request.Method != "GET" {
			logger.Info("request",
				zap.String("method", c.Request.Method),
				zap.String("path", c.Request.URL.Path),
				zap.Int("status", status),
				zap.String("ip", c.ClientIP()),
			)
		}
	}
}
