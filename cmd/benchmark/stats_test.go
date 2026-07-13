package main

import (
	"testing"
	"time"
)

func TestComputeStatsNormal(t *testing.T) {
	// 正常场景：3 条消息，每条预期投递给 5 个客户端，全部收到
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	steadyStart := now.Add(-10 * time.Second)
	steadyEnd := now

	sends := []SendRecord{
		{ID: MessageID{TalkerID: 0, Seq: 1, SendIdx: 1}, SendTime: now.Add(-8 * time.Second), ExpectedClients: 5},
		{ID: MessageID{TalkerID: 0, Seq: 2, SendIdx: 2}, SendTime: now.Add(-5 * time.Second), ExpectedClients: 5},
		{ID: MessageID{TalkerID: 1, Seq: 1, SendIdx: 3}, SendTime: now.Add(-2 * time.Second), ExpectedClients: 5},
	}

	var deliveries []DeliveryRecord
	for _, s := range sends {
		for listener := 0; listener < 5; listener++ {
			deliveries = append(deliveries, DeliveryRecord{
				ListenerID:  listener,
				MessageID:   s.ID,
				ReceiveTime: s.SendTime.Add(10 * time.Millisecond),
			})
		}
	}

	stats := ComputeStats(sends, deliveries, nil, steadyStart, steadyEnd, 5, 5, 0, 0, "test")

	if stats.SentCount != 3 {
		t.Errorf("SentCount: 期望 3，实际 %d", stats.SentCount)
	}
	expectedDel := 3 * 5 // 15
	if stats.ExpectedDeliveries != int64(expectedDel) {
		t.Errorf("ExpectedDeliveries: 期望 %d，实际 %d", expectedDel, stats.ExpectedDeliveries)
	}
	if stats.DeliveryCount != int64(expectedDel) {
		t.Errorf("DeliveryCount: 期望 %d，实际 %d", expectedDel, stats.DeliveryCount)
	}
	if stats.LossPct != 0 {
		t.Errorf("LossPct: 期望 0，实际 %.4f", stats.LossPct)
	}
}

func TestComputeStatsWithLoss(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	steadyStart := now.Add(-10 * time.Second)
	steadyEnd := now

	sends := []SendRecord{
		{ID: MessageID{TalkerID: 0, Seq: 1, SendIdx: 1}, SendTime: now.Add(-5 * time.Second), ExpectedClients: 4},
		{ID: MessageID{TalkerID: 0, Seq: 2, SendIdx: 2}, SendTime: now.Add(-3 * time.Second), ExpectedClients: 4},
	}

	// 只收到 5 个投递（预期 8 个）
	deliveries := []DeliveryRecord{
		{ListenerID: 0, MessageID: sends[0].ID, ReceiveTime: now.Add(-4*time.Second + 10*time.Millisecond)},
		{ListenerID: 1, MessageID: sends[0].ID, ReceiveTime: now.Add(-4*time.Second + 10*time.Millisecond)},
		{ListenerID: 2, MessageID: sends[0].ID, ReceiveTime: now.Add(-4*time.Second + 10*time.Millisecond)},
		{ListenerID: 0, MessageID: sends[1].ID, ReceiveTime: now.Add(-2*time.Second + 15*time.Millisecond)},
		{ListenerID: 1, MessageID: sends[1].ID, ReceiveTime: now.Add(-2*time.Second + 15*time.Millisecond)},
	}

	stats := ComputeStats(sends, deliveries, nil, steadyStart, steadyEnd, 4, 4, 0, 0, "test")

	if stats.ExpectedDeliveries != 8 {
		t.Errorf("ExpectedDeliveries: 期望 8，实际 %d", stats.ExpectedDeliveries)
	}
	if stats.DeliveryCount != 5 {
		t.Errorf("DeliveryCount: 期望 5，实际 %d", stats.DeliveryCount)
	}
	// LossRate = (8-5)/8 = 37.5%
	expectedLoss := 37.5
	if stats.LossPct != expectedLoss {
		t.Errorf("LossPct: 期望 %.4f%%，实际 %.4f%%", expectedLoss, stats.LossPct)
	}
}

func TestComputeStatsDedup(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	steadyStart := now.Add(-10 * time.Second)
	steadyEnd := now

	sends := []SendRecord{
		{ID: MessageID{TalkerID: 0, Seq: 1, SendIdx: 1}, SendTime: now.Add(-5 * time.Second), ExpectedClients: 3},
	}

	// listener 0 收到两次相同的消息（重复投递）
	deliveries := []DeliveryRecord{
		{ListenerID: 0, MessageID: sends[0].ID, ReceiveTime: now.Add(-4*time.Second + 10*time.Millisecond)},
		{ListenerID: 0, MessageID: sends[0].ID, ReceiveTime: now.Add(-4*time.Second + 20*time.Millisecond)}, // duplicate
		{ListenerID: 1, MessageID: sends[0].ID, ReceiveTime: now.Add(-4*time.Second + 15*time.Millisecond)},
		{ListenerID: 2, MessageID: sends[0].ID, ReceiveTime: now.Add(-4*time.Second + 15*time.Millisecond)},
	}

	stats := ComputeStats(sends, deliveries, nil, steadyStart, steadyEnd, 3, 3, 0, 0, "test")

	// After dedup: 3 unique (listener, message_id) pairs
	if stats.DeliveryCount != 3 {
		t.Errorf("去重后 DeliveryCount: 期望 3，实际 %d", stats.DeliveryCount)
	}
}

func TestComputeStatsLatency(t *testing.T) {
	latencies := []LatencyRecord{
		{Latency: 10 * time.Millisecond},
		{Latency: 20 * time.Millisecond},
		{Latency: 30 * time.Millisecond},
		{Latency: 40 * time.Millisecond},
		{Latency: 50 * time.Millisecond},
		{Latency: 60 * time.Millisecond},
		{Latency: 70 * time.Millisecond},
		{Latency: 80 * time.Millisecond},
		{Latency: 90 * time.Millisecond},
		{Latency: 100 * time.Millisecond},
	}

	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	steadyStart := now.Add(-10 * time.Second)
	steadyEnd := now

	stats := ComputeStats(nil, nil, latencies, steadyStart, steadyEnd, 0, 0, 0, 0, "test")

	if stats.P50Ms < 49 || stats.P50Ms > 51 {
		t.Errorf("P50: 期望 ~50ms，实际 %.2f", stats.P50Ms)
	}
	if stats.P95Ms < 95 || stats.P95Ms > 101 {
		t.Errorf("P95: 期望 ~95-100ms，实际 %.2f", stats.P95Ms)
	}
	if stats.P99Ms < 99 || stats.P99Ms > 101 {
		t.Errorf("P99: 期望 ~100ms，实际 %.2f", stats.P99Ms)
	}
}

func TestComputeStatsSteadyWindow(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	steadyStart := now.Add(-5 * time.Second)
	steadyEnd := now

	// 只有 steady 窗口内的消息计入发送 QPS
	sends := []SendRecord{
		{ID: MessageID{SendIdx: 1}, SendTime: now.Add(-10 * time.Second), ExpectedClients: 5}, // before window
		{ID: MessageID{SendIdx: 2}, SendTime: now.Add(-4 * time.Second), ExpectedClients: 5},  // in window
		{ID: MessageID{SendIdx: 3}, SendTime: now.Add(-3 * time.Second), ExpectedClients: 5},  // in window
		{ID: MessageID{SendIdx: 4}, SendTime: now.Add(-2 * time.Second), ExpectedClients: 5},  // in window
		{ID: MessageID{SendIdx: 5}, SendTime: now.Add(-1 * time.Second), ExpectedClients: 5},  // in window
		{ID: MessageID{SendIdx: 6}, SendTime: now.Add(1 * time.Second), ExpectedClients: 5},   // after window
	}

	stats := ComputeStats(sends, nil, nil, steadyStart, steadyEnd, 5, 5, 0, 0, "test")

	// 稳态窗口 5 秒，4 条消息
	if stats.SentCount != 6 {
		t.Errorf("SentCount: 期望 6，实际 %d", stats.SentCount)
	}
	expectedQPS := 4.0 / 5.0 // 0.8
	if stats.SendQPS < expectedQPS-0.1 || stats.SendQPS > expectedQPS+0.1 {
		t.Errorf("SendQPS: 期望 ~0.8，实际 %.4f", stats.SendQPS)
	}
}

func TestComputeStatsDrainRemaining(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	steadyStart := now.Add(-10 * time.Second)
	steadyEnd := now

	sends := []SendRecord{
		{ID: MessageID{TalkerID: 0, Seq: 1, SendIdx: 1}, SendTime: now.Add(-5 * time.Second), ExpectedClients: 3},
	}

	// 只有 1 个投递（预期 3 个），2 个在 drain 后丢失
	deliveries := []DeliveryRecord{
		{ListenerID: 0, MessageID: sends[0].ID, ReceiveTime: now.Add(-4*time.Second + 10*time.Millisecond)},
	}

	stats := ComputeStats(sends, deliveries, nil, steadyStart, steadyEnd, 3, 3, 2, 0, "test")

	if stats.DrainRemaining != 2 {
		t.Errorf("DrainRemaining: 期望 2，实际 %d", stats.DrainRemaining)
	}
	if stats.DeliveryCount != 1 {
		t.Errorf("DeliveryCount: 期望 1，实际 %d", stats.DeliveryCount)
	}
}
