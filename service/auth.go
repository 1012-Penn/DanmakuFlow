// Package service 提供弹幕系统的业务逻辑层。
package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
)

// AuthClaims 是 JWT token 中携带的完整声明（含 jwt 标准字段）。
type AuthClaims struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	jwt.RegisteredClaims
}

// AuthService 提供用户注册、登录和 JWT 验证功能。
type AuthService struct {
	userStore   store.UserStore
	jwtSecret   []byte
	expiryHours int
}

// NewAuthService 创建 AuthService。
// jwtSecret 为空时使用内置默认密钥（仅限开发环境）。
func NewAuthService(userStore store.UserStore, jwtSecret string, expiryHours int) *AuthService {
	secret := jwtSecret
	if secret == "" {
		secret = "danmakuflow-dev-secret-do-not-use-in-production"
	}
	if expiryHours <= 0 {
		expiryHours = 72
	}
	return &AuthService{
		userStore:   userStore,
		jwtSecret:   []byte(secret),
		expiryHours: expiryHours,
	}
}

var (
	ErrUsernameTaken   = errors.New("username already taken")
	ErrInvalidUsername = errors.New("username must be 3-32 characters, letters and digits only")
	ErrWeakPassword    = errors.New("password must be at least 6 characters")
	ErrInvalidCreds    = errors.New("invalid username or password")
	ErrInvalidToken    = errors.New("invalid or expired token")
)

// Register 注册新用户，返回 user 和 JWT token。
func (s *AuthService) Register(username, password, nickname string) (*model.User, string, error) {
	// 验证输入
	username = strings.TrimSpace(username)
	if err := validateUsername(username); err != nil {
		return nil, "", err
	}
	if len(password) < 6 {
		return nil, "", ErrWeakPassword
	}
	if len(password) > 72 {
		// bcrypt 只处理前 72 字节，超过时静默截断可能导致逻辑错误
		return nil, "", errors.New("password must not exceed 72 characters")
	}
	nickname = strings.TrimSpace(nickname)
	if nickname == "" {
		nickname = username
	}
	if len(nickname) > 32 {
		nickname = nickname[:32]
	}

	// bcrypt 哈希密码
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, "", fmt.Errorf("password hash: %w", err)
	}

	user := &model.User{
		ID:           uuid.New().String(),
		Username:     username,
		PasswordHash: string(hash),
		Nickname:     nickname,
	}

	if err := s.userStore.Create(user); err != nil {
		if errors.Is(err, store.ErrDuplicateUsername) {
			return nil, "", ErrUsernameTaken
		}
		return nil, "", fmt.Errorf("create user: %w", err)
	}

	token, err := s.generateToken(user)
	if err != nil {
		return nil, "", err
	}
	return user, token, nil
}

// Login 验证用户名密码，返回 user 和 JWT token。
func (s *AuthService) Login(username, password string) (*model.User, string, error) {
	username = strings.TrimSpace(username)
	user, err := s.userStore.FindByUsername(username)
	if err != nil {
		return nil, "", fmt.Errorf("find user: %w", err)
	}
	if user == nil {
		return nil, "", ErrInvalidCreds
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, "", ErrInvalidCreds
	}

	token, err := s.generateToken(user)
	if err != nil {
		return nil, "", err
	}
	return user, token, nil
}

// ValidateToken 解析并验证 JWT token，返回 Actor。
// 返回 *model.Actor 以匹配 websocket.AuthValidator 接口。
func (s *AuthService) ValidateToken(tokenString string) (*model.Actor, error) {
	token, err := jwt.ParseWithClaims(tokenString, &AuthClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	claims, ok := token.Claims.(*AuthClaims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}
	return &model.Actor{
		UserID:        claims.UserID,
		Username:      claims.Username,
		Nickname:      claims.Nickname,
		Authenticated: true,
	}, nil
}

// generateToken 为用户签发 JWT token（HS256），有效期 72 小时。
func (s *AuthService) generateToken(user *model.User) (string, error) {
	now := time.Now()
	claims := &AuthClaims{
		UserID:   user.ID,
		Username: user.Username,
		Nickname: user.Nickname,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(s.expiryHours) * time.Hour)),
			Issuer:    "danmakuflow",
			Subject:   user.ID,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return tokenString, nil
}

// validateUsername 校验用户名格式：3-32 字符，仅含字母和数字。
func validateUsername(username string) error {
	if len(username) < 3 || len(username) > 32 {
		return ErrInvalidUsername
	}
	for _, r := range username {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return ErrInvalidUsername
		}
	}
	return nil
}
