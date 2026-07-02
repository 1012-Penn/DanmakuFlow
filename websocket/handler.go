package websocket

import (
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// upgrader 是全局的 HTTP→WebSocket 升级器。
// 作为配置对象，整个程序生命周期只需一个实例。
var upgrader = websocket.Upgrader{}

// ServeWs 处理 WebSocket 握手请求。
// 流程：升级 HTTP → 创建 Client → 注册到 Hub → 启动读写 goroutine。
func ServeWs(hub *Hub, c *gin.Context) {
	// Upgrade 内部验证 Upgrade 头，返回 101 Switching Protocols
	// 第三个参数传 nil，不需要额外响应头
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	client := NewClient(hub, conn)
	client.hub.register <- client

	// 每个客户端独立 goroutine，互不阻塞
	go client.writePump()
	go client.readPump()
}
