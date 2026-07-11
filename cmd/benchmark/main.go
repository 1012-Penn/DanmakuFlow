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
//	# 大连接数测试
//	go run ./cmd/benchmark -c 5000 -r 2s
//
// 输出字段:
//
//	conns      — 当前已建立的连接数
//	sent/s     — 每秒发出的消息数
//	recv/s     — 每秒收到的消息数
//	lag        — 发送与接收之间的差值（积压）
//	loss       — 累计丢失率
//	errs       — 累计错误数
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
	httpRate    = flag.Int("http-qps", 50, "HTTP API 目标 QPS（仅在 http-only 时使用）")
	verbose     = flag.Bool("v", false, "详细日志（打印每条错误）")
)

// 全局原子计数器
var (
	connCount     atomic.Int64 // 成功连接数
	msgSentCount  atomic.Int64 // 已发送消息数
	msgRecvCount  atomic.Int64 // 已接收消息数 (WS)
	errorCount    atomic.Int64 // 错误总数
	lastSentCount atomic.Int64 // 上一秒的发送计数（用于计算速率）
	lastRecvCount atomic.Int64 // 上一秒的接收计数
	httpSentCount atomic.Int64 // HTTP 发送计数
	httpOKCount   atomic.Int64 // HTTP 成功响应数
	httpErrCount  atomic.Int64 // HTTP 错误数
)

// 弹幕内容池（随机选取）
var danmakuContents = []string{
	"前方高能预警！", "233333", "哈哈哈哈哈", "牛逼", "666666",
	"这也太好看了吧", "awsl", "妈妈问我为什么跪着看", "名场面",
	"梦开始的地方", "泪目", "全体起立", "经典永流传",
	"不懂就问这是什么番", "打卡", "二刷报道", "弹幕护体",
	"日常催更（1/1）", "承包这个镜头", "完结撒花",
}

// benchmarkResult 保存单次测试的汇总结果
type benchmarkResult struct {
	Config     string
	Duration   time.Duration
	Connections int64
	MsgSent    int64
	MsgRecv    int64
	Errors     int64
	LossRate   float64
	Throughput float64
}

func main() {
	flag.Parse()

	fmt.Println("🎯 DanmakuFlow 压测工具")
	fmt.Printf("   服务器:     %s\n", *serverAddr)
	fmt.Printf("   房间:       %s\n", *roomID)
	fmt.Printf("   连接数:     %d\n", *connections)
	fmt.Printf("   发送间隔:   %s\n", *rate)
	fmt.Printf("   测试时长:   %s\n", *duration)
	fmt.Printf("   Talker比例: %.0f%%\n", *talkerRatio*100)
	fmt.Printf("   HTTP Only:  %v\n", *httpOnly)
	fmt.Println(strings.Repeat("─", 50))

	if *httpOnly {
		runHTTPBenchmark()
		return
	}

	runWSBenchmark()
}

// ──────────── WebSocket 压测 ────────────

func runWSBenchmark() {
	startTime := time.Now()

	talkerCount := int(float64(*connections) * *talkerRatio)
	if talkerCount < 1 && *connections > 0 {
		talkerCount = 1
	}

	fmt.Printf("  其中 Talker: %d (%.0f%%)\n", talkerCount, *talkerRatio*100)
	fmt.Println(strings.Repeat("─", 50))

	// Monitor goroutine（每秒打印实时统计）
	monitorDone := make(chan struct{})
	go monitorWS(startTime, monitorDone)

	// 并行建立连接（不等待 bot 结束——bot 会一直阻塞到测试终结）
	var dialWg sync.WaitGroup
	sem := make(chan struct{}, 200) // 限制并发 dial 数

	for i := 0; i < *connections; i++ {
		dialWg.Add(1)
		sem <- struct{}{}
		go func(id int) {
			defer func() {
				<-sem
				dialWg.Done()
			}()
			isTalker := id < talkerCount
			runBot(id, isTalker)
		}(i)
		time.Sleep(*rampDelay)
	}

	dialWg.Wait() // 只等 dial 完成，不等 bot 退出

	// 确认所有连接就绪后，运行测试直到 duration 结束
	<-time.After(*duration)

	close(monitorDone)

	// 汇总输出
	elapsed := time.Since(startTime)
	r := summarize(elapsed)
	printResult(r)
	saveResult(r)
}

func runBot(id int, isTalker bool) {
	wsURL := fmt.Sprintf("ws://%s/ws?room_id=%s", *serverAddr, *roomID)
	userID := fmt.Sprintf("bench_%d", id)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		errorCount.Add(1)
		if *verbose {
			log.Printf("[ERR] 连接失败 id=%d: %v", id, err)
		}
		return
	}

	connCount.Add(1)

	// 读 goroutine：持续接收服务端广播直到连接断开
	go func() {
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
			msgRecvCount.Add(1)
		}
	}()

	// 如果是 talker，启动发弹幕 goroutine
	if isTalker {
		go func() {
			msgIdx := 0
			ticker := time.NewTicker(*rate)
			defer ticker.Stop()
			defer conn.Close()

			for range ticker.C {
				content := danmakuContents[msgIdx%len(danmakuContents)]
				msg := map[string]any{
					"content": content,
					"user_id": userID,
					"color":   "#ffffff",
					"type":    "scroll",
				}
				data, err := json.Marshal(msg)
				if err != nil {
					errorCount.Add(1)
					continue
				}
				if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
					errorCount.Add(1)
					if *verbose {
						log.Printf("[ERR] 发送失败 id=%d: %v", id, err)
					}
					return
				}
				msgSentCount.Add(1)
				msgIdx++
			}
		}()
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

			var bf float64
			if sentPS > 0 {
				bf = float64(recvPS) / float64(sentPS)
			}

			// 丢包 = 预期应收 (sent×conns) 与实际收的差距
			expectedRecv := sent * conns
			var loss float64
			if expectedRecv > 0 {
				loss = float64(expectedRecv-recv) / float64(expectedRecv) * 100
				if loss < 0 {
					loss = 0
				}
			}

			elapsed := time.Since(startTime)
			fmt.Printf("\r📊 %s | conns:%-5d | sent/s:%-4d | recv/s:%-6d | bf:%-6.1f | loss:%.2f%% | errs:%-4d",
				elapsed.Round(time.Second), conns, sentPS, recvPS, bf, loss, errs)

		case <-done:
			fmt.Println()
			return
		}
	}
}

func summarize(elapsed time.Duration) benchmarkResult {
	sent := msgSentCount.Load()
	recv := msgRecvCount.Load()
	errs := errorCount.Load()
	conns := connCount.Load()

	expectedRecv := sent * conns
	var loss float64
	if expectedRecv > 0 {
		loss = float64(expectedRecv-recv) / float64(expectedRecv) * 100
		if loss < 0 {
			loss = 0
		}
	}
	throughput := float64(recv) / elapsed.Seconds()

	return benchmarkResult{
		Config:      fmt.Sprintf("c=%d r=%s talker=%.0f%% room=%s", *connections, *rate, *talkerRatio*100, *roomID),
		Duration:    elapsed,
		Connections: conns,
		MsgSent:     sent,
		MsgRecv:     recv,
		Errors:      errs,
		LossRate:    loss,
		Throughput:  throughput,
	}
}

func printResult(r benchmarkResult) {
	fmt.Println(strings.Repeat("═", 50))
	fmt.Println("📋 测试结果汇总")
	fmt.Println(strings.Repeat("─", 50))
	fmt.Printf("  配置:        %s\n", r.Config)
	fmt.Printf("  持续时间:    %v\n", r.Duration)
	fmt.Printf("  连接数:      %d\n", r.Connections)
	fmt.Printf("  发送消息:    %d\n", r.MsgSent)
	fmt.Printf("  接收消息:    %d\n", r.MsgRecv)
	fmt.Printf("  总错误数:    %d\n", r.Errors)
	if r.LossRate > 0 {
		fmt.Printf("  丢消息率:    %.2f%%\n", r.LossRate)
	}
	fmt.Printf("  吞吐量:      %.0f msg/s\n", r.Throughput)
	fmt.Println(strings.Repeat("═", 50))
}

// saveResult 将结果追加到 benchmark_results.log。
func saveResult(r benchmarkResult) {
	ts := time.Now().Format("2006-01-02 15:04:05")
	line := fmt.Sprintf("[%s] %s | duration=%v conns=%d sent=%d recv=%d loss=%.2f%% throughput=%.0f/s errs=%d\n",
		ts, r.Config, r.Duration, r.Connections, r.MsgSent, r.MsgRecv,
		r.LossRate, r.Throughput, r.Errors)

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

	// 控制发送速率
	interval := max(time.Second/time.Duration(*httpRate), time.Millisecond)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var wg sync.WaitGroup
	done := make(chan struct{})

	// Monitor 每秒输出
	go func() {
		reportTicker := time.NewTicker(1 * time.Second)
		defer reportTicker.Stop()
		for {
			select {
			case <-reportTicker.C:
				sent := httpSentCount.Load()
				ok := httpOKCount.Load()
				errs := httpErrCount.Load()
				elapsed := time.Since(startTime)
				fmt.Printf("\r📊 HTTP | %s | req:%-5d | ok:%-5d | errs:%-4d | qps:%.0f",
					elapsed.Round(time.Second), sent, ok, errs, float64(sent)/elapsed.Seconds())
			case <-done:
				return
			}
		}
	}()

	// 发请求
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

	// 汇总
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
