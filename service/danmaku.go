package service

import (
	"encoding/json"
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
type CreateDanmakuRequest struct {
	Content  string `json:"content"`
	UserID   string `json:"user_id"`
	RoomID   string `json:"room_id"`
	Color    string `json:"color"`
	Position string `json:"position"`
}

// DanmakuService 是弹幕的业务层。
// 职责：创建弹幕 → 存库 + 广播，保证两者一定同时发生。
// 无论消息来自 HTTP 还是 WebSocket，都经过这里统一处理。
type DanmakuService struct {
	store store.Store
	hub   *websocket.Hub
}

// NewDanmakuService 创建一个 DanmakuService。
func NewDanmakuService(s store.Store, hub *websocket.Hub) *DanmakuService {
	return &DanmakuService{store: s, hub: hub}
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
func (s *DanmakuService) createAndBroadcast(req CreateDanmakuRequest) model.Danmaku {
	color := req.Color
	if color == "" {
		color = "#ffffff"
	}

	position := req.Position
	if position == "" {
		position = "middle"
	}

	dm := model.Danmaku{
		ID:        uuid.New().String(),
		Content:   req.Content,
		Color:     color,
		Position:  position,
		Timestamp: time.Now(),
		RoomID:    req.RoomID,
		UserID:    req.UserID,
	}

	// 先存库，再广播——保证数据持久化后再推送给客户端
	s.store.Add(dm)

	data, _ := json.Marshal(dm)
	s.hub.GetOrCreateRoom(req.RoomID).Broadcast(data)

	return dm
}
