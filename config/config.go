// Package config 提供配置文件加载功能。
//
// 支持从 YAML 文件加载配置，文件不存在时使用内置默认值。
// 配置文件路径可以通过 CONFIG_PATH 环境变量覆盖。
package config

import (
	"fmt"
	"os"
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
}

// ServerConfig 存放 HTTP 服务器相关配置。
type ServerConfig struct {
	Port int `yaml:"port"` // 监听端口，默认 8080
}

// WebSocketConfig 存放 WebSocket 连接参数。
type WebSocketConfig struct {
	WriteWaitSeconds    int `yaml:"write_wait_seconds"`    // 写超时（秒）
	PongWaitSeconds     int `yaml:"pong_wait_seconds"`     // 等 Pong 超时（秒）
	MaxMessageSize      int `yaml:"max_message_size"`      // 单条消息最大字节数
	BroadcastBufferSize int `yaml:"broadcast_buffer_size"` // Room.broadcast 通道缓冲区大小
	SendBufferSize      int `yaml:"send_buffer_size"`      // Client.send 通道缓冲区大小
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

// Load 从指定路径加载 YAML 配置文件。
// 如果文件不存在，返回默认配置（不会报错）。
// 如果文件存在但解析失败，返回错误。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
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
	}
}
