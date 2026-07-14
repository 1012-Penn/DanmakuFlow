package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/1012-Penn/DanmakuFlow/metrics"
	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
	"github.com/1012-Penn/DanmakuFlow/websocket"
)

type CreateDanmakuRequest struct {
	Content   string `json:"content"`
	UserID    string `json:"user_id"`
	RoomID    string `json:"room_id"`
	Color     string `json:"color"`
	Type      string `json:"type"`
	FontSize  int    `json:"font_size"`
	RequestID string `json:"request_id"`
}

var (
	ErrPersistenceQueueFull = errors.New("persistence queue is full")
	ErrPersistenceFailed    = errors.New("persistence failed")
	ErrRoomNotLive          = errors.New("room is not live")
	ErrContentBlocked       = errors.New("content blocked by moderation")
	ErrMuted                = errors.New("user is muted in this room")
	ErrUserBanned           = errors.New("user is banned")
)

// 弹幕发送相关领域错误。
var (
	ErrUnauthorizedWS = errors.New("authentication required")
)

// rateLimiter 基于内存的每用户频率限制。
type rateLimiter struct {
	mu             sync.Mutex
	lastTime       map[string]time.Time
	interval       time.Duration
	lastCleanCount int
}

func newRateLimiter(msgsPerSec float64) *rateLimiter {
	var interval time.Duration
	if msgsPerSec > 0 {
		interval = time.Duration(float64(time.Second) / msgsPerSec)
	}
	return &rateLimiter{
		lastTime: make(map[string]time.Time),
		interval: interval,
	}
}

func (rl *rateLimiter) Allow(userID string) bool {
	if rl.interval <= 0 {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	last, ok := rl.lastTime[userID]
	now := time.Now()
	if ok && now.Sub(last) < rl.interval {
		return false
	}
	rl.lastTime[userID] = now

	if size := len(rl.lastTime); size > 1000 && size-rl.lastCleanCount > 1000 {
		threshold := now.Add(-rl.interval * 2)
		for uid, t := range rl.lastTime {
			if t.Before(threshold) {
				delete(rl.lastTime, uid)
			}
		}
		rl.lastCleanCount = len(rl.lastTime)
	}
	return true
}

// DanmakuService 是弹幕的业务层。
//
// 状态机：
//   - 正常运行时 accepting=true，创建弹幕时登记 inflight，完成后释放
//   - Shutdown 先将 accepting=false，禁止新请求
//   - 等待所有 inflight 完成
//   - 然后关闭 producer / danmakuChan（确保 no send on closed channel）
//   - 最后等待 consumer 排空
//
// Kafka producer 与 danmakuChan 是互斥的：
//   - kafkaProducer != nil → 使用 Kafka 作为持久化路径，danmakuChan 不创建
//   - kafkaProducer == nil → 使用 danmakuChan + 直写 MySQL（现有行为不变）
type DanmakuService struct {
	store            store.Store
	hub              *websocket.Hub
	danmakuChan      chan model.Danmaku
	wg               sync.WaitGroup
	closed           atomic.Bool
	rateLimiter      *rateLimiter
	hasDSN           bool
	roomStatusGetter model.RoomStatusGetter
	kafkaProducer    KafkaProducerInterface // nil = 不使用 Kafka
	kafkaBrokers     []string               // Kafka broker 列表，用于 Ping
	kafkaPingFn      func() bool            // Ping 函数，由内部或测试注入
	instanceID       string                 // 实例 ID，写入 Kafka event

	// 审核治理服务（nil = 不启用）
	moderation *ModerationService

	// 接受/在途状态机
	// 使用 atomic.Int64 避免 sync.WaitGroup.Add 与 Wait 的非法并发
	accepting atomic.Bool
	inflight  atomic.Int64
}

func NewDanmakuService(s store.Store, hub *websocket.Hub, asyncBufferSize int, msgsPerSec float64, hasDSN bool, roomStatusGetter model.RoomStatusGetter, kafkaProducer KafkaProducerInterface, kafkaBrokers []string, instanceID string, moderation *ModerationService) *DanmakuService {
	svc := &DanmakuService{
		store:            s,
		hub:              hub,
		danmakuChan:      nil,
		rateLimiter:      newRateLimiter(msgsPerSec),
		hasDSN:           hasDSN,
		roomStatusGetter: roomStatusGetter,
		kafkaProducer:    kafkaProducer,
		kafkaBrokers:     kafkaBrokers,
		instanceID:       instanceID,
		moderation:       moderation,
		kafkaPingFn: func() bool {
			if len(kafkaBrokers) == 0 {
				return false
			}
			return KafkaPing(kafkaBrokers)
		},
	}
	svc.accepting.Store(true)
	// Kafka 启用时 danmakuChan 不创建，持久化由 Kafka SyncProducer 完成
	if kafkaProducer == nil && asyncBufferSize > 0 {
		svc.danmakuChan = make(chan model.Danmaku, asyncBufferSize)
		metrics.AsyncChanCap.Set(float64(asyncBufferSize))
		svc.wg.Add(1)
		go svc.danmakuConsumer()
		go svc.updateAsyncChanLen()
	}
	return svc
}

// danmakuConsumer 从 channel 中取出弹幕，写入 store。
func (s *DanmakuService) danmakuConsumer() {
	defer s.wg.Done()
	for dm := range s.danmakuChan {
		writeStart := time.Now()
		if err := s.store.Add(dm); err != nil {
			slog.Error("存储写入失败",
				"dm_id", dm.ID,
				"room_id", dm.RoomID,
				"error", err,
			)
			metrics.StoreWriteTotal.WithLabelValues("error").Inc()
		} else {
			metrics.StoreWriteTotal.WithLabelValues("success").Inc()
		}
		metrics.StoreWriteLatency.Observe(time.Since(writeStart).Seconds())
	}
}

// HandleMessage 处理 WebSocket 收到的消息，返回 HandleResult。
// 支持两种格式：
//   - 新信封格式：{"type":"danmaku","payload":{...}}
//   - 旧裸格式：{"content":"...","user_id":"...","request_id":"..."}
//
// actor 参数提供认证身份：
//   - actor.Authenticated == true：强制使用 actor.UserID 覆盖客户端 user_id
//   - actor.Authenticated == false：返回 unauthorized 错误
func (s *DanmakuService) HandleMessage(roomID string, actor model.Actor, data []byte) model.HandleResult {
	// 1. 解析消息（先解析以获取 request_id）
	var req CreateDanmakuRequest
	var env model.MessageEnvelope
	if err := json.Unmarshal(data, &env); err == nil && env.Type == model.MsgTypeDanmaku {
		data = env.Payload
	}
	// 先做初步解析获取 request_id（即使后续校验失败也需要回填）
	_ = json.Unmarshal(data, &req)
	req.RoomID = roomID

	// 2. 未认证用户不能发送弹幕
	if !actor.Authenticated {
		return model.HandleResult{
			RequestID: req.RequestID,
			OK:        false,
			ErrorCode: model.ErrCodeUnauthorized,
			Reason:    "authentication required",
		}
	}

	// 3. HTTP 与 WebSocket 共享同一套可发送性校验。
	if err := s.validateRoomForSend(roomID); err != nil {
		return model.HandleResult{
			RequestID: req.RequestID,
			OK:        false,
			ErrorCode: roomSendErrorCode(err),
			Reason:    err.Error(),
		}
	}

	// 4. 完整解析请求
	if err := json.Unmarshal(data, &req); err != nil {
		return model.HandleResult{
			RequestID: req.RequestID,
			OK:        false,
			ErrorCode: model.ErrCodeValidationError,
			Reason:    "invalid JSON: " + err.Error(),
		}
	}
	req.RoomID = roomID
	// 强制使用认证身份覆盖客户端传入的 user_id
	req.UserID = actor.UserID

	dm, persistence, err := s.createAndBroadcast(req)
	if err != nil {
		slog.Warn("WS 弹幕校验不通过",
			"room_id", roomID,
			"user_id", actor.UserID,
			"error", err,
		)
		return model.HandleResult{
			RequestID: req.RequestID,
			OK:        false,
			ErrorCode: roomSendErrorCode(err),
			Reason:    err.Error(),
		}
	}

	dataBytes, _ := json.Marshal(dm)
	return model.HandleResult{
		RequestID:   req.RequestID,
		MessageID:   dm.ID,
		OK:          true,
		Persistence: persistence,
		Data:        dataBytes,
	}
}

func (s *DanmakuService) CreateDanmaku(req CreateDanmakuRequest) (model.Danmaku, error) {
	if err := s.validateRoomForSend(req.RoomID); err != nil {
		return model.Danmaku{}, err
	}
	dm, _, err := s.createAndBroadcast(req)
	return dm, err
}

// validateRoomForSend defines the business rule shared by HTTP and WebSocket
// producers. History reads intentionally do not use this rule: ended rooms keep
// their public replay value.
func (s *DanmakuService) validateRoomForSend(roomID string) error {
	if s.roomStatusGetter == nil {
		return nil
	}

	status, err := s.roomStatusGetter.GetStatus(roomID)
	if err != nil {
		if errors.Is(err, ErrRoomNotFound) {
			return ErrRoomNotFound
		}
		slog.Warn("查询房间状态失败", "room_id", roomID, "error", err)
		return fmt.Errorf("%w: room status lookup: %v", ErrPersistenceFailed, err)
	}

	switch status {
	case model.RoomStatusLive:
		return nil
	case "":
		return ErrRoomNotFound
	case model.RoomStatusBanned:
		return ErrRoomBanned
	case model.RoomStatusPending, model.RoomStatusEnded:
		return ErrRoomNotLive
	default:
		return ErrRoomNotLive
	}
}

func roomSendErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrRoomNotFound):
		return model.ErrCodeRoomNotFound
	case errors.Is(err, ErrRoomBanned):
		return model.ErrCodeRoomBanned
	case errors.Is(err, ErrRoomNotLive):
		return model.ErrCodeRoomNotLive
	case errors.Is(err, ErrPersistenceQueueFull), errors.Is(err, ErrPersistenceFailed):
		return model.ErrCodePersistenceUnavail
	case errors.Is(err, ErrContentBlocked):
		return model.ErrCodeContentBlocked
	case errors.Is(err, ErrMuted):
		return model.ErrCodeMuted
	case errors.Is(err, ErrUserBanned):
		return model.ErrCodeUserBanned
	default:
		return model.ErrCodeValidationError
	}
}

func (s *DanmakuService) ListByRoom(roomID string, limit int) []model.Danmaku {
	return s.store.ListByRoom(roomID, limit)
}

var validTypes = map[string]bool{
	"scroll": true, "top": true, "bottom": true, "reverse": true,
}

func (s *DanmakuService) HasStoreDSN() bool {
	return s.hasDSN
}

func (s *DanmakuService) PingStore() bool {
	return s.store.Ping()
}

// HasKafkaConfig 返回是否配置了 Kafka producer。
func (s *DanmakuService) HasKafkaConfig() bool {
	return s.kafkaProducer != nil
}

// PingKafka 检查 Kafka 连通性（仅当配置了 Kafka 时）。
func (s *DanmakuService) PingKafka() bool {
	if s.kafkaProducer == nil || s.kafkaPingFn == nil {
		return false
	}
	return s.kafkaPingFn()
}

// QueryHistory 查询指定房间在 sinceTime+lastID 之后的弹幕历史。
func (s *DanmakuService) QueryHistory(roomID string, sinceTime time.Time, lastID string, limit int) ([]model.Danmaku, error) {
	return s.store.ListSince(roomID, sinceTime, lastID, limit)
}

func (req CreateDanmakuRequest) validate() error {
	if strings.TrimSpace(req.Content) == "" {
		return errors.New("content cannot be empty")
	}
	if len(req.Content) > 500 {
		return errors.New("content cannot exceed 500 characters")
	}
	if strings.TrimSpace(req.UserID) == "" {
		return errors.New("user_id cannot be empty")
	}
	if strings.TrimSpace(req.RoomID) == "" {
		return errors.New("room_id cannot be empty")
	}
	if req.Type != "" && !validTypes[req.Type] {
		return errors.New("type must be one of scroll/top/bottom/reverse")
	}
	if req.FontSize != 0 && req.FontSize != 18 && req.FontSize != 25 && req.FontSize != 36 {
		return errors.New("font_size must be 18/25/36")
	}
	if req.Color != "" && !strings.HasPrefix(req.Color, "#") {
		return errors.New("color must start with #, e.g. #FFFFFF")
	}
	return nil
}

func (s *DanmakuService) updateAsyncChanLen() {
	if s.danmakuChan == nil {
		return
	}
	for {
		if s.closed.Load() {
			metrics.AsyncChanLen.Set(0)
			return
		}
		metrics.AsyncChanLen.Set(float64(len(s.danmakuChan)))
		time.Sleep(time.Second)
	}
}

// Shutdown 优雅关闭。
// 必须先禁止新请求，再等待在途请求完成，然后关闭并排空 danmakuChan。
func (s *DanmakuService) Shutdown(ctx context.Context) error {
	// 1. 禁止新请求
	s.accepting.Store(false)

	// 2. 等待所有在途业务请求完成
	pollCtx, pollCancel := context.WithTimeout(ctx, 10*time.Second)
	defer pollCancel()
	for s.inflight.Load() > 0 {
		select {
		case <-pollCtx.Done():
			slog.Warn("等待在途请求超时",
				"remaining", s.inflight.Load(),
				"error", pollCtx.Err(),
			)
			// Do not close danmakuChan while an accepted request may still send.
			// The caller may retry Shutdown after the dependency recovers.
			return pollCtx.Err()
		default:
			time.Sleep(time.Millisecond)
		}
	}
	slog.Info("所有在途请求已完成")

	// 3. 关闭 Kafka producer（flush 缓冲区）
	if s.kafkaProducer != nil {
		if err := s.kafkaProducer.Close(); err != nil {
			slog.Warn("Kafka producer 关闭失败", "error", err)
		}
		slog.Info("Kafka producer 已关闭")
	}

	// 5. 关闭 danmakuChan（此时保证无新写入）
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	if s.danmakuChan == nil {
		return nil
	}
	close(s.danmakuChan)

	// 4. 等待 consumer 排空
	drainDone := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(drainDone)
	}()
	select {
	case <-drainDone:
		slog.Info("异步写入通道已排空")
		return nil
	case <-ctx.Done():
		slog.Warn("异步写入通道排空超时", "error", ctx.Err())
		return ctx.Err()
	}
}

// createAndBroadcast 是内部核心方法。
// 先登记 inflight 再检查 accepting，确保 Shutdown 不会漏掉在途请求。
func (s *DanmakuService) createAndBroadcast(req CreateDanmakuRequest) (model.Danmaku, string, error) {
	s.inflight.Add(1)
	defer s.inflight.Add(-1)

	if !s.accepting.Load() {
		return model.Danmaku{}, "", errors.New("service is shutting down")
	}

	if err := req.validate(); err != nil {
		return model.Danmaku{}, "", err
	}

	if s.rateLimiter != nil && !s.rateLimiter.Allow(req.UserID) {
		return model.Danmaku{}, "", errors.New("sending too fast, please wait")
	}

	// 审核检查（ModerationService 为 nil 时跳过）
	if s.moderation != nil {
		banned, err := s.moderation.CheckUserBan(req.UserID)
		if err != nil && s.moderation.IsFailClosed() {
			return model.Danmaku{}, "", err
		}
		if banned {
			return model.Danmaku{}, "", ErrUserBanned
		}

		muted, err := s.moderation.CheckMute(req.UserID, req.RoomID)
		if err != nil && s.moderation.IsFailClosed() {
			return model.Danmaku{}, "", err
		}
		if muted {
			return model.Danmaku{}, "", ErrMuted
		}

		blocked, flagged, matchedWord := s.moderation.CheckContent(req.Content)
		if blocked {
			slog.Warn("弹幕被屏蔽词拦截",
				"user_id", req.UserID, "room_id", req.RoomID,
				"word", matchedWord,
			)
			return model.Danmaku{}, "", fmt.Errorf("%w: %s", ErrContentBlocked, matchedWord)
		}
		if flagged {
			// 标记待审：持久化但不广播
			dm := s.buildDanmaku(req)
			dm.Status = model.DanmakuStatusFlagged
			persistence, err := s.persistDanmaku(dm)
			if err != nil {
				return model.Danmaku{}, "", err
			}
			slog.Info("弹幕已标记待审", "dm_id", dm.ID, "word", matchedWord)
			return dm, persistence, nil
		}
	}

	dm := s.buildDanmaku(req)
	dm.Status = model.DanmakuStatusApproved

	persistence, err := s.persistDanmaku(dm)
	if err != nil {
		return model.Danmaku{}, "", err
	}

	data, _ := json.Marshal(dm)
	bcastPayload, _ := json.Marshal(model.MessageEnvelope{
		Type:    model.MsgTypeBroadcast,
		Payload: data,
	})
	s.hub.BroadcastToRoom(req.RoomID, bcastPayload)

	return dm, persistence, nil
}

// buildDanmaku 根据请求构建弹幕对象，应用默认值。
func (s *DanmakuService) buildDanmaku(req CreateDanmakuRequest) model.Danmaku {
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

	return model.Danmaku{
		ID:        uuid.New().String(),
		Content:   req.Content,
		Color:     color,
		Type:      dmType,
		FontSize:  fontSize,
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
		RoomID:    req.RoomID,
		UserID:    req.UserID,
		Status:    model.DanmakuStatusApproved,
	}
}

// persistDanmaku 持久化弹幕，返回持久化状态字符串。
// 支持 Kafka 路径（同步 produce）、异步 channel 路径、直写 MySQL/内存 路径。
func (s *DanmakuService) persistDanmaku(dm model.Danmaku) (string, error) {
	persistence := "persisted"
	if s.kafkaProducer != nil {
		produceCtx, produceCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := s.kafkaProducer.Produce(produceCtx, dm); err != nil {
			produceCancel()
			slog.Error("Kafka produce failed",
				"dm_id", dm.ID,
				"room_id", dm.RoomID,
				"error", err,
			)
			return "", fmt.Errorf("%w: kafka: %v", ErrPersistenceFailed, err)
		}
		produceCancel()
		persistence = "persisted"
	} else if s.danmakuChan != nil {
		select {
		case s.danmakuChan <- dm:
			persistence = "buffered"
		default:
			slog.Warn("async write channel full, dropping danmaku",
				"chan_cap", cap(s.danmakuChan),
				"dm_id", dm.ID,
				"room_id", dm.RoomID,
			)
			metrics.StoreWriteTotal.WithLabelValues("drop").Inc()
			return "", ErrPersistenceQueueFull
		}
	} else {
		writeStart := time.Now()
		if err := s.store.Add(dm); err != nil {
			slog.Error("store write failed",
				"dm_id", dm.ID,
				"room_id", dm.RoomID,
				"error", err,
			)
			metrics.StoreWriteTotal.WithLabelValues("error").Inc()
			metrics.StoreWriteLatency.Observe(time.Since(writeStart).Seconds())
			return "", fmt.Errorf("%w: %v", ErrPersistenceFailed, err)
		} else {
			metrics.StoreWriteTotal.WithLabelValues("success").Inc()
		}
		metrics.StoreWriteLatency.Observe(time.Since(writeStart).Seconds())
	}
	return persistence, nil
}
