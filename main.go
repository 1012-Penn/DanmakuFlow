package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/1012-Penn/DanmakuFlow/config"
	"github.com/1012-Penn/DanmakuFlow/handler"
	"github.com/1012-Penn/DanmakuFlow/service"
	"github.com/1012-Penn/DanmakuFlow/store"
	"github.com/1012-Penn/DanmakuFlow/websocket"
)

func main() {
	// 加载配置
	configPath := "config.yaml"
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 初始化结构化日志（slog）
	initLogger(cfg.Log)

	// 创建 Hub（房间管理器），传入 WebSocket 配置
	hub := websocket.NewHubWithConfig(websocket.Config{
		WriteWaitSeconds:    cfg.WebSocket.WriteWaitSeconds,
		PongWaitSeconds:     cfg.WebSocket.PongWaitSeconds,
		MaxMessageSize:      cfg.WebSocket.MaxMessageSize,
		BroadcastBufferSize: cfg.WebSocket.BroadcastBufferSize,
		SendBufferSize:      cfg.WebSocket.SendBufferSize,
	})

	// 关闭 Gin 的调试日志（我们用自己的日志代替）
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// 选择存储后端：有 DSN 用 MySQL，否则用 MemoryStore
	var s store.Store
	if cfg.Store.DSN != "" {
		mysqlStore, err := store.NewMySQLStore(cfg.Store.DSN)
		if err != nil {
			slog.Error("数据库连接失败", "error", err)
			os.Exit(1)
		}
		s = mysqlStore
		slog.Info("已连接 MySQL", "dsn", cfg.Store.DSN)
	} else {
		s = store.New()
		slog.Info("使用内存存储（MemoryStore）")
	}

	// 组装依赖链
	svc := service.NewDanmakuService(s, hub, cfg.Store.AsyncBufferSize)
	h := handler.New(svc, hub, cfg.Store.DefaultListLimit)

	// 注册所有路由
	h.RegisterRoutes(r)

	// 前端页面
	r.StaticFile("/", "./templates/index.html")
	r.StaticFile("/burst", "./templates/burst.html")

	addr := fmt.Sprintf(":%d", cfg.Server.Port)

	// 创建 http.Server（为了后续能优雅关闭）
	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	// 在独立 goroutine 中启动，不阻塞退出信号监听
	go func() {
		slog.Info("HTTP 服务器启动", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP 服务器异常退出", "error", err)
			os.Exit(1)
		}
	}()

	// 等待退出信号（Ctrl+C 或 kill）
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	slog.Info("收到退出信号，开始优雅关闭...", "signal", sig.String())

	// 1. 关闭 WebSocket 连接（给所有客户端发关闭帧）
	hub.Shutdown()

	// 2. 排空异步写库通道，等待 consumer 将剩余弹幕写入存储
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := svc.Shutdown(drainCtx); err != nil {
		slog.Warn("异步写入排空超时，部分弹幕可能未入库", "error", err)
	}
	drainCancel()

	// 3. 关闭 MySQL 数据库连接（此时确保无更多写入）
	if ms, ok := s.(*store.MySQLStore); ok {
		ms.Close()
		slog.Info("MySQL 连接已关闭")
	}

	// 4. 关闭 HTTP 服务器（最多等待 5 秒完成当前请求）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("HTTP 服务器关闭超时", "error", err)
	}

	slog.Info("服务器已安全关闭")
}

// initLogger 根据配置初始化 slog 全局日志。
func initLogger(lc config.LogConfig) {
	level, err := lc.ResolveLevel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "日志配置错误: %v，使用默认级别 info\n", err)
		level = 0 // slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: slog.Level(level),
	}

	var h slog.Handler
	switch lc.Format {
	case "json":
		h = slog.NewJSONHandler(os.Stdout, opts)
	default:
		h = slog.NewTextHandler(os.Stdout, opts)
	}

	slog.SetDefault(slog.New(h))
}
