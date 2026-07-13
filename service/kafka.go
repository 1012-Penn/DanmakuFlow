// Package service 提供弹幕系统的业务逻辑层。
// Kafka producer / consumer 组件放在此包中以共用 model.Danmaku 和 store.Store。

package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/IBM/sarama"

	"github.com/1012-Penn/DanmakuFlow/metrics"
	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
)

// ─── Event Schema ──────────────────────────────────────────────

// SchemaVersion 是当前事件 schema 版本。
const SchemaVersion = 1

// DanmakuEvent 是写入 Kafka topic 的弹幕事件。
// 所有字段使用 json tag（snake_case），与现有 JSON 风格一致。
type DanmakuEvent struct {
	EventID       string    `json:"event_id"`       // 全局唯一事件 ID，等于 danmaku.id
	DanmakuID     string    `json:"danmaku_id"`     // 弹幕 ID，预留未来拆分 event_id / danmaku_id
	RoomID        string    `json:"room_id"`        // 房间 ID
	UserID        string    `json:"user_id"`        // 发送者用户 ID
	Content       string    `json:"content"`        // 弹幕内容
	Color         string    `json:"color"`          // 颜色
	Type          string    `json:"type"`           // 弹幕类型：scroll/top/bottom/reverse
	FontSize      int       `json:"font_size"`      // 字号
	Timestamp     time.Time `json:"timestamp"`      // 弹幕发送时间（UTC）
	InstanceID    string    `json:"instance_id"`    // 来源实例 ID
	SchemaVersion int       `json:"schema_version"` // schema 版本号
}

// eventFromDanmaku 将 model.Danmaku 转为 Kafka 事件。
func eventFromDanmaku(dm model.Danmaku, instanceID string) DanmakuEvent {
	return DanmakuEvent{
		EventID:       dm.ID,
		DanmakuID:     dm.ID,
		RoomID:        dm.RoomID,
		UserID:        dm.UserID,
		Content:       dm.Content,
		Color:         dm.Color,
		Type:          dm.Type,
		FontSize:      dm.FontSize,
		Timestamp:     dm.Timestamp,
		InstanceID:    instanceID,
		SchemaVersion: SchemaVersion,
	}
}

// ─── Producer Interface ────────────────────────────────────────

// KafkaProducerInterface 是 DanmakuService 依赖的 producer 抽象。
// 实现可以是真实的 Kafka SyncProducer 或 mock。
type KafkaProducerInterface interface {
	// Produce 同步发送一条弹幕事件到 Kafka。
	// ctx 控制超时。返回 error 表示 kafka 未确认接收。
	Produce(ctx context.Context, dm model.Danmaku) error

	// Close 关闭 producer，flush 缓冲区。
	Close() error
}

// ─── SyncProducer ──────────────────────────────────────────────

// KafkaProducer 封装 sarama.SyncProducer，提供弹幕事件的生产能力。
type KafkaProducer struct {
	producer   sarama.SyncProducer
	topic      string
	instanceID string
}

// NewKafkaProducer 创建并返回 KafkaProducer。
func NewKafkaProducer(brokers []string, topic, clientID string) (*KafkaProducer, error) {
	config := sarama.NewConfig()
	config.Producer.RequiredAcks = sarama.WaitForAll       // acks=all
	config.Producer.Retry.Max = 3                          // 最多重试 3 次
	config.Producer.Retry.Backoff = 100 * time.Millisecond // 重试间隔
	config.Producer.Return.Successes = true                // SyncProducer 需要
	config.Producer.Timeout = 5 * time.Second              // delivery.timeout.ms
	config.ClientID = clientID

	producer, err := sarama.NewSyncProducer(brokers, config)
	if err != nil {
		return nil, err
	}

	return &KafkaProducer{
		producer:   producer,
		topic:      topic,
		instanceID: clientID,
	}, nil
}

// Produce 将弹幕事件同步发送到 Kafka。
// 使用 dm.ID 作为消息 key（保证同一弹幕的多次 produce 落在同分区，配合幂等消费）。
func (p *KafkaProducer) Produce(ctx context.Context, dm model.Danmaku) error {
	event := eventFromDanmaku(dm, p.instanceID)
	value, err := json.Marshal(event)
	if err != nil {
		metrics.KafkaProduceTotal.WithLabelValues("error").Inc()
		return err
	}

	msg := &sarama.ProducerMessage{
		Topic: p.topic,
		Key:   sarama.StringEncoder(dm.ID),
		Value: sarama.ByteEncoder(value),
	}

	start := time.Now()
	_, _, err = p.producer.SendMessage(msg)
	metrics.KafkaProduceLatency.Observe(time.Since(start).Seconds())

	if err != nil {
		metrics.KafkaProduceTotal.WithLabelValues("error").Inc()
		return err
	}

	metrics.KafkaProduceTotal.WithLabelValues("success").Inc()
	return nil
}

// Close 关闭 producer，flush 所有未发送消息。
func (p *KafkaProducer) Close() error {
	return p.producer.Close()
}

// KafkaPing 尝试通过发送测试消息到 __consumer_offsets（无需管理员权限的方式）
// 来验证 Kafka 连通性。简单实现：直接用 sarama.NewClient 并 Close。
func KafkaPing(brokers []string) bool {
	config := sarama.NewConfig()
	config.Net.DialTimeout = 1 * time.Second
	config.Producer.RequiredAcks = sarama.WaitForLocal
	config.Producer.Timeout = 1 * time.Second

	client, err := sarama.NewClient(brokers, config)
	if err != nil {
		return false
	}
	_ = client.Close()
	return true
}

// ─── Consumer ──────────────────────────────────────────────────

// KafkaConsumer 从 Kafka topic 消费弹幕事件并写入 store。
type KafkaConsumer struct {
	consumer sarama.ConsumerGroup
	topic    string
	group    string
	store    store.Store
	ready    chan struct{}
}

// NewKafkaConsumer 创建并返回 KafkaConsumer。
// store 用于写入（MySQL），brokers 即 Kafka 代理列表。
func NewKafkaConsumer(brokers []string, topic, group, clientID string, s store.Store) (*KafkaConsumer, error) {
	config := sarama.NewConfig()
	config.Consumer.Group.Rebalance.GroupStrategies = []sarama.BalanceStrategy{sarama.NewBalanceStrategyRoundRobin()}
	config.Consumer.Offsets.Initial = sarama.OffsetOldest // 首次从最早开始消费
	config.Consumer.Offsets.AutoCommit.Enable = false     // 手动提交
	config.ClientID = clientID

	cg, err := sarama.NewConsumerGroup(brokers, group, config)
	if err != nil {
		return nil, err
	}

	return &KafkaConsumer{
		consumer: cg,
		topic:    topic,
		group:    group,
		store:    s,
		ready:    make(chan struct{}),
	}, nil
}

// consumerGroupHandler 实现 sarama.ConsumerGroupHandler 接口。
type consumerGroupHandler struct {
	store store.Store
	topic string
	ready chan struct{}
}

func (h *consumerGroupHandler) Setup(sarama.ConsumerGroupSession) error {
	close(h.ready)
	return nil
}

func (h *consumerGroupHandler) Cleanup(sarama.ConsumerGroupSession) error {
	return nil
}

func (h *consumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for msg := range claim.Messages() {
		if err := h.processMessage(session, msg); err != nil {
			// processMessage 内部已记录日志和指标
			// 跳过该消息，继续消费下一条
		}
	}
	return nil
}

// processMessage 处理单条 Kafka 消息。
// 返回 error 表示处理后仍失败（已被跳过）。
func (h *consumerGroupHandler) processMessage(session sarama.ConsumerGroupSession, msg *sarama.ConsumerMessage) error {
	metrics.KafkaConsumerMsgTotal.Inc()

	var event DanmakuEvent
	if err := json.Unmarshal(msg.Value, &event); err != nil {
		slog.Warn("Kafka 消息反序列化失败",
			"topic", msg.Topic,
			"partition", msg.Partition,
			"offset", msg.Offset,
			"error", err,
		)
		metrics.KafkaConsumerErrTotal.WithLabelValues("deserialize").Inc()
		// 反序列化失败：跳过该消息
		session.MarkMessage(msg, "")
		return nil
	}

	// 构建弹幕对象
	dm := model.Danmaku{
		ID:        event.DanmakuID,
		Content:   event.Content,
		Color:     event.Color,
		Type:      event.Type,
		FontSize:  event.FontSize,
		RoomID:    event.RoomID,
		Timestamp: event.Timestamp,
		UserID:    event.UserID,
	}

	// 写入 store（幂等：duplicate key 视为成功）
	if err := h.store.Add(dm); err != nil {
		if isDuplicateKeyError(err) {
			// 重复消息：幂等写入成功
			metrics.KafkaConsumerErrTotal.WithLabelValues("mysql_dup").Inc()
			session.MarkMessage(msg, "")
			return nil
		}

		slog.Error("Kafka 消费写入 MySQL 失败",
			"dm_id", dm.ID,
			"room_id", dm.RoomID,
			"error", err,
		)
		metrics.KafkaConsumerErrTotal.WithLabelValues("mysql_other").Inc()

		// 重试最多 3 次
		for retry := 1; retry <= 3; retry++ {
			time.Sleep(time.Duration(retry*100) * time.Millisecond)
			if err := h.store.Add(dm); err != nil {
				if isDuplicateKeyError(err) {
					metrics.KafkaConsumerErrTotal.WithLabelValues("mysql_dup").Inc()
					session.MarkMessage(msg, "")
					return nil
				}
				continue
			}
			// 重试成功
			session.MarkMessage(msg, "")
			return nil
		}

		// 3 次重试后仍失败：记录日志、递增指标、跳过以保持消费进度
		slog.Error("Kafka 消费写入 MySQL 重试耗尽，跳过消息",
			"dm_id", dm.ID,
			"room_id", dm.RoomID,
			"error", err,
		)
		// 仍 commit offset 避免消费积压
		session.MarkMessage(msg, "")
		return err
	}

	session.MarkMessage(msg, "")
	return nil
}

// Start 启动 consumer group 消费循环。
// 会阻塞直到 ctx 被取消或 consumer 遇到不可恢复错误。
func (c *KafkaConsumer) Start(ctx context.Context) {
	handler := &consumerGroupHandler{
		store: c.store,
		topic: c.topic,
		ready: c.ready,
	}

	for {
		select {
		case <-ctx.Done():
			slog.Info("Kafka consumer 收到停止信号，退出消费循环")
			return
		default:
		}

		if err := c.consumer.Consume(ctx, []string{c.topic}, handler); err != nil {
			if errors.Is(err, sarama.ErrClosedConsumerGroup) {
				return
			}
			slog.Error("Kafka consumer 消费循环错误", "error", err)
			// 等待后重试
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

// Close 关闭 consumer group。
func (c *KafkaConsumer) Close() error {
	return c.consumer.Close()
}

// ─── Helpers ───────────────────────────────────────────────────

// isDuplicateKeyError 判断 MySQL 错误是否为 duplicate key。
// GORM + go-sql-driver/mysql 在违反唯一约束时返回 "Error 1062: Duplicate entry ..."。
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "duplicate entry")
}

// ensure interface compliance
var _ KafkaProducerInterface = (*KafkaProducer)(nil)
