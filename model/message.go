// Package model 提供弹幕系统的数据模型。
package model

import "encoding/json"

// MessageEnvelope 是 WebSocket 通信的统一消息信封。
// 所有通过 WebSocket 传输的消息都包裹在这个结构中。
//
// Type 字段指示消息类型，Payload 是具体的业务数据。
// 设计参照 JSON-RPC 的消息格式，但简化以匹配弹幕场景。
type MessageEnvelope struct {
	Type    string          `json:"type"`    // 消息类型
	Payload json.RawMessage `json:"payload"` // 业务数据
}

// 定义消息类型常量。
const (
	// 客户端 → 服务端
	MsgTypeDanmaku = "danmaku" // 发送弹幕

	// 服务端 → 客户端
	MsgTypeBroadcast = "broadcast" // 弹幕广播
	MsgTypeAck       = "ack"       // 发送确认
	MsgTypeError     = "error"     // 错误通知
	MsgTypeHistory   = "history"   // 历史弹幕补偿
)

// AckPayload 是 ACK 消息的负载。
type AckPayload struct {
	RequestID   string `json:"request_id"`           // 客户端请求 ID，用于关联请求和确认
	MessageID   string `json:"message_id"`           // 服务端生成的消息 ID
	OK          bool   `json:"ok"`                   // 是否成功
	Persistence string `json:"persistence"`          // 持久化状态：persisted/buffered/dropped
	ErrorCode   string `json:"error_code,omitempty"` // 错误码
	Reason      string `json:"reason,omitempty"`     // 错误原因
}

// ErrorPayload 是错误消息的负载。
type ErrorPayload struct {
	RequestID string `json:"request_id,omitempty"`
	Code      string `json:"code"`    // 错误码
	Message   string `json:"message"` // 人类可读的错误描述
}

// HistoryPayload 是历史弹幕补偿的负载。
type HistoryPayload struct {
	Danmaku       []Danmaku `json:"danmaku"`
	RoomID        string    `json:"room_id"`
	HasMore       bool      `json:"has_more"`
	NextTime      string    `json:"next_time,omitempty"`
	NextMessageID string    `json:"next_message_id,omitempty"`
}

// HandleResult 是 HandleMessage 的返回类型。
// 定义在 model 包中，避免 websocket 包依赖 service 包。
type HandleResult struct {
	RequestID string `json:"request_id"`
	MessageID string `json:"message_id"`
	OK        bool   `json:"ok"`
	ErrorCode string `json:"error_code,omitempty"`
	Reason    string `json:"reason,omitempty"`
	// Persistence 表示持久化状态：persisted / buffered / dropped
	Persistence string `json:"persistence"`
	// Data 是原始数据，用于广播
	Data []byte `json:"-"`
}
