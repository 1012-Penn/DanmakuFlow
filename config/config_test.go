package config

import (
	"os"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("/nonexistent/path.yaml")
	if err != nil {
		t.Fatalf("Load non-existent should return defaults, got error: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("default port should be 8080, got %d", cfg.Server.Port)
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	os.Setenv("SERVER_PORT", "9090")
	os.Setenv("STORE_DSN", "user:pass@tcp(127.0.0.1:3306)/test")
	os.Setenv("REDIS_ADDR", "redis:6379")
	os.Setenv("REDIS_INSTANCE_ID_PREFIX", "test-instance")
	os.Setenv("LOG_LEVEL", "debug")
	os.Setenv("LOG_FORMAT", "json")
	os.Setenv("PPROF_ENABLED", "true")
	defer func() {
		os.Unsetenv("SERVER_PORT")
		os.Unsetenv("STORE_DSN")
		os.Unsetenv("REDIS_ADDR")
		os.Unsetenv("REDIS_INSTANCE_ID_PREFIX")
		os.Unsetenv("LOG_LEVEL")
		os.Unsetenv("LOG_FORMAT")
		os.Unsetenv("PPROF_ENABLED")
	}()

	cfg := applyEnvOverrides(Default())

	if cfg.Server.Port != 9090 {
		t.Errorf("SERVER_PORT override failed: got %d", cfg.Server.Port)
	}
	if cfg.Store.DSN != "user:pass@tcp(127.0.0.1:3306)/test" {
		t.Errorf("STORE_DSN override failed: got %q", cfg.Store.DSN)
	}
	if cfg.Redis.Addr != "redis:6379" {
		t.Errorf("REDIS_ADDR override failed: got %q", cfg.Redis.Addr)
	}
	if cfg.Redis.InstanceID != "test-instance" {
		t.Errorf("REDIS_INSTANCE_ID_PREFIX override failed: got %q", cfg.Redis.InstanceID)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("LOG_LEVEL override failed: got %q", cfg.Log.Level)
	}
	if cfg.Log.Format != "json" {
		t.Errorf("LOG_FORMAT override failed: got %q", cfg.Log.Format)
	}
	if !cfg.Pprof.Enabled {
		t.Error("PPROF_ENABLED override failed")
	}
}

func TestEmptyEnvNoOverride(t *testing.T) {
	// 确保未设置的环境变量不会覆盖默认值
	cfg := applyEnvOverrides(Default())
	if cfg.Server.Port != 8080 {
		t.Errorf("unset env should not override port: got %d", cfg.Server.Port)
	}
	if cfg.Store.DSN != "" {
		t.Errorf("unset env should not override DSN: got %q", cfg.Store.DSN)
	}
}

func TestKafkaEnvOverrides(t *testing.T) {
	os.Setenv("KAFKA_BROKERS", "broker1:9092,broker2:9092")
	defer os.Unsetenv("KAFKA_BROKERS")

	cfg := applyEnvOverrides(Default())

	if len(cfg.Kafka.Brokers) != 2 {
		t.Fatalf("KAFKA_BROKERS override failed: expected 2 brokers, got %d", len(cfg.Kafka.Brokers))
	}
	if cfg.Kafka.Brokers[0] != "broker1:9092" {
		t.Errorf("broker[0] = %q, expected %q", cfg.Kafka.Brokers[0], "broker1:9092")
	}
	if cfg.Kafka.Brokers[1] != "broker2:9092" {
		t.Errorf("broker[1] = %q, expected %q", cfg.Kafka.Brokers[1], "broker2:9092")
	}
}

func TestKafkaDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Kafka.Brokers != nil {
		t.Errorf("default brokers should be nil, got %v", cfg.Kafka.Brokers)
	}
	if cfg.Kafka.Topic != "danmaku_events" {
		t.Errorf("default topic = %q, expected %q", cfg.Kafka.Topic, "danmaku_events")
	}
	if cfg.Kafka.ConsumerGroup != "danmakuflow-danmaku-persist" {
		t.Errorf("default consumer_group = %q", cfg.Kafka.ConsumerGroup)
	}
}

func TestInvalidPortFallsBack(t *testing.T) {
	os.Setenv("SERVER_PORT", "not-a-number")
	defer os.Unsetenv("SERVER_PORT")

	cfg := applyEnvOverrides(Default())
	if cfg.Server.Port != 8080 {
		t.Errorf("invalid port should fall back to default: got %d", cfg.Server.Port)
	}
}
