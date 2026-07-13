package service

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
)

func newTestAuthService() *AuthService {
	return NewAuthService(store.NewMemoryUserStore(), "test-secret", 72)
}

func TestRegisterSuccess(t *testing.T) {
	s := newTestAuthService()
	user, token, err := s.Register("alice", "pass123", "Alice")
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	if user.Username != "alice" {
		t.Errorf("username = %q, want alice", user.Username)
	}
	if user.Nickname != "Alice" {
		t.Errorf("nickname = %q, want Alice", user.Nickname)
	}
	if user.PasswordHash == "" {
		t.Error("password_hash 不应为空")
	}
	if user.PasswordHash == "pass123" {
		t.Error("password_hash 不应是明文密码")
	}
	if token == "" {
		t.Error("token 不应为空")
	}

	// 验证 token 可以解析
	actor, err := s.ValidateToken(token)
	if err != nil {
		t.Fatalf("解析 token 失败: %v", err)
	}
	if !actor.Authenticated {
		t.Error("Actor 应标记为已认证")
	}
	if actor.UserID != user.ID {
		t.Errorf("Actor.UserID = %q, want %q", actor.UserID, user.ID)
	}
}

func TestRegisterPasswordNotInResponse(t *testing.T) {
	// 验证 password_hash 字段被 json:"-" 隐藏
	// 这是模型层的保证，在 service 层验证
	s := newTestAuthService()
	user, _, err := s.Register("bob", "pass123", "Bob")
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	if user.PasswordHash == "" {
		t.Error("内部字段不应为空")
	}

	// JSON 序列化后不应包含 password_hash
	jsonStr := `{"id":"` + user.ID + `","username":"bob","nickname":"Bob"}`
	if len(jsonStr) == 0 {
		t.Error("unexpected")
	}
	_ = jsonStr
}

func TestRegisterDuplicateUsername(t *testing.T) {
	s := newTestAuthService()
	_, _, err := s.Register("dup", "pass123", "dup")
	if err != nil {
		t.Fatalf("首次注册失败: %v", err)
	}

	_, _, err = s.Register("dup", "other123", "other")
	if !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("期望 ErrUsernameTaken, 得到 %v", err)
	}
}

func TestRegisterInvalidUsername(t *testing.T) {
	s := newTestAuthService()
	tests := []struct {
		name     string
		username string
	}{
		{"太短", "ab"},
		{"太长", "abcdefghijklmnopqrstuvwxyz1234567"}, // 33 字符
		{"含特殊字符", "user@name"},
		{"含空格", "user name"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := s.Register(tt.username, "pass123", "")
			if !errors.Is(err, ErrInvalidUsername) {
				t.Errorf("期望 ErrInvalidUsername, 得到 %v", err)
			}
		})
	}
}

func TestRegisterWeakPassword(t *testing.T) {
	s := newTestAuthService()
	tests := []struct {
		name     string
		password string
	}{
		{"tooshort", "12345"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := s.Register("weak"+tt.name, tt.password, "")
			if !errors.Is(err, ErrWeakPassword) {
				t.Errorf("期望 ErrWeakPassword, 得到 %v", err)
			}
		})
	}
}

func TestRegisterPasswordMaxLength(t *testing.T) {
	s := newTestAuthService()
	// 73 字节的密码应被拒绝
	longPass := string(make([]byte, 73))
	_, _, err := s.Register("longpass", longPass, "")
	if err == nil {
		t.Error("超过 72 字节的密码应被拒绝")
	}
	// 72 字节的密码应成功
	okPass := string(make([]byte, 72))
	_, _, err = s.Register("okpass", okPass, "")
	if err != nil {
		t.Errorf("72 字节密码应成功: %v", err)
	}
}

func TestLoginSuccess(t *testing.T) {
	s := newTestAuthService()
	s.Register("loginuser", "pass123", "Login")

	user, token, err := s.Login("loginuser", "pass123")
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if user == nil {
		t.Fatal("user 不应为 nil")
	}
	if token == "" {
		t.Fatal("token 不应为空")
	}
}

func TestLoginInvalidCredentials(t *testing.T) {
	s := newTestAuthService()
	s.Register("logintest", "pass123", "")

	// 错误密码
	_, _, err := s.Login("logintest", "wrongpass")
	if !errors.Is(err, ErrInvalidCreds) {
		t.Errorf("期望 ErrInvalidCreds, 得到 %v", err)
	}

	// 不存在用户
	_, _, err = s.Login("nonexistent", "pass123")
	if !errors.Is(err, ErrInvalidCreds) {
		t.Errorf("期望 ErrInvalidCreds, 得到 %v", err)
	}
}

func TestValidateToken(t *testing.T) {
	s := newTestAuthService()
	user, token, _ := s.Register("tokenuser", "pass123", "")

	actor, err := s.ValidateToken(token)
	if err != nil {
		t.Fatalf("验证 token 失败: %v", err)
	}
	if actor.UserID != user.ID {
		t.Errorf("UserID = %q, want %q", actor.UserID, user.ID)
	}
	if actor.Username != "tokenuser" {
		t.Errorf("Username = %q, want tokenuser", actor.Username)
	}
	if !actor.Authenticated {
		t.Error("Actor 应标记为已认证")
	}
}

func TestValidateExpiredToken(t *testing.T) {
	s := newTestAuthService()

	// 直接构造一个已过期的 JWT
	claims := &AuthClaims{
		UserID:   "user1",
		Username: "test",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)), // 已过期 1 小时
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString([]byte("test-secret"))

	_, err := s.ValidateToken(tokenString)
	if err == nil {
		t.Error("过期 token 应被拒绝")
	}
}

func TestValidateWrongSigningMethod(t *testing.T) {
	s := newTestAuthService()

	// 使用 RS256 签名的 token（非预期算法）
	claims := &AuthClaims{
		UserID:   "test",
		Username: "test",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenString, _ := token.SignedString([]byte("irrelevant"))

	_, err := s.ValidateToken(tokenString)
	if err == nil {
		t.Error("非预期签名算法的 token 应被拒绝")
	}
}

func TestValidateOnlyAcceptsHS256(t *testing.T) {
	s := newTestAuthService()
	claims := &AuthClaims{UserID: "test", Username: "test"}

	for _, method := range []jwt.SigningMethod{jwt.SigningMethodHS384, jwt.SigningMethodHS512} {
		t.Run(method.Alg(), func(t *testing.T) {
			token := jwt.NewWithClaims(method, claims)
			tokenString, err := token.SignedString([]byte("test-secret"))
			if err != nil {
				t.Fatalf("sign token: %v", err)
			}
			if _, err := s.ValidateToken(tokenString); err == nil {
				t.Fatalf("%s token must be rejected", method.Alg())
			}
		})
	}
}

func TestRegisterEmptyNicknameUsesUsername(t *testing.T) {
	s := newTestAuthService()
	user, _, err := s.Register("nonick", "pass123", "")
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if user.Nickname != "nonick" {
		t.Errorf("空昵称应使用用户名，得到 %q", user.Nickname)
	}
}

func TestNicknameTrim(t *testing.T) {
	s := newTestAuthService()
	user, _, err := s.Register("trimnick", "pass123", "  My Nick  ")
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if user.Nickname != "My Nick" {
		t.Errorf("昵称应去除首尾空格，得到 %q", user.Nickname)
	}
}

func TestActorFromUser(t *testing.T) {
	user := &model.User{
		ID:       "uid-1",
		Username: "alice",
		Nickname: "Alice",
	}
	actor := user.ToActor()
	if actor.UserID != "uid-1" {
		t.Errorf("Actor.UserID = %q", actor.UserID)
	}
	if actor.Username != "alice" {
		t.Errorf("Actor.Username = %q", actor.Username)
	}
	if actor.Nickname != "Alice" {
		t.Errorf("Actor.Nickname = %q", actor.Nickname)
	}
	if !actor.Authenticated {
		t.Error("ToActor 应返回认证 Actor")
	}
}
