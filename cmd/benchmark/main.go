// Command benchmark 是对 DanmakuFlow 进行 WebSocket + HTTP 压测的工具。
//
// 使用方式:
//
//	# 先启动服务（另一个终端）
//	cd /root/workplace/claude_learn/danmaku && go run .
//
//	# 基准测试：1000 连接，每秒每条消息间隔
//	go run ./cmd/benchmark -c 1000 -r 1s -room test_room
//
//	# HTTP API 压测
//	go run ./cmd/benchmark -c 200 -r 500ms -http-only
//
// 输出字段说明：
//
//	conns          — 当前已建立的连接数
//	sent/s         — 每秒发出的消息数（稳态窗口内）
//	recv/s         — 每秒收到的消息数（稳态窗口内）
//	bf             — 广播因子（recv/s ÷ sent/s），接近连接数说明广播正常
//	errs           — 累计错误数
//	连接成功率     — 成功连接数 / 总尝试数
//	弹幕发送 QPS   — 稳态窗口内的发送量 / 稳态时长
//	客户端投递吞吐 — 去重后投递数 / 稳态时长
//	精确投递丢失率 — (预期投递总数 - 去重后实际投递数) / 预期投递总数
//	延迟 P50/P95/P99 — 基于 receive_time - send_time 的百分位延迟
//	Drain 后未到达 — 预期投递与去重后投递之差在 drain 结束时仍未收到
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/1012-Penn/DanmakuFlow/model"
)

// 命令行参数
var (
	serverAddr  = flag.String("addr", "localhost:8080", "服务器地址")
	connections = flag.Int("c", 1000, "WS 连接数（总客户端数）")
	rate        = flag.Duration("r", 1*time.Second, "每个 talker 的发送间隔")
	roomID      = flag.String("room", "benchmark", "房间 ID")
	httpOnly    = flag.Bool("http-only", false, "只压 HTTP API，不连 WS")
	talkerRatio = flag.Float64("talker-ratio", 0.1, "发送消息的客户端比例 (0-1)")
	rampDelay   = flag.Duration("ramp", 5*time.Millisecond, "每个连接的建立间隔")
	duration    = flag.Duration("d", 30*time.Second, "测试持续时间")
	drainWait   = flag.Duration("drain", 3*time.Second, "测试结束后的排空等待时间")
	trackMsgs   = flag.Bool("track", true, "启用消息 ID 追踪（精确丢失率和延迟）")
	httpRate    = flag.Int("http-qps", 50, "HTTP API 目标 QPS（仅在 http-only 时使用）")
	verbose     = flag.Bool("v", false, "详细日志（打印每条错误）")
)

// 全局原子计数器
var (
	connCount     atomic.Int64
	msgSentCount  atomic.Int64
	msgRecvCount  atomic.Int64
	errorCount    atomic.Int64
	lastSentCount atomic.Int64
	lastRecvCount atomic.Int64
	httpSentCount atomic.Int64
	httpOKCount   atomic.Int64
	httpErrCount  atomic.Int64
)

// startBarrier 控制所有 talker 同时开始发送。
var startBarrier chan struct{}

// stopBarrier 通知所有 talker 停止发送。
var stopBarrier chan struct{}

// 弹幕内容池
var danmakuContents = []string{
	"前方高能预警！", "233333", "哈哈哈哈哈", "牛逼", "666666",
	"这也太好看了吧", "awsl", "妈妈问我为什么跪着看", "名场面",
	"梦开始的地方", "泪目", "全体起立", "经典永流传",
	"不懂就问这是什么番", "打卡", "二刷报道", "弹幕护体",
	"日常催更（1/1）", "承包这个镜头", "完结撒花",
}

func main() {
	flag.Parse()

	fmt.Println("🎯 DanmakuFlow 压测工具")
	fmt.Printf("   服务器:     %s\n", *serverAddr)
	fmt.Printf("   房间:       %s\n", *roomID)
	fmt.Printf("   连接数:     %d\n", *connections)
	fmt.Printf("   发送间隔:   %s\n", *rate)
	fmt.Printf("   测试时长:   %s\n", *duration)
	fmt.Printf("   排空等待:   %s\n", *drainWait)
	fmt.Printf("   Talker比例: %.0f%%\n", *talkerRatio*100)
	fmt.Printf("   消息追踪:   %v\n", *trackMsgs)
	fmt.Printf("   HTTP Only:  %v\n", *httpOnly)
	fmt.Println(strings.Repeat("─", 50))

	startBarrier = make(chan struct{})
	stopBarrier = make(chan struct{})

	if *httpOnly {
		runHTTPBenchmark()
		return
	}

	runWSBenchmark()
}

// ──────────── WebSocket 压测 ────────────

func runWSBenchmark() {
	talkerCount := int(float64(*connections) * *talkerRatio)
	if talkerCount < 1 && *connections > 0 {
		talkerCount = 1
	}

	fmt.Printf("  其中 Talker: %d (%.0f%%)\n", talkerCount, *talkerRatio*100)
	fmt.Println(strings.Repeat("─", 50))

	// 在启动建连 goroutine 之前初始化 tracker（避免并发写 map）
	tracker := new(BenchmarkTracker)

	startTime := time.Now()
	monitorDone := make(chan struct{})
	go monitorWS(startTime, monitorDone)

	var dialWg sync.WaitGroup
	sem := make(chan struct{}, 200)

	for i := 0; i < *connections; i++ {
		dialWg.Add(1)
		sem <- struct{}{}
		go func(id int) {
			defer func() {
				<-sem
				dialWg.Done()
			}()
			runBot(id, id < talkerCount, tracker)
		}(i)
		time.Sleep(*rampDelay)
	}

	dialWg.Wait()

	connsAtStart := connCount.Load()
	steadyStart := time.Now()
	fmt.Printf("\n✅ 所有连接就绪 (%d), 开始发送...\n", connsAtStart)
	close(startBarrier)

	// 运行测试
	<-time.After(*duration)

	// 通知所有 talker 停止
	steadyEnd := time.Now()
	close(stopBarrier)

	// 排空：停止发送，只接收在途消息
	fmt.Printf("⏳ 排空中 (%s)...\n", *drainWait)
	<-time.After(*drainWait)

	close(monitorDone)

	// 计算统计
	stats := tracker.Compute(steadyStart, steadyEnd, connsAtStart, *connections, 0, errorCount.Load(),
		fmt.Sprintf("c=%d r=%s talker=%.0f%% room=%s", *connections, *rate, *talkerRatio*100, *roomID))

	// 计算排空后仍未到达
	drainRemaining := stats.ExpectedDeliveries - stats.DeliveryCount
	if drainRemaining < 0 {
		drainRemaining = 0
	}
	stats.DrainRemaining = int(drainRemaining)

	printStats(stats)
	saveStats(stats)
}

func runBot(id int, isTalker bool, tracker *BenchmarkTracker) {
	wsURL := fmt.Sprintf("ws://%s/ws?room_id=%s", *serverAddr, *roomID)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		errorCount.Add(1)
		if *verbose {
			log.Printf("[ERR] 连接失败 id=%d: %v", id, err)
		}
		return
	}

	connCount.Add(1)

	// 读 goroutine：持续接收服务端广播
	go func(listenerID int) {
		defer func() {
			connCount.Add(-1)
			conn.Close()
		}()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env model.MessageEnvelope
			if json.Unmarshal(data, &env) != nil || env.Type != model.MsgTypeBroadcast {
				continue
			}
			msgRecvCount.Add(1)

			if *trackMsgs {
				var dm model.Danmaku
				if json.Unmarshal(env.Payload, &dm) != nil {
					continue
				}
				// 解析 "bm{talkerID}_s{seq}_i{sendIdx}" 格式
				var talkerID, seq int
				var sendIdx int64
				if n, _ := fmt.Sscanf(dm.Content, "bm%d_s%d_i%d", &talkerID, &seq, &sendIdx); n == 3 {
					tracker.RecordDelivery(listenerID, MessageID{
						TalkerID: talkerID,
						Seq:      seq,
						SendIdx:  sendIdx,
					}, time.Now())
				}
			}
		}
	}(id)

	// talker goroutine
	if isTalker {
		go func(tID int) {
			<-startBarrier

			ticker := time.NewTicker(*rate)
			defer ticker.Stop()

			for seq := 0; ; seq++ {
				select {
				case <-ticker.C:
					sendTime := time.Now()
					expectedClients := int(connCount.Load())
					msgID := tracker.NextMessageID(tID, seq)
					content := fmt.Sprintf("bm%d_s%d_i%d", tID, seq, msgID.SendIdx)
					req := map[string]any{
						"content":    content,
						"user_id":    fmt.Sprintf("bench_%d", tID),
						"color":      "#ffffff",
						"type":       "scroll",
						"request_id": content,
					}
					payload, err := json.Marshal(req)
					if err != nil {
						errorCount.Add(1)
						continue
					}
					data, err := json.Marshal(model.MessageEnvelope{Type: model.MsgTypeDanmaku, Payload: payload})
					if err != nil {
						errorCount.Add(1)
						continue
					}
					if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
						errorCount.Add(1)
						if *verbose {
							log.Printf("[ERR] 发送失败 id=%d: %v", tID, err)
						}
						return
					}
					tracker.RecordSend(msgID, sendTime, expectedClients)
					msgSentCount.Add(1)

				case <-stopBarrier:
					return
				}
			}
		}(id)
	}
}

// monitorWS 每隔 1 秒输出实时统计。
func monitorWS(startTime time.Time, done chan struct{}) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sent := msgSentCount.Load()
			recv := msgRecvCount.Load()
			lastSent := lastSentCount.Swap(sent)
			lastRecv := lastRecvCount.Swap(recv)
			sentPS := sent - lastSent
			recvPS := recv - lastRecv
			errs := errorCount.Load()
			conns := connCount.Load()

			fmt.Printf("\r📊 %s | conns:%-5d | sent/s:%-4d | recv/s:%-6d | bf:%-6.1f | errs:%-4d",
				time.Since(startTime).Round(time.Second), conns, sentPS, recvPS, bf(recvPS, sentPS), errs)

		case <-done:
			fmt.Println()
			return
		}
	}
}

func bf(recvPS, sentPS int64) float64 {
	if sentPS > 0 {
		return float64(recvPS) / float64(sentPS)
	}
	return 0
}

func printStats(s Stats) {
	fmt.Println(strings.Repeat("═", 50))
	fmt.Println("📋 测试结果汇总")
	fmt.Println(strings.Repeat("─", 50))
	fmt.Printf("  配置:            %s\n", s.Config)
	fmt.Printf("  稳态窗口:        %v\n", s.SteadyDuration)
	fmt.Printf("  连接总数:        %d\n", s.ConnectionSuccess)
	fmt.Printf("  连接成功率:      %.1f%%\n", float64(s.ConnectionSuccess)/float64(s.ConnectionTotal)*100)
	fmt.Printf("  弹幕发送量:      %d\n", s.SentCount)
	fmt.Printf("  弹幕发送 QPS:    %.0f msg/s（稳态窗口）\n", s.SendQPS)
	fmt.Printf("  预期投递总数:    %d\n", s.ExpectedDeliveries)
	fmt.Printf("  去重后投递数:    %d\n", s.DeliveryCount)
	fmt.Printf("  客户端投递吞吐:  %.0f msg/s\n", s.DeliveryThroughput)
	fmt.Printf("  精确投递丢失率:  %.4f%%\n", s.LossPct)
	if *trackMsgs {
		fmt.Printf("  延迟 P50:         %.2f ms\n", s.P50Ms)
		fmt.Printf("  延迟 P95:         %.2f ms\n", s.P95Ms)
		fmt.Printf("  延迟 P99:         %.2f ms\n", s.P99Ms)
	}
	fmt.Printf("  Drain 后未到达:  %d\n", s.DrainRemaining)
	fmt.Printf("  总错误数:        %d\n", s.Errors)
	fmt.Println(strings.Repeat("═", 50))
}

func saveStats(s Stats) {
	ts := time.Now().Format("2006-01-02 15:04:05")
	line := fmt.Sprintf("[%s] %s | steady=%v conns=%d sent=%d recv=%d expected=%d loss=%.4f%% p50=%.2f p95=%.2f p99=%.2f drain=%d errs=%d\n",
		ts, s.Config, s.SteadyDuration, s.ConnectionSuccess, s.SentCount, s.DeliveryCount,
		s.ExpectedDeliveries, s.LossPct, s.P50Ms, s.P95Ms, s.P99Ms, s.DrainRemaining, s.Errors)

	f, err := os.OpenFile("benchmark_results.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("⚠️ 无法写入结果文件: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		log.Printf("⚠️ 写入结果失败: %v", err)
	}
	fmt.Printf("📝 结果已保存到 benchmark_results.log\n")
}

// ──────────── HTTP API 压测 ────────────

func runHTTPBenchmark() {
	startTime := time.Now()
	endTime := startTime.Add(*duration)

	interval := max(time.Second/time.Duration(*httpRate), time.Millisecond)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var wg sync.WaitGroup
	done := make(chan struct{})

	go func() {
		reportTicker := time.NewTicker(1 * time.Second)
		defer reportTicker.Stop()
		for {
			select {
			case <-reportTicker.C:
				sent := httpSentCount.Load()
				ok := httpOKCount.Load()
				errs := httpErrCount.Load()
				e := time.Since(startTime)
				fmt.Printf("\r📊 HTTP | %s | req:%-5d | ok:%-5d | errs:%-4d | qps:%.0f",
					e.Round(time.Second), sent, ok, errs, float64(sent)/e.Seconds())
			case <-done:
				return
			}
		}
	}()

	for range ticker.C {
		if time.Now().After(endTime) {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sendHTTP()
		}()
	}

	wg.Wait()
	close(done)
	fmt.Println()

	elapsed := time.Since(startTime)
	sent := httpSentCount.Load()
	ok := httpOKCount.Load()
	errs := httpErrCount.Load()

	fmt.Println(strings.Repeat("═", 50))
	fmt.Println("📋 HTTP 压测结果汇总")
	fmt.Println(strings.Repeat("─", 50))
	fmt.Printf("  目标 QPS:    %d\n", *httpRate)
	fmt.Printf("  持续时间:    %v\n", elapsed)
	fmt.Printf("  请求数:      %d\n", sent)
	fmt.Printf("  成功数:      %d\n", ok)
	fmt.Printf("  错误数:      %d\n", errs)
	fmt.Printf("  实际 QPS:    %.0f\n", float64(sent)/elapsed.Seconds())
	fmt.Printf("  成功率:      %.1f%%\n", float64(ok)/float64(sent)*100)
	fmt.Println(strings.Repeat("═", 50))
}

func sendHTTP() {
	url := fmt.Sprintf("http://%s/api/room/%s/danmaku", *serverAddr, *roomID)
	userID := fmt.Sprintf("http_bench_%d", time.Now().UnixNano()%1000)

	body := map[string]string{
		"content": danmakuContents[time.Now().UnixNano()%int64(len(danmakuContents))],
		"user_id": userID,
		"color":   "#ffffff",
		"type":    "scroll",
	}
	data, _ := json.Marshal(body)

	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	httpSentCount.Add(1)
	if err != nil {
		httpErrCount.Add(1)
		return
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		httpOKCount.Add(1)
	} else {
		httpErrCount.Add(1)
	}
}

// compile-time assertion that benchmark builds correctly
var _ = time.Second
