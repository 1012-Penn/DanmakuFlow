// Package config 提供配置文件加载功能。
//
// 支持从 YAML 文件加载配置，文件不存在时使用内置默认值。
// 配置文件路径可以通过 CONFIG_PATH 环境变量覆盖。
// 支持环境变量覆盖 YAML 中的对应字段（环境变量优先级更高）。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 是应用配置的根结构。
// yaml tag 指定配置文件中对应的字段名。
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	WebSocket WebSocketConfig `yaml:"websocket"`
	Store     StoreConfig     `yaml:"store"`
	Log       LogConfig       `yaml:"log"`
	Redis     RedisConfig     `yaml:"redis"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
	Pprof     PprofConfig     `yaml:"pprof"`
	Auth      AuthConfig      `yaml:"auth"`
}

// AuthConfig 存放 JWT 认证相关配置。
type AuthConfig struct {
	// JWTSecret 是 JWT 签名的密钥。
	// 空字符串时使用内置默认密钥（仅限开发环境）。
	JWTSecret string `yaml:"jwt_secret"`
	// TokenExpiryHours 是 JWT 过期时间（小时），默认 72。
	TokenExpiryHours int `yaml:"token_expiry_hours"`
}

// ServerConfig 存放 HTTP 服务器相关配置。
type ServerConfig struct {
	Port int `yaml:"port"` // 监听端口，默认 8080
}

// WebSocketConfig 存放 WebSocket 连接参数。
type WebSocketConfig struct {
	WriteWaitSeconds    int      `yaml:"write_wait_seconds"`    // 写超时（秒）
	PongWaitSeconds     int      `yaml:"pong_wait_seconds"`     // 等 Pong 超时（秒）
	MaxMessageSize      int      `yaml:"max_message_size"`      // 单条消息最大字节数
	BroadcastBufferSize int      `yaml:"broadcast_buffer_size"` // Room.broadcast 通道缓冲区大小
	SendBufferSize      int      `yaml:"send_buffer_size"`      // Client.send 通道缓冲区大小
	MaxConnPerRoom      int      `yaml:"max_conn_per_room"`     // 每房间最大连接数，0=不限制
	MaxConnPerIP        int      `yaml:"max_conn_per_ip"`       // 每 IP 最大连接数，0=不限制
	AllowedOrigins      []string `yaml:"allowed_origins"`       // 允许的 Origin，空=不校验
}

// PprofConfig 存放 pprof 性能分析配置。
type PprofConfig struct {
	Enabled bool `yaml:"enabled"` // 是否开启 pprof 端点，默认 false
}

// RateLimitConfig 存放频率限制相关配置。
type RateLimitConfig struct {
	MessagesPerSec float64 `yaml:"messages_per_sec"` // 每用户每秒可发送弹幕数，0=不限制
}

// LogConfig 存放日志相关配置。
type LogConfig struct {
	Level  string `yaml:"level"`  // debug / info / warn / error
	Format string `yaml:"format"` // text（开发用）/ json（生产用）
}

// ResolveLevel 将配置中的字符串 Level 转换为 slog 可用的级别。
// 返回值可用于 slog.HandlerOptions.Level。
func (l LogConfig) ResolveLevel() (int, error) {
	switch strings.ToLower(l.Level) {
	case "debug":
		return -4, nil // slog.LevelDebug
	case "info":
		return 0, nil // slog.LevelInfo
	case "warn":
		return 4, nil // slog.LevelWarn
	case "error":
		return 8, nil // slog.LevelError
	default:
		return 0, fmt.Errorf("不支持的日志级别: %s（可选: debug/info/warn/error）", l.Level)
	}
}

// StoreConfig 存放存储层相关配置。
type StoreConfig struct {
	DefaultListLimit int    `yaml:"default_list_limit"` // List/ListByRoom 默认返回条数
	DSN              string `yaml:"dsn"`                // MySQL DSN，空值表示用 MemoryStore
	AsyncBufferSize  int    `yaml:"async_buffer_size"`  // 异步写入通道缓冲区大小（0=同步写）
}

// RedisConfig 存放 Redis 连接配置。
type RedisConfig struct {
	Addr       string `yaml:"addr"`        // Redis 地址，如 "localhost:6379"。空 = 不使用 Redis
	InstanceID string `yaml:"instance_id"` // 实例标识（用于去重）。空 = 自动生成
}

// Load 从指定路径加载 YAML 配置文件，再应用环境变量覆盖。
// 如果文件不存在，返回默认配置（不会报错）。
// 如果文件存在但解析失败，返回错误。
// 环境变量优先级高于 YAML 配置。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return applyEnvOverrides(Default()), nil
		}
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return applyEnvOverrides(&cfg), nil
}

// applyEnvOverrides 用环境变量覆盖配置中的对应字段。
// 环境变量为空时不覆盖。所有环境变量均为可选。
//
// 支持的环境变量：
//
//	SERVER_PORT               — 服务端口
//	STORE_DSN                 — MySQL DSN（空 = 使用 MemoryStore）
//	REDIS_ADDR                — Redis 地址（空 = 不使用 Redis）
//	REDIS_INSTANCE_ID_PREFIX  — 实例 ID 前缀（空 = 自动生成）
//	LOG_LEVEL                 — 日志级别（debug/info/warn/error）
//	LOG_FORMAT                — 日志格式（text/json）
//	PPROF_ENABLED             — 是否开启 pprof（true/false，默认 false）
//	JWT_SECRET                — JWT 签名密钥（空=使用内置默认密钥）
func applyEnvOverrides(cfg *Config) *Config {
	if v := os.Getenv("SERVER_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = port
		}
	}
	if v := os.Getenv("STORE_DSN"); v != "" {
		cfg.Store.DSN = v
	}
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		cfg.Redis.Addr = v
	}
	if v := os.Getenv("REDIS_INSTANCE_ID_PREFIX"); v != "" {
		cfg.Redis.InstanceID = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("LOG_FORMAT"); v != "" {
		cfg.Log.Format = v
	}
	if v := os.Getenv("PPROF_ENABLED"); v != "" {
		if enabled, err := strconv.ParseBool(v); err == nil {
			cfg.Pprof.Enabled = enabled
		}
	}
	if v := os.Getenv("JWT_SECRET"); v != "" {
		cfg.Auth.JWTSecret = v
	}
	return cfg
}

// Default 返回一份内置默认配置。
// 确保没有配置文件时应用也能正常运行。
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Port: 8080,
		},
		WebSocket: WebSocketConfig{
			WriteWaitSeconds:    10,
			PongWaitSeconds:     60,
			MaxMessageSize:      512,
			BroadcastBufferSize: 256,
			SendBufferSize:      256,
			MaxConnPerRoom:      0, // 0 = 不限制
			MaxConnPerIP:        0, // 0 = 不限制
			AllowedOrigins:      nil,
		},
		Store: StoreConfig{
			DefaultListLimit: 20,
			DSN:              "",
			AsyncBufferSize:  1024,
		},
		Log: LogConfig{
			Level:  "info",
			Format: "text",
		},
		Redis: RedisConfig{
			Addr:       "", // 空 = 不使用 Redis
			InstanceID: "",
		},
		RateLimit: RateLimitConfig{
			MessagesPerSec: 0, // 0 = 不限制
		},
		Auth: AuthConfig{
			JWTSecret:        "", // 空 = main.go 使用内置默认密钥
			TokenExpiryHours: 72, // 72 小时
		},
	}
}
