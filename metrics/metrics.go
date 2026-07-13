// Package metrics 提供 DanmakuFlow 的 Prometheus 指标定义。
//
// 所有指标以 danmakuflow_ 为前缀，支持自定义注册器（Registry）用于测试隔离。
// 用法：
//
//	import "github.com/1012-Penn/DanmakuFlow/metrics"
//	// 在 main 中注册：
//	metrics.Register(reg) // reg = prometheus.NewRegistry()
//
// 各包通过包级变量直接增减指标，无需依赖注入。
// 注意：Register() 只能调用一次，重复注册会 panic。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// 命名空间和子系统前缀。
const (
	namespace = "danmakuflow"
)

// WebSocket 指标。
var (
	WSConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "ws",
		Name:      "connections",
		Help:      "当前 WebSocket 连接数。",
	})

	WSActiveRooms = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "ws",
		Name:      "active_rooms",
		Help:      "当前活跃房间数。",
	})

	WSConnTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "ws",
		Name:      "conn_total",
		Help:      "WebSocket 连接建立总数。",
	})

	WSConnRejects = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "ws",
		Name:      "conn_rejects_total",
		Help:      "WebSocket 连接拒绝总数，按 reason 分类。",
	}, []string{"reason"})

	WSSlowKicks = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "ws",
		Name:      "slow_kicks_total",
		Help:      "慢客户端被踢出总数。",
	})

	WSMsgRecv = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "ws",
		Name:      "messages_received_total",
		Help:      "WebSocket 接收消息总数。",
	})

	WSClientDeliveries = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "ws",
		Name:      "client_deliveries_total",
		Help:      "WebSocket 投递给客户端的消息总数（含广播重复）。",
	})

	WSBroadcastDrops = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "ws",
		Name:      "broadcast_drops_total",
		Help:      "房间广播因通道满被丢弃的总数。",
	})
)

// Redis 指标。
var (
	RedisPublishTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "redis",
		Name:      "publish_total",
		Help:      "Redis Publish 总数，按 result 分类（success/error/dropped）。",
	}, []string{"result"})

	RedisPublishLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "redis",
		Name:      "publish_latency_seconds",
		Help:      "Redis Publish 延迟（秒）。",
		Buckets:   prometheus.DefBuckets,
	})

	RedisPubQueueLen = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "redis",
		Name:      "publish_queue_len",
		Help:      "Redis 发布队列当前长度。",
	})

	RedisPubQueueCap = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "redis",
		Name:      "publish_queue_cap",
		Help:      "Redis 发布队列容量。",
	})

	RedisSubStatus = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "redis",
		Name:      "sub_status",
		Help:      "Redis 订阅状态：1=已连接，0=已断开。",
	})

	RedisSubEvents = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "redis",
		Name:      "sub_events_total",
		Help:      "Redis 订阅重连或退出次数。",
	})
)

// 持久化指标。
var (
	AsyncChanLen = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "persist",
		Name:      "async_chan_len",
		Help:      "异步写入队列当前长度。",
	})

	AsyncChanCap = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "persist",
		Name:      "async_chan_cap",
		Help:      "异步写入队列容量。",
	})

	StoreWriteTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "persist",
		Name:      "writes_total",
		Help:      "Store 写入总数，按 result 分类（success/error/drop）。",
	}, []string{"result"})

	StoreWriteLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "persist",
		Name:      "write_latency_seconds",
		Help:      "Store 写入延迟（秒）。",
		Buckets:   prometheus.DefBuckets,
	})
)

// HTTP 指标。
var (
	HTTPReqTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "http",
		Name:      "requests_total",
		Help:      "HTTP 请求总数，按 method/route/status 分类。",
	}, []string{"method", "route", "status"})

	HTTPReqDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "http",
		Name:      "request_duration_seconds",
		Help:      "HTTP 请求延迟（秒），按 method/route 分类。",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "route"})
)

// Register 将所有指标注册到 reg。
// 应当在 main 中启动时调用一次。重复注册会 panic。
// 注意：prometheus.DefaultRegisterer 已包含 Go/Process 收集器，不再重复注册。
// 测试中应当使用 prometheus.NewRegistry() 并单独注册需要测试的指标。
func Register(reg prometheus.Registerer) {
	reg.MustRegister(
		// WebSocket
		WSConnections,
		WSActiveRooms,
		WSConnTotal,
		WSConnRejects,
		WSSlowKicks,
		WSMsgRecv,
		WSClientDeliveries,
		WSBroadcastDrops,

		// Redis
		RedisPublishTotal,
		RedisPublishLatency,
		RedisPubQueueLen,
		RedisPubQueueCap,
		RedisSubStatus,
		RedisSubEvents,

		// 持久化
		AsyncChanLen,
		AsyncChanCap,
		StoreWriteTotal,
		StoreWriteLatency,

		// HTTP
		HTTPReqTotal,
		HTTPReqDuration,
	)
}
