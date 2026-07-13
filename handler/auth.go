package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/service"
)

// AuthHandler 处理用户认证相关的 HTTP 请求。
type AuthHandler struct {
	authService *service.AuthService
}

// NewAuthHandler 创建 AuthHandler。
func NewAuthHandler(authService *service.AuthService) *AuthHandler {
	return &AuthHandler{authService: authService}
}

// registerRequest 注册请求体。
type registerRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Nickname string `json:"nickname"`
}

// loginRequest 登录请求体。
type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// authResponse 认证成功后的响应体。
type authResponse struct {
	Token string      `json:"token"`
	User  *model.User `json:"user"`
}

// Register 处理用户注册。
func (h *AuthHandler) Register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user, token, err := h.authService.Register(req.Username, req.Password, req.Nickname)
	if err != nil {
		if errors.Is(err, service.ErrUsernameTaken) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, service.ErrInvalidUsername) || errors.Is(err, service.ErrWeakPassword) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "registration failed"})
		return
	}

	c.JSON(http.StatusCreated, authResponse{Token: token, User: user})
}

// Login 处理用户登录。
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user, token, err := h.authService.Login(req.Username, req.Password)
	if err != nil {
		if errors.Is(err, service.ErrInvalidCreds) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "login failed"})
		return
	}

	c.JSON(http.StatusOK, authResponse{Token: token, User: user})
}

// Me 返回当前登录用户的信息。
// 由 AuthMiddleware 保护，仅携带有效 JWT 才能访问。
func (h *AuthHandler) Me(c *gin.Context) {
	userID, _ := c.Get("user_id")
	username, _ := c.Get("username")
	nickname, _ := c.Get("nickname")

	c.JSON(http.StatusOK, gin.H{
		"user": gin.H{
			"id":       userID,
			"username": username,
			"nickname": nickname,
		},
	})
}

// AuthMiddleware 验证请求中的 Bearer JWT token。
// 验证通过后将 user_id/username/nickname 注入 gin.Context。
// 不携带 token 或 token 无效时返回 401。
func AuthMiddleware(authService *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractBearerToken(c)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization token"})
			return
		}

		actor, err := authService.ValidateToken(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}

		c.Set("user_id", actor.UserID)
		c.Set("username", actor.Username)
		c.Set("nickname", actor.Nickname)
		c.Next()
	}
}

// OptionalAuthMiddleware 尝试解析 JWT，但不强制要求。
// 已废弃：认证后的弹幕接口不再需要此中间件，POST /danmaku 已改为强制 AuthMiddleware。
// 保留此函数供未来可能的不需要严格鉴权的查询类接口使用。
func OptionalAuthMiddleware(authService *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractBearerToken(c)
		if token == "" {
			token = c.Query("token")
		}
		if token == "" {
			c.Next()
			return
		}
		actor, err := authService.ValidateToken(token)
		if err != nil {
			c.Next()
			return
		}
		c.Set("user_id", actor.UserID)
		c.Set("username", actor.Username)
		c.Set("nickname", actor.Nickname)
		c.Next()
	}
}

// extractBearerToken 从请求头中提取 Bearer token。
func extractBearerToken(c *gin.Context) string {
	auth := c.GetHeader("Authorization")
	_, token, found := strings.Cut(auth, "Bearer ")
	if found {
		return token
	}
	return ""
}

// RegisterAuthRoutes 注册认证相关路由。
func (h *AuthHandler) RegisterAuthRoutes(r *gin.Engine, authService *service.AuthService) {
	auth := r.Group("/api/auth")
	{
		auth.POST("/register", h.Register)
		auth.POST("/login", h.Login)
		auth.GET("/me", AuthMiddleware(authService), h.Me)
	}

	// 登录页面
	r.StaticFile("/login", "./templates/login.html")
}
