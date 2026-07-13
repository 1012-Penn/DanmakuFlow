// Package main 提供 DanmakuFlow 压测工具。
package main

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// MessageID 唯一标识一条压测消息。
type MessageID struct {
	TalkerID int
	Seq      int
	SendIdx  int64
}

// SendRecord 记录一条已发送消息。
type SendRecord struct {
	ID              MessageID
	SendTime        time.Time
	ExpectedClients int // 发送时在线的客户端数
}

// DeliveryRecord 记录一次投递接收。
type DeliveryRecord struct {
	ListenerID  int
	MessageID   MessageID
	ReceiveTime time.Time
}

// LatencyRecord 记录一条消息的端到端延迟。
type LatencyRecord struct {
	Latency time.Duration
}

// latPercentile 计算百分位延迟（毫秒）。
func latPercentile(records []LatencyRecord, p int) float64 {
	if len(records) == 0 {
		return 0
	}
	idx := int(math.Ceil(float64(len(records))*float64(p)/100)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(records) {
		idx = len(records) - 1
	}
	return records[idx].Latency.Seconds() * 1000
}

// Stats 保存单次压测的完整统计结果。
type Stats struct {
	Config             string
	SteadyDuration     time.Duration
	ConnectionSuccess  int64
	ConnectionTotal    int
	SentCount          int64
	DeliveryCount      int64 // 去重后的 (listener, message_id) 对数
	ExpectedDeliveries int64 // 预期投递总数（每条消息 × 当时在线客户端数）
	LossPct            float64
	SendQPS            float64
	DeliveryThroughput float64
	P50Ms              float64
	P95Ms              float64
	P99Ms              float64
	DrainRemaining     int
	Errors             int64
}

// ComputeStats 从发送/接收记录计算最终统计。
// 传入的去重后的 deliveries 必须是按 (listener_id, message_id) 去重后的。
func ComputeStats(
	sends []SendRecord,
	deliveries []DeliveryRecord,
	latencies []LatencyRecord,
	steadyStart, steadyEnd time.Time,
	connSuccess int64,
	connTotal int,
	drainRemaining int,
	errors int64,
	config string,
) Stats {
	steadyDuration := steadyEnd.Sub(steadyStart)
	if steadyDuration <= 0 {
		steadyDuration = time.Millisecond
	}

	sentCount := int64(len(sends))

	// 去重 deliveries 按 (listener_id, message_id)
	dedup := make(map[[2]int64]struct{})
	for _, d := range deliveries {
		key := [2]int64{int64(d.ListenerID), int64(d.MessageID.SendIdx)}
		dedup[key] = struct{}{}
	}
	deliveryCount := int64(len(dedup))

	// 预期投递总数
	var expectedDeliveries int64
	for _, s := range sends {
		expectedDeliveries += int64(s.ExpectedClients)
	}

	// 精确丢失率
	lossPct := 0.0
	if expectedDeliveries > 0 {
		lossPct = float64(expectedDeliveries-deliveryCount) / float64(expectedDeliveries) * 100
		if lossPct < 0 {
			lossPct = 0
		}
	}

	// 发送 QPS = 稳态窗口内的发送量 / 稳态时长
	var steadySends int64
	for _, s := range sends {
		if (s.SendTime.Equal(steadyStart) || s.SendTime.After(steadyStart)) &&
			(s.SendTime.Before(steadyEnd) || s.SendTime.Equal(steadyEnd)) {
			steadySends++
		}
	}
	sendQPS := float64(steadySends) / steadyDuration.Seconds()

	// 投递吞吐
	deliveryThroughput := float64(deliveryCount) / steadyDuration.Seconds()

	// 延迟百分位
	var p50, p95, p99 float64
	if len(latencies) > 0 {
		sorted := make([]LatencyRecord, len(latencies))
		copy(sorted, latencies)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].Latency < sorted[j].Latency
		})
		p50 = latPercentile(sorted, 50)
		p95 = latPercentile(sorted, 95)
		p99 = latPercentile(sorted, 99)
	}

	return Stats{
		Config:             config,
		SteadyDuration:     steadyDuration,
		ConnectionSuccess:  connSuccess,
		ConnectionTotal:    connTotal,
		SentCount:          sentCount,
		DeliveryCount:      deliveryCount,
		ExpectedDeliveries: expectedDeliveries,
		LossPct:            lossPct,
		SendQPS:            sendQPS,
		DeliveryThroughput: deliveryThroughput,
		P50Ms:              p50,
		P95Ms:              p95,
		P99Ms:              p99,
		DrainRemaining:     drainRemaining,
		Errors:             errors,
	}
}

// BenchmarkTracker 是 goroutine 安全的压测数据收集器。
// 必须在启动建连 goroutine 之前创建并传入。
type BenchmarkTracker struct {
	mu         sync.Mutex
	sends      []SendRecord
	deliveries []DeliveryRecord
	latencies  []LatencyRecord
	sendIdx    atomic.Int64 // 全局递增发送序号，用于生成唯一 message_id
}

func (bt *BenchmarkTracker) NextMessageID(talkerID, seq int) MessageID {
	return MessageID{TalkerID: talkerID, Seq: seq, SendIdx: bt.sendIdx.Add(1)}
}

// RecordSend only records a message after WriteMessage succeeds.
func (bt *BenchmarkTracker) RecordSend(id MessageID, sendTime time.Time, expectedClients int) {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	bt.sends = append(bt.sends, SendRecord{
		ID:              id,
		SendTime:        sendTime,
		ExpectedClients: expectedClients,
	})
}

// RecordDelivery 记录一次投递接收。
func (bt *BenchmarkTracker) RecordDelivery(listenerID int, messageID MessageID, receiveTime time.Time) {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	bt.deliveries = append(bt.deliveries, DeliveryRecord{
		ListenerID:  listenerID,
		MessageID:   messageID,
		ReceiveTime: receiveTime,
	})
}

// RecordLatency 记录一条延迟数据。
func (bt *BenchmarkTracker) RecordLatency(lat time.Duration) {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	bt.latencies = append(bt.latencies, LatencyRecord{Latency: lat})
}

// Compute 基于收集的数据计算最终统计。
func (bt *BenchmarkTracker) Compute(steadyStart, steadyEnd time.Time, connSuccess int64, connTotal int, drainRemaining int, errors int64, config string) Stats {
	bt.mu.Lock()
	sends := make([]SendRecord, len(bt.sends))
	copy(sends, bt.sends)
	dels := make([]DeliveryRecord, len(bt.deliveries))
	copy(dels, bt.deliveries)
	lats := make([]LatencyRecord, len(bt.latencies))
	copy(lats, bt.latencies)
	bt.mu.Unlock()

	// Derive latency from the authoritative send table. Delivery may be recorded
	// before RecordSend because reader and writer goroutines run independently.
	sendTimes := make(map[int64]time.Time, len(sends))
	for _, send := range sends {
		sendTimes[send.ID.SendIdx] = send.SendTime
	}
	seen := make(map[[2]int64]struct{}, len(dels))
	for _, delivery := range dels {
		key := [2]int64{int64(delivery.ListenerID), delivery.MessageID.SendIdx}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if sentAt, ok := sendTimes[delivery.MessageID.SendIdx]; ok && !delivery.ReceiveTime.Before(sentAt) {
			lats = append(lats, LatencyRecord{Latency: delivery.ReceiveTime.Sub(sentAt)})
		}
	}

	return ComputeStats(sends, dels, lats, steadyStart, steadyEnd, connSuccess, connTotal, drainRemaining, errors, config)
}
