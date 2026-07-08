package main

import (
	"github.com/gin-gonic/gin"

	"github.com/1012-Penn/DanmakuFlow/handler"
	"github.com/1012-Penn/DanmakuFlow/store"
	"github.com/1012-Penn/DanmakuFlow/websocket"
)

func main() {
	r := gin.Default()

	// 创建 Hub（房间管理器）
	hub := websocket.NewHub()

	// 组装依赖：Store → Handler
	// Store 负责数据存取，Handler 依赖 Store 和 Hub
	s := store.New()
	h := handler.New(s, hub)

	// 注册所有路由（HTTP API + WebSocket + 前端页面）
	h.RegisterRoutes(r)

	// 访问 http://localhost:8080/ 时返回前端页面
	r.StaticFile("/", "./templates/index.html")

	r.Run(":8080")
}
