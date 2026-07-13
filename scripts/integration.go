// Command integration 验证 DanmakuFlow 双实例跨实例广播功能。
//
// 使用方式:
//
//	# 先启动双实例:
//	docker compose up --build -d
//
//	# 运行集成测试:
//	go run ./scripts/integration.go
//
// 验证项:
//  1. 两个实例拥有不同的 instance ID
//  2. 连接 A(端口8081) → 房间X、连接 B(端口8082) → 房间X → A 发消息 B 能收到
//  3. 房间隔离：不同房间的消息不串
//  4. 通过 Nginx(端口8080) 也能成功建立 WebSocket
//  5. /healthz 和 /readyz 返回正常
//  6. /metrics 包含 danmakuflow_ 前缀指标
//
// Redis 降级测试（可选）:
//   - 如需测试 Redis 降级行为，可手动暂停 Redis 容器
//
// 当前限制:
//   - Redis 订阅断开后不会自动重连。如需测试降级，需要等待测试失败的超时。
//   - 恢复 Redis 后订阅不会自动恢复，需要重启实例。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	instanceA  = "http://localhost:8081"
	instanceB  = "http://localhost:8082"
	nginxEntry = "http://localhost:8080"

	timeout = 5 * time.Second
)

func main() {
	fmt.Println("🧪 DanmakuFlow 双实例集成测试")
	fmt.Println(strings.Repeat("─", 50))
	exitCode := 0

	// 1. 检查实例健康状态
	fmt.Println("\n1️⃣  检查实例健康状态")
	if !checkHealth(instanceA, "danmaku-a") {
		exitCode = 1
	}
	if !checkHealth(instanceB, "danmaku-b") {
		exitCode = 1
	}
	if !checkHealth(nginxEntry, "nginx") {
		exitCode = 1
	}

	// 2. 检查 /readyz
	fmt.Println("\n2️⃣  检查 /readyz 就绪状态")
	if !checkReady(instanceA, "danmaku-a") {
		exitCode = 1
	}
	if !checkReady(instanceB, "danmaku-b") {
		exitCode = 1
	}

	// 3. 检查 /metrics
	fmt.Println("\n3️⃣  检查 /metrics 端点")
	if !checkMetrics(instanceA, "danmaku-a") {
		exitCode = 1
	}
	if !checkMetrics(instanceB, "danmaku-b") {
		exitCode = 1
	}

	// 4. 验证两个实例有不同的 instance ID
	fmt.Println("\n4️⃣  验证不同实例 ID")
	if !checkDifferentInstanceIDs() {
		exitCode = 1
	}

	// 5. 验证跨实例消息投递
	fmt.Println("\n5️⃣  验证跨实例消息投递")
	if !testCrossInstanceDelivery() {
		exitCode = 1
	}

	// 6. 验证房间隔离
	fmt.Println("\n6️⃣  验证房间隔离")
	if !testRoomIsolation() {
		exitCode = 1
	}

	// 7. 验证 Nginx WebSocket 代理
	fmt.Println("\n7️⃣  验证 Nginx WebSocket 代理")
	if !testNginxWebSocket() {
		exitCode = 1
	}

	fmt.Println(strings.Repeat("═", 50))
	if exitCode == 0 {
		fmt.Println("✅ 所有集成测试通过!")
	} else {
		fmt.Printf("❌ 部分测试失败 (exit=%d)\n", exitCode)
	}
	os.Exit(exitCode)
}

func checkHealth(baseURL, name string) bool {
	resp, err := http.Get(baseURL + "/healthz")
	if err != nil {
		fmt.Printf("  ❌ %s: 无法连接 (%v)\n", name, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("  ❌ %s: 返回 %d\n", name, resp.StatusCode)
		return false
	}
	var body map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		fmt.Printf("  ❌ %s: status=%v\n", name, body["status"])
		return false
	}
	fmt.Printf("  ✅ %s (instance=%v)\n", name, body["instance_id"])
	return true
}

func checkReady(baseURL, name string) bool {
	resp, err := http.Get(baseURL + "/readyz")
	if err != nil {
		fmt.Printf("  ❌ %s: 无法连接 (%v)\n", name, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("  ❌ %s: 返回 %d\n", name, resp.StatusCode)
		return false
	}
	var body map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&body)
	status := body["status"]
	if status != "ok" && status != "degraded" {
		fmt.Printf("  ❌ %s: 未预期的 status=%v\n", name, status)
		return false
	}
	fmt.Printf("  ✅ %s (status=%v, deps=%v)\n", name, status, body["dependencies"])
	return true
}

func checkMetrics(baseURL, name string) bool {
	resp, err := http.Get(baseURL + "/metrics")
	if err != nil {
		fmt.Printf("  ❌ %s: 无法连接 (%v)\n", name, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("  ❌ %s: 返回 %d\n", name, resp.StatusCode)
		return false
	}
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	if !strings.Contains(body, "danmakuflow_") {
		fmt.Printf("  ❌ %s: 缺少 danmakuflow_ 前缀指标\n", name)
		return false
	}
	fmt.Printf("  ✅ %s (含 danmakuflow_ 指标)\n", name)
	return true
}

func checkDifferentInstanceIDs() bool {
	getID := func(url string) string {
		resp, err := http.Get(url + "/healthz")
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		var body map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&body)
		id, _ := body["instance_id"].(string)
		return id
	}

	idA := getID(instanceA)
	idB := getID(instanceB)

	if idA == "" || idB == "" {
		fmt.Printf("  ❌ 无法获取 instance ID (A=%q, B=%q)\n", idA, idB)
		return false
	}
	if idA == idB {
		fmt.Printf("  ❌ 两个实例 ID 相同: %s\n", idA)
		return false
	}
	fmt.Printf("  ✅ A=%s\n", idA)
	fmt.Printf("  ✅ B=%s\n", idB)
	return true
}

func testCrossInstanceDelivery() bool {
	// 连接 A 到房间 "cross_test"
	wsA, err := dialWS(instanceA, "cross_test")
	if err != nil {
		fmt.Printf("  ❌ 连接 danmaku-a 失败: %v\n", err)
		return false
	}
	defer wsA.Close()

	// 连接 B 到相同房间
	wsB, err := dialWS(instanceB, "cross_test")
	if err != nil {
		fmt.Printf("  ❌ 连接 danmaku-b 失败: %v\n", err)
		return false
	}
	defer wsB.Close()

	// 等待 Redis 订阅建立 + 注册完成
	time.Sleep(time.Second)

	// A 发送唯一消息
	uniqueMsg := fmt.Sprintf("cross_msg_%d", time.Now().UnixNano())
	payload := fmt.Sprintf(`{"content":"%s","user_id":"cross_test","color":"#fff","type":"scroll"}`, uniqueMsg)
	if err := wsA.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		fmt.Printf("  ❌ A 发送失败: %v\n", err)
		return false
	}

	// B 可能在超时内收到多条消息（ACK 或 broadcast），找到包含目标内容的那条
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		wsB.SetReadDeadline(deadline)
		_, data, err := wsB.ReadMessage()
		if err != nil {
			break
		}
		var env map[string]interface{}
		json.Unmarshal(data, &env)
		// 新协议：广播消息为 {"type":"broadcast","payload":{...}}
		if msgType, _ := env["type"].(string); msgType == "broadcast" {
			payloadRaw, ok := env["payload"].(map[string]interface{})
			if !ok {
				continue
			}
			content, _ := payloadRaw["content"].(string)
			if content == uniqueMsg {
				fmt.Printf("  ✅ A→B 跨实例投递成功 (content=%q)\n", content)
				return true
			}
		}
	}
	fmt.Printf("  ❌ B 未在 %v 内收到 A 的消息\n", timeout)
	fmt.Printf("  ⚠️  如果 Redis 正在运行，预期 B 应收到跨实例广播\n")
	fmt.Printf("  ⚠️  当前实现中 Redis 订阅断开不会自动重连\n")
	return false
}

func testRoomIsolation() bool {
	// 连接 A 到房间 "iso_room1"
	wsA1, err := dialWS(instanceA, "iso_room1")
	if err != nil {
		fmt.Printf("  ❌ 连接 A-iso_room1 失败: %v\n", err)
		return false
	}
	defer wsA1.Close()

	// 连接 B 到房间 "iso_room2"（不同房间）
	wsB2, err := dialWS(instanceB, "iso_room2")
	if err != nil {
		fmt.Printf("  ❌ 连接 B-iso_room2 失败: %v\n", err)
		return false
	}
	defer wsB2.Close()

	time.Sleep(500 * time.Millisecond)

	// A 在 room1 发消息
	msg := fmt.Sprintf("iso_only_%d", time.Now().UnixNano())
	payload := fmt.Sprintf(`{"content":"%s","user_id":"iso_test"}`, msg)
	if err := wsA1.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		fmt.Printf("  ❌ A 发送失败: %v\n", err)
		return false
	}

	// B 在 room2 不应该收到 room1 的消息。
	// B 可能收到自己 room2 的消息（无），或心跳等，但不应收到 iso_only 消息。
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		wsB2.SetReadDeadline(deadline)
		_, data, err := wsB2.ReadMessage()
		if err != nil {
			break
		}
		var env map[string]interface{}
		json.Unmarshal(data, &env)
		if msgType, _ := env["type"].(string); msgType == "broadcast" {
			payloadRaw, ok := env["payload"].(map[string]interface{})
			if !ok {
				continue
			}
			content, _ := payloadRaw["content"].(string)
			if content == msg {
				fmt.Printf("  ❌ room2 的 B 不应收到 room1 的消息（房间隔离失败）\n")
				return false
			}
		}
	}
	// 如果 B 什么都没收到或只收到非 broadcast 消息，隔离正常
	fmt.Printf("  ✅ 房间隔离正常 (room1 消息未到达 room2)\n")
	return true
}

func testNginxWebSocket() bool {
	ws, err := dialWS(nginxEntry, "nginx_test")
	if err != nil {
		fmt.Printf("  ❌ 通过 Nginx 连接失败: %v\n", err)
		return false
	}
	defer ws.Close()

	// 发一条消息验证双向通信
	msg := `{"content":"nginx_test_msg","user_id":"nginx_test"}`
	if err := ws.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
		fmt.Printf("  ❌ Nginx WS 发送失败: %v\n", err)
		return false
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ws.SetReadDeadline(deadline)
		_, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		var env map[string]interface{}
		json.Unmarshal(data, &env)
		if msgType, _ := env["type"].(string); msgType == "broadcast" {
			payloadRaw, ok := env["payload"].(map[string]interface{})
			if !ok {
				continue
			}
			content, _ := payloadRaw["content"].(string)
			if content == "nginx_test_msg" {
				fmt.Printf("  ✅ Nginx WebSocket 代理正常\n")
				return true
			}
		}
		if msgType, _ := env["type"].(string); msgType == "ack" {
			fmt.Printf("  ⚠️  通过 Nginx 收到 ACK (等待 broadcast)\n")
		}
	}
	fmt.Printf("  ❌ 通过 Nginx 未收到广播\n")
	return false
}

func dialWS(baseURL, roomID string) (*websocket.Conn, error) {
	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/ws?room_id=" + roomID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	return conn, err
}
