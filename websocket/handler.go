package websocket

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// upgrader 负责把 HTTP 连接升级到 WebSocket。
// 留空 CheckOrigin 表示允许所有来源（开发阶段方便测试）。
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // 开发阶段允许所有来源
	},
}

// ServeWs 处理 WebSocket 握手请求。
// 流程：解析房间号 → Upgrade HTTP → 创建 Client → 注册到 Room → 启动读写 goroutine。
// handler 参数是 MessageHandler，由 service 层实现，用于统一处理收到的消息。
func ServeWs(hub *Hub, handler MessageHandler, c *gin.Context) {
	// 从 URL 查询参数取房间号，例如 ws://localhost:8080/ws?room=abc
	roomID := c.DefaultQuery("room", "")
	if roomID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "room parameter is required"})
		return
	}

	// 升级 HTTP → WebSocket
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	// 找到或创建房间，然后注册客户端
	room := hub.GetOrCreateRoom(roomID)
	client := NewClient(hub, room, conn, handler)

	// 注册到房间（通过 channel 发送，不直接操作 map）
	room.register <- client

	// 启动读写 goroutine
	go client.writePump()
	go client.readPump()
}
