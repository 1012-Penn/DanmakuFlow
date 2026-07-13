// Package redisclient 封装了 go-redis 的 Pub/Sub 操作。
//
// 提供两个核心能力：
//   - Publish: 将弹幕消息发布到房间对应的 Redis 频道
//   - Subscribe: 订阅所有房间频道，接收跨实例广播
//
// 通过 SourceID 机制避免自己发布的消息被自己重复消费。
package redisclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/1012-Penn/DanmakuFlow/metrics"
)

// GenerateInstanceID 生成全局唯一的运行实例标识。
//
// 策略：
//   - prefix 非空时（来自配置）作为可读前缀
//   - 自动追加 PID + UUID 短尾，确保同主机多进程 ID 不同
//   - prefix 为空时使用 hostname
//
// 结果示例：
//   - GenerateInstanceID("")      → "myhost-a1b2c3d4-5678"
//   - GenerateInstanceID("pub")   → "pub-e5f6g7h8-9012"
func GenerateInstanceID(prefix string) string {
	if prefix == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = "unknown"
		}
		prefix = host
	}
	// 清理 prefix 中可能的分隔符
	prefix = strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, prefix)

	pid := os.Getpid()
	uid := uuid.New().String()
	short := uid[:8] // 取 UUID 前 8 个字符

	return fmt.Sprintf("%s-%d-%s", prefix, pid, short)
}

// Message 是 Redis Pub/Sub 中传输的消息结构。
type Message struct {
	SourceID string          `json:"source_id"` // 来源实例 ID，用于去重
	RoomID   string          `json:"room_id"`   // 目标房间 ID
	Data     json.RawMessage `json:"data"`      // 弹幕 JSON 字节
}

// Client 封装了 go-redis 的 Pub/Sub 操作。
// 线程安全——所有 goroutine 共用同一个 *redis.Client。
type Client struct {
	rdb      *redis.Client // Redis 客户端连接
	instance string        // 本实例的唯一标识（启动时生成）
}

// New 创建一个 Redis Pub/Sub 客户端。
// instanceID 用于消息去重——收到自己发布的消息时跳过。
func New(addr string, instanceID string) *Client {
	rdb := redis.NewClient(&redis.Options{
		Addr: addr,
	})
	return &Client{
		rdb:      rdb,
		instance: instanceID,
	}
}

// Publish 将弹幕数据发布到指定房间的 Redis 频道。
// 频道命名规则：room:<roomID>。
// 发布的消息包含来源实例 ID，订阅方据此跳过自己发的消息。
// 记录发布延迟、成功/失败计数等指标。
func (c *Client) Publish(ctx context.Context, roomID string, data []byte) error {
	start := time.Now()
	msg := Message{
		SourceID: c.instance,
		RoomID:   roomID,
		Data:     data,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		metrics.RedisPublishTotal.WithLabelValues("error").Inc()
		return err
	}
	err = c.rdb.Publish(ctx, "room:"+roomID, payload).Err()
	metrics.RedisPublishLatency.Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.RedisPublishTotal.WithLabelValues("error").Inc()
	} else {
		metrics.RedisPublishTotal.WithLabelValues("success").Inc()
	}
	return err
}

// StartSubscription 启动可恢复的 Redis 订阅循环。
// 返回的 channel 在 context 取消或订阅无法恢复时关闭。
// 内部使用指数退避 + jitter 在连接断开后自动重连。
func (c *Client) StartSubscription(ctx context.Context) <-chan Message {
	ch := make(chan Message, 64)
	go c.subscriptionLoop(ctx, ch)
	return ch
}

const (
	subBackoffMin = 100 * time.Millisecond
	subBackoffMax = 30 * time.Second
)

func (c *Client) subscriptionLoop(ctx context.Context, ch chan<- Message) {
	defer close(ch)

	backoff := subBackoffMin
	for {
		pubsub := c.rdb.PSubscribe(ctx, "room:*")
		if _, err := pubsub.Receive(ctx); err != nil {
			_ = pubsub.Close()
			if !c.waitForReconnect(ctx, err, backoff) {
				return
			}
			backoff = growBackoff(backoff)
			continue
		}

		metrics.RedisSubStatus.Set(1)
		connectedAt := time.Now()
		for {
			redisMsg, err := pubsub.ReceiveMessage(ctx)
			if err != nil {
				_ = pubsub.Close()
				if time.Since(connectedAt) >= 30*time.Second {
					backoff = subBackoffMin
				}
				if !c.waitForReconnect(ctx, err, backoff) {
					return
				}
				backoff = growBackoff(backoff)
				break
			}

			var msg Message
			if err := json.Unmarshal([]byte(redisMsg.Payload), &msg); err != nil {
				slog.Warn("Redis 消息解析失败", "error", err)
				continue
			}
			if msg.SourceID == c.instance {
				continue
			}
			select {
			case ch <- msg:
			case <-ctx.Done():
				metrics.RedisSubStatus.Set(0)
				_ = pubsub.Close()
				return
			}
		}
	}
}

func (c *Client) waitForReconnect(ctx context.Context, err error, backoff time.Duration) bool {
	metrics.RedisSubStatus.Set(0)
	if ctx.Err() != nil {
		return false
	}
	metrics.RedisSubEvents.Inc()
	slog.Warn("Redis 订阅断开，准备重连", "error", err, "backoff", backoff)
	jittered := time.Duration(float64(backoff) * (0.8 + rand.Float64()*0.4))
	timer := time.NewTimer(jittered)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func growBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > subBackoffMax {
		return subBackoffMax
	}
	return next
}

// Close 关闭 Redis 连接。
// 关闭后所有正在进行的操作返回错误，Subscribe 的 goroutine 退出。
func (c *Client) Close() error {
	return c.rdb.Close()
}

// Ping 测试 Redis 连接是否正常。
// 初始化后调用，确认 Redis 可达。
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}
