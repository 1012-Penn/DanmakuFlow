package service

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
	"github.com/1012-Penn/DanmakuFlow/websocket"
)

// CreateDanmakuRequest 是创建弹幕的通用请求结构。
// HTTP 和 WebSocket 都使用这个结构，但解析方式不同：
//   - HTTP:  Gin 的 ShouldBindJSON 把请求体直接绑定到这里
//   - WebSocket: readPump 收到原始 JSON 字节，内部 Unmarshal 到这里
//
// Type 和 FontSize 参照 Bilibili 弹幕协议设计。
//
//	Type: "scroll"（滚动）/ "top"（顶部）/ "bottom"（底部）/ "reverse"（逆向）
//	FontSize: 25（普通）/ 18（小）
type CreateDanmakuRequest struct {
	Content  string `json:"content"`
	UserID   string `json:"user_id"`
	RoomID   string `json:"room_id"`
	Color    string `json:"color"`
	Type     string `json:"type"`
	FontSize int    `json:"font_size"`
}

// DanmakuService 是弹幕的业务层。
// 职责：创建弹幕 → 存库 + 广播，保证两者一定同时发生。
// 无论消息来自 HTTP 还是 WebSocket，都经过这里统一处理。
//
// 写库采用异步 channel 模式：createAndBroadcast 将弹幕写入
// danmakuChan 后立即返回，不阻塞 WS 广播。后台 consumer goroutine
// 从 channel 读出并调用 store.Add。这样数据库写入慢不会拖慢弹幕推送。
type DanmakuService struct {
	store       store.Store
	hub         *websocket.Hub
	danmakuChan chan model.Danmaku // 异步写库通道，nil = 同步写
}

// NewDanmakuService 创建一个 DanmakuService。
// asyncBufferSize > 0 时启用异步写库；= 0 时为同步写（测试场景用）。
func NewDanmakuService(s store.Store, hub *websocket.Hub, asyncBufferSize int) *DanmakuService {
	svc := &DanmakuService{
		store:       s,
		hub:         hub,
		danmakuChan: nil,
	}
	if asyncBufferSize > 0 {
		svc.danmakuChan = make(chan model.Danmaku, asyncBufferSize)
		go svc.danmakuConsumer()
	}
	return svc
}

// danmakuConsumer 从 channel 中取出弹幕，写入 store。
// 在独立的 goroutine 中运行，不阻塞主流程。
func (s *DanmakuService) danmakuConsumer() {
	for dm := range s.danmakuChan {
		s.store.Add(dm)
	}
}

// HandleMessage 供 WebSocket Client 调用。
// data 是浏览器发来的原始 JSON 字节，内部解析后走统一流程。
func (s *DanmakuService) HandleMessage(roomID string, data []byte) {
	var req CreateDanmakuRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return
	}
	req.RoomID = roomID
	s.createAndBroadcast(req)
}

// CreateDanmaku 供 HTTP Handler 调用。
// 返回完整的 Danmaku 对象，供 handler 序列化为 HTTP 响应。
func (s *DanmakuService) CreateDanmaku(req CreateDanmakuRequest) (model.Danmaku, error) {
	dm := s.createAndBroadcast(req)
	return dm, nil
}

// ListByRoom 返回指定房间的弹幕历史。
// 由 HTTP Handler 调用，供前端拉取历史弹幕。
func (s *DanmakuService) ListByRoom(roomID string, limit int) []model.Danmaku {
	return s.store.ListByRoom(roomID, limit)
}

// createAndBroadcast 是内部核心方法：构建 Danmaku → 存库 → 广播。
// HandleMessage 和 CreateDanmaku 最终都调它，保证一致的行为。
//
// 写库策略：
//   - 异步模式（danmakuChan != nil）：弹幕入 channel → 立即广播 → 返回
//   - 同步模式（danmakuChan == nil）：先写库 → 再广播 → 返回（测试用）
func (s *DanmakuService) createAndBroadcast(req CreateDanmakuRequest) model.Danmaku {
	color := req.Color
	if color == "" {
		color = "#ffffff"
	}

	dmType := req.Type
	if dmType == "" {
		dmType = "scroll"
	}

	fontSize := req.FontSize
	if fontSize <= 0 {
		fontSize = 25
	}

	dm := model.Danmaku{
		ID:        uuid.New().String(),
		Content:   req.Content,
		Color:     color,
		Type:      dmType,
		FontSize:  fontSize,
		Timestamp: time.Now(),
		RoomID:    req.RoomID,
		UserID:    req.UserID,
	}

	// 异步写库：不阻塞广播
	if s.danmakuChan != nil {
		select {
		case s.danmakuChan <- dm:
		default:
			slog.Warn("异步写入通道已满, 丢弃弹幕",
				"chan_cap", cap(s.danmakuChan),
				"dm_id", dm.ID,
				"room_id", dm.RoomID,
			)
		}
	} else {
		// 同步写库（测试等小流量场景）
		s.store.Add(dm)
	}

	// 广播始终同步（低延迟要求）
	data, _ := json.Marshal(dm)
	s.hub.GetOrCreateRoom(req.RoomID).Broadcast(data)

	return dm
}
