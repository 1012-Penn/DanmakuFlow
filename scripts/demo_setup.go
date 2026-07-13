// demo_setup prepares a repeatable local Docker demo without exposing setup
// steps in the browser recording. Run it with: go run scripts/demo_setup.go.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type authResponse struct {
	Token string `json:"token"`
}

type roomResponse struct {
	ID string `json:"id"`
}

func main() {
	addr := flag.String("addr", "http://localhost:8080", "DanmakuFlow HTTP address")
	hostUser := flag.String("host-user", "demohost", "host username")
	hostPass := flag.String("host-pass", "DemoPass123", "host password")
	viewerUser := flag.String("viewer-user", "demoviewer", "viewer username")
	viewerPass := flag.String("viewer-pass", "DemoPass123", "viewer password")
	title := flag.String("title", "DanmakuFlow 面试演示直播间", "room title")
	flag.Parse()

	baseURL := strings.TrimRight(*addr, "/")
	client := &http.Client{Timeout: 8 * time.Second}
	hostToken, err := registerOrLogin(client, baseURL, *hostUser, *hostPass)
	must(err)
	_, err = registerOrLogin(client, baseURL, *viewerUser, *viewerPass)
	must(err)

	roomID, err := createRoom(client, baseURL, hostToken, *title)
	must(err)
	must(postJSON(client, baseURL+"/api/rooms/"+roomID+"/start", hostToken, nil, nil))

	fmt.Println("演示环境已准备完成")
	fmt.Printf("主播账号: %s / %s\n", *hostUser, *hostPass)
	fmt.Printf("观众账号: %s / %s\n", *viewerUser, *viewerPass)
	fmt.Printf("直播间: %s/room?room_id=%s\n", baseURL, roomID)
	fmt.Printf("Grafana: %s\n", strings.Replace(baseURL, ":8080", ":3000", 1))
}

func registerOrLogin(client *http.Client, baseURL, username, password string) (string, error) {
	body := map[string]string{"username": username, "password": password, "nickname": username}
	var result authResponse
	err := postJSON(client, baseURL+"/api/auth/register", "", body, &result)
	if err == nil {
		return result.Token, nil
	}
	if !strings.Contains(err.Error(), "409") {
		return "", err
	}
	result = authResponse{}
	if err := postJSON(client, baseURL+"/api/auth/login", "", body, &result); err != nil {
		return "", err
	}
	return result.Token, nil
}

func createRoom(client *http.Client, baseURL, token, title string) (string, error) {
	var room roomResponse
	if err := postJSON(client, baseURL+"/api/rooms", token, map[string]string{"title": title}, &room); err != nil {
		return "", err
	}
	if room.ID == "" {
		return "", fmt.Errorf("create room: empty room ID")
	}
	return room.ID, nil
}

func postJSON(client *http.Client, url, token string, payload any, target any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("POST %s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	if target != nil && len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, target); err != nil {
			return err
		}
	}
	return nil
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
