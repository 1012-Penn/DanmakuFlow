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
	"log/slog"

	"github.com/redis/go-redis/v9"
)

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
func (c *Client) Publish(ctx context.Context, roomID string, data []byte) error {
	msg := Message{
		SourceID: c.instance,
		RoomID:   roomID,
		Data:     data,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.rdb.Publish(ctx, "room:"+roomID, payload).Err()
}

// Subscribe 订阅所有房间频道（PSUBSCRIBE room:*），返回接收消息的 channel。
// 使用 PSubscribe 模式匹配而不必为每个房间单独 Subscribe。
// 收到的消息已自动过滤本实例发出的（SourceID 匹配则跳过）。
//
// ctx 用于控制订阅生命周期——ctx 取消时 goroutine 退出。
func (c *Client) Subscribe(ctx context.Context) <-chan Message {
	pubsub := c.rdb.PSubscribe(ctx, "room:*")
	ch := make(chan Message, 64)

	go func() {
		defer pubsub.Close()
		defer close(ch) // 退出时关闭 channel，让消费者 range 退出
		for {
			redisMsg, err := pubsub.ReceiveMessage(ctx)
			if err != nil {
				// ctx 取消或连接断开，正常退出
				slog.Debug("Redis 订阅已退出", "error", err)
				return
			}

			var msg Message
			if err := json.Unmarshal([]byte(redisMsg.Payload), &msg); err != nil {
				slog.Warn("Redis 消息解析失败", "error", err)
				continue
			}

			// 跳过自己发布的消息——本实例已经做过本地广播了
			if msg.SourceID == c.instance {
				continue
			}

			select {
			case ch <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch
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
