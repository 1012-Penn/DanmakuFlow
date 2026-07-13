// Command integration 验证 DanmakuFlow 双实例跨实例广播功能。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	instanceA  = "http://localhost:8081"
	instanceB  = "http://localhost:8082"
	nginxEntry = "http://localhost:8080"
	timeout    = 5 * time.Second
	benchUser  = "integration"
	benchPass  = "intgpass123"
)

type testSetup struct {
	token  string
	roomID string
}

func main() {
	fmt.Println("Test: DanmakuFlow Dual-Instance Integration")
	fmt.Println(strings.Repeat("-", 50))
	exitCode := 0

	fmt.Println("\n1. Health checks")
	if !checkHealth(instanceA, "a") {
		exitCode = 1
	}
	if !checkHealth(instanceB, "b") {
		exitCode = 1
	}
	if !checkHealth(nginxEntry, "nginx") {
		exitCode = 1
	}

	fmt.Println("\n2. Ready checks")
	if !checkReady(instanceA, "a") {
		exitCode = 1
	}
	if !checkReady(instanceB, "b") {
		exitCode = 1
	}

	fmt.Println("\n3. Metrics")
	if !checkMetrics(instanceA, "a") {
		exitCode = 1
	}
	if !checkMetrics(instanceB, "b") {
		exitCode = 1
	}

	fmt.Println("\n4. Different instance IDs")
	if !checkDifferentInstanceIDs() {
		exitCode = 1
	}

	fmt.Println("\n5. Setup test room")
	ts, err := ensureSetup()
	if err != nil {
		fmt.Printf("  FAIL: setup: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n6. Cross-instance delivery")
	if !testCrossInstanceDelivery(ts) {
		exitCode = 1
	}

	fmt.Println("\n7. Room isolation")
	if !testRoomIsolation(ts) {
		exitCode = 1
	}

	fmt.Println("\n8. Nginx WebSocket proxy")
	if !testNginxWebSocket(ts) {
		exitCode = 1
	}

	fmt.Println("\n9. Reconnect history")
	if !testReconnectHistory(ts) {
		exitCode = 1
	}

	fmt.Println(strings.Repeat("=", 50))
	if exitCode == 0 {
		fmt.Println("ALL TESTS PASSED")
	} else {
		fmt.Printf("SOME TESTS FAILED (exit=%d)\n", exitCode)
	}
	os.Exit(exitCode)
}

func ensureSetup() (*testSetup, error) {
	post := func(url, body string) (*http.Response, error) {
		return http.Post(url, "application/json", strings.NewReader(body))
	}

	regURL := instanceA + "/api/auth/register"
	regBody := fmt.Sprintf(`{"username":"%s","password":"%s"}`, benchUser, benchPass)
	resp, err := post(regURL, regBody)
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()
		resp, err = post(instanceA+"/api/auth/login", regBody)
		if err != nil {
			return nil, fmt.Errorf("login: %w", err)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("auth failed: HTTP %d", resp.StatusCode)
	}
	var r map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&r)
	token, _ := r["token"].(string)
	if token == "" {
		return nil, fmt.Errorf("no token in response")
	}
	fmt.Println("  OK: got JWT token")

	title := fmt.Sprintf("room_%d", time.Now().UnixNano())
	createBody := fmt.Sprintf(`{"title":"%s"}`, title)
	creq, _ := http.NewRequest("POST", instanceA+"/api/rooms", strings.NewReader(createBody))
	creq.Header.Set("Content-Type", "application/json")
	creq.Header.Set("Authorization", "Bearer "+token)
	cresp, err := http.DefaultClient.Do(creq)
	if err != nil {
		return nil, fmt.Errorf("create room: %w", err)
	}
	if cresp.StatusCode != http.StatusCreated {
		cresp.Body.Close()
		return nil, fmt.Errorf("create room: HTTP %d", cresp.StatusCode)
	}
	var cr map[string]interface{}
	json.NewDecoder(cresp.Body).Decode(&cr)
	cresp.Body.Close()
	roomID, _ := cr["id"].(string)
	if roomID == "" {
		return nil, fmt.Errorf("no id in create response")
	}

	sreq, _ := http.NewRequest("POST", instanceA+"/api/rooms/"+roomID+"/start", nil)
	sreq.Header.Set("Authorization", "Bearer "+token)
	sresp, err := http.DefaultClient.Do(sreq)
	if err != nil {
		return nil, fmt.Errorf("start room: %w", err)
	}
	sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("start room: HTTP %d", sresp.StatusCode)
	}
	fmt.Printf("  OK: room %s ready\n", roomID)
	return &testSetup{token: token, roomID: roomID}, nil
}

func dialWS(ts *testSetup, base, roomID string) (*websocket.Conn, error) {
	u := "ws" + strings.TrimPrefix(base, "http") + "/ws?room_id=" + roomID
	if ts != nil && ts.token != "" {
		u += "&token=" + url.QueryEscape(ts.token)
	}
	c, _, e := websocket.DefaultDialer.Dial(u, nil)
	return c, e
}

func authPost(ts *testSetup, urlStr, body string) (*http.Response, error) {
	req, _ := http.NewRequest("POST", urlStr, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ts.token)
	return http.DefaultClient.Do(req)
}

// Health checks
func checkHealth(base, name string) bool {
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		fmt.Printf("  FAIL %s: %v\n", name, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("  FAIL %s: HTTP %d\n", name, resp.StatusCode)
		return false
	}
	fmt.Printf("  OK %s\n", name)
	return true
}

func checkReady(base, name string) bool {
	resp, err := http.Get(base + "/readyz")
	if err != nil {
		fmt.Printf("  FAIL %s: %v\n", name, err)
		return false
	}
	defer resp.Body.Close()
	var r map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&r)
	s, _ := r["status"].(string)
	if s != "ok" && s != "degraded" {
		fmt.Printf("  FAIL %s: status=%s\n", name, s)
		return false
	}
	fmt.Printf("  OK %s (status=%s)\n", name, s)
	return true
}

func checkMetrics(base, name string) bool {
	resp, err := http.Get(base + "/metrics")
	if err != nil {
		fmt.Printf("  FAIL %s: %v\n", name, err)
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "danmakuflow_") {
		fmt.Printf("  FAIL %s: no danmaku_ metrics\n", name)
		return false
	}
	fmt.Printf("  OK %s\n", name)
	return true
}

func checkDifferentInstanceIDs() bool {
	get := func(u string) string {
		resp, err := http.Get(u + "/healthz")
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		var r map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&r)
		id, _ := r["instance_id"].(string)
		return id
	}
	a, b := get(instanceA), get(instanceB)
	if a == "" || b == "" || a == b {
		fmt.Printf("  FAIL: ids a=%q b=%q\n", a, b)
		return false
	}
	fmt.Printf("  OK: A=%s B=%s\n", a, b)
	return true
}

func testCrossInstanceDelivery(ts *testSetup) bool {
	wa, err := dialWS(ts, instanceA, ts.roomID)
	if err != nil {
		fmt.Printf("  FAIL: connect A: %v\n", err)
		return false
	}
	defer wa.Close()
	wb, err := dialWS(ts, instanceB, ts.roomID)
	if err != nil {
		fmt.Printf("  FAIL: connect B: %v\n", err)
		return false
	}
	defer wb.Close()

	time.Sleep(time.Second)
	msg := fmt.Sprintf("cross_%d", time.Now().UnixNano())
	p := fmt.Sprintf(`{"type":"danmaku","payload":{"content":"%s","request_id":"x1"}}`, msg)
	if err := wa.WriteMessage(websocket.TextMessage, []byte(p)); err != nil {
		fmt.Printf("  FAIL: write: %v\n", err)
		return false
	}

	dl := time.Now().Add(timeout)
	for time.Now().Before(dl) {
		wb.SetReadDeadline(dl)
		_, data, err := wb.ReadMessage()
		if err != nil {
			break
		}
		var env map[string]interface{}
		json.Unmarshal(data, &env)
		if t, _ := env["type"].(string); t == "broadcast" {
			if pl, ok := env["payload"].(map[string]interface{}); ok {
				if c, _ := pl["content"].(string); c == msg {
					fmt.Printf("  OK: A->B cross-instance delivery\n")
					return true
				}
			}
		}
	}
	fmt.Printf("  FAIL: B did not receive message\n")
	return false
}

func testRoomIsolation(ts *testSetup) bool {
	// Create second room
	title := ts.roomID + "_iso"
	b := fmt.Sprintf(`{"title":"%s"}`, title)
	cresp, err := authPost(ts, instanceA+"/api/rooms", b)
	if err != nil || cresp.StatusCode != http.StatusCreated {
		if cresp != nil {
			cresp.Body.Close()
		}
		fmt.Printf("  FAIL: create iso room: %v\n", err)
		return false
	}
	var cr map[string]interface{}
	json.NewDecoder(cresp.Body).Decode(&cr)
	cresp.Body.Close()
	isoID, _ := cr["id"].(string)
	if isoID == "" {
		fmt.Println("  FAIL: no id")
		return false
	}

	sresp, err := authPost(ts, instanceA+"/api/rooms/"+isoID+"/start", "")
	if err != nil || sresp.StatusCode != http.StatusOK {
		if sresp != nil {
			sresp.Body.Close()
		}
		fmt.Printf("  FAIL: start iso room: %v\n", err)
		return false
	}
	sresp.Body.Close()

	wa, err := dialWS(ts, instanceA, ts.roomID)
	if err != nil {
		fmt.Printf("  FAIL: connect main: %v\n", err)
		return false
	}
	defer wa.Close()
	wb, err := dialWS(ts, instanceB, isoID)
	if err != nil {
		fmt.Printf("  FAIL: connect iso: %v\n", err)
		return false
	}
	defer wb.Close()

	time.Sleep(500 * time.Millisecond)
	msg := fmt.Sprintf("iso_%d", time.Now().UnixNano())
	p := fmt.Sprintf(`{"type":"danmaku","payload":{"content":"%s","request_id":"iso1"}}`, msg)
	wa.WriteMessage(websocket.TextMessage, []byte(p))

	dl := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(dl) {
		wb.SetReadDeadline(dl)
		_, data, err := wb.ReadMessage()
		if err != nil {
			break
		}
		var env map[string]interface{}
		json.Unmarshal(data, &env)
		if t, _ := env["type"].(string); t == "broadcast" {
			if pl, ok := env["payload"].(map[string]interface{}); ok {
				if c, _ := pl["content"].(string); c == msg {
					fmt.Println("  FAIL: iso room received main room message")
					return false
				}
			}
		}
	}
	fmt.Println("  OK: room isolation works")
	return true
}

func testNginxWebSocket(ts *testSetup) bool {
	w, err := dialWS(ts, nginxEntry, ts.roomID)
	if err != nil {
		fmt.Printf("  FAIL: nginx ws: %v\n", err)
		return false
	}
	defer w.Close()

	p := `{"type":"danmaku","payload":{"content":"nginx_test","request_id":"n1"}}`
	if err := w.WriteMessage(websocket.TextMessage, []byte(p)); err != nil {
		fmt.Printf("  FAIL: write: %v\n", err)
		return false
	}

	dl := time.Now().Add(timeout)
	for time.Now().Before(dl) {
		w.SetReadDeadline(dl)
		_, data, err := w.ReadMessage()
		if err != nil {
			break
		}
		var env map[string]interface{}
		json.Unmarshal(data, &env)
		if t, _ := env["type"].(string); t == "broadcast" {
			if pl, ok := env["payload"].(map[string]interface{}); ok {
				if c, _ := pl["content"].(string); c == "nginx_test" {
					fmt.Println("  OK: nginx websocket proxy works")
					return true
				}
			}
		}
	}
	fmt.Println("  FAIL: no broadcast via nginx")
	return false
}

func testReconnectHistory(ts *testSetup) bool {
	w, err := dialWS(ts, instanceA, ts.roomID)
	if err != nil {
		fmt.Printf("  FAIL: connect: %v\n", err)
		return false
	}

	first := fmt.Sprintf("cursor_%d", time.Now().UnixNano())
	p := fmt.Sprintf(`{"type":"danmaku","payload":{"content":"%s","request_id":"rh1"}}`, first)
	if err := w.WriteMessage(websocket.TextMessage, []byte(p)); err != nil {
		w.Close()
		return false
	}

	var lastID, lastTime string
	dl := time.Now().Add(timeout)
	for time.Now().Before(dl) {
		w.SetReadDeadline(dl)
		_, data, err := w.ReadMessage()
		if err != nil {
			break
		}
		type payload struct {
			ID        string `json:"id"`
			Content   string `json:"content"`
			Timestamp string `json:"timestamp"`
		}
		type msg struct {
			Type    string  `json:"type"`
			Payload payload `json:"payload"`
		}
		var m msg
		if json.Unmarshal(data, &m) == nil && m.Type == "broadcast" && m.Payload.Content == first {
			lastID, lastTime = m.Payload.ID, m.Payload.Timestamp
			break
		}
	}
	w.Close()
	if lastID == "" || lastTime == "" {
		fmt.Println("  FAIL: no cursor")
		return false
	}

	missing := fmt.Sprintf("missing_%d", time.Now().UnixNano())
	postBody := fmt.Sprintf(`{"content":"%s"}`, missing)
	resp, err := authPost(ts, instanceB+"/api/room/"+ts.roomID+"/danmaku", postBody)
	if err != nil || resp.StatusCode != http.StatusCreated {
		if resp != nil {
			resp.Body.Close()
		}
		fmt.Printf("  FAIL: write during disconnect: %v\n", err)
		return false
	}
	resp.Body.Close()
	time.Sleep(time.Second)

	reURL := "ws" + strings.TrimPrefix(instanceA, "http") + "/ws?room_id=" + url.QueryEscape(ts.roomID) +
		"&token=" + url.QueryEscape(ts.token) +
		"&since_time=" + url.QueryEscape(lastTime) + "&last_message_id=" + url.QueryEscape(lastID)
	rw, _, err := websocket.DefaultDialer.Dial(reURL, nil)
	if err != nil {
		fmt.Printf("  FAIL: reconnect: %v\n", err)
		return false
	}
	defer rw.Close()

	rw.SetReadDeadline(time.Now().Add(timeout))
	for {
		_, data, err := rw.ReadMessage()
		if err != nil {
			break
		}
		type dm struct {
			Content string `json:"content"`
		}
		type histPayload struct {
			Danmaku []dm `json:"danmaku"`
		}
		type histMsg struct {
			Type    string      `json:"type"`
			Payload histPayload `json:"payload"`
		}
		var m histMsg
		if json.Unmarshal(data, &m) != nil || m.Type != "history" {
			continue
		}
		for _, d := range m.Payload.Danmaku {
			if d.Content == missing {
				fmt.Println("  OK: history compensation works")
				return true
			}
		}
	}
	fmt.Println("  FAIL: no history compensation")
	return false
}
