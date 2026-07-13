package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/1012-Penn/DanmakuFlow/config"
	"github.com/1012-Penn/DanmakuFlow/handler"
	"github.com/1012-Penn/DanmakuFlow/metrics"
	"github.com/1012-Penn/DanmakuFlow/redisclient"
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

	// 生成实例 ID（用于健康检查和指标）
	instanceID := redisclient.GenerateInstanceID(cfg.Redis.InstanceID)
	slog.Info("实例 ID", "instance_id", instanceID)

	// 注册 Prometheus 指标（使用默认 Registry）
	metrics.Register(prometheus.DefaultRegisterer)

	// 创建 Redis 客户端（如果有配置）
	redisConfigured := cfg.Redis.Addr != ""
	var redisClient *redisclient.Client
	if cfg.Redis.Addr != "" {
		redisClient = redisclient.New(cfg.Redis.Addr, instanceID)
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := redisClient.Ping(pingCtx)
		pingCancel()
		if err != nil {
			slog.Warn("Redis 连接失败，降级为纯本地广播",
				"addr", cfg.Redis.Addr,
				"error", err,
			)
			redisClient.Close() // 关闭连接，避免资源泄漏
			redisClient = nil   // 降级：跳过 Redis，纯本地广播
		} else {
			slog.Info("已连接 Redis",
				"addr", cfg.Redis.Addr,
				"instance_id", instanceID,
			)
		}
	}

	// 创建 Hub（房间管理器），传入 WebSocket 配置和可选的 Redis 客户端
	hub := websocket.NewHubWithConfig(websocket.Config{
		WriteWaitSeconds:    cfg.WebSocket.WriteWaitSeconds,
		PongWaitSeconds:     cfg.WebSocket.PongWaitSeconds,
		MaxMessageSize:      cfg.WebSocket.MaxMessageSize,
		BroadcastBufferSize: cfg.WebSocket.BroadcastBufferSize,
		SendBufferSize:      cfg.WebSocket.SendBufferSize,
		MaxConnPerRoom:      cfg.WebSocket.MaxConnPerRoom,
		MaxConnPerIP:        cfg.WebSocket.MaxConnPerIP,
		AllowedOrigins:      cfg.WebSocket.AllowedOrigins,
	}, redisClient)

	// 即使 Redis 连接失败，只要配置了地址就标记为已配置，
	// 让 Readyz 能正确报告 degraded 而非 disabled。
	if redisConfigured {
		hub.MarkRedisConfigured()
	}

	// 关闭 Gin 的调试日志（我们用自己的日志代替）
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(handler.MetricsMiddleware())

	// 选择存储后端：有 DSN 用 MySQL，否则用 MemoryStore
	var s store.Store
	var userStore store.UserStore
	hasDSN := cfg.Store.DSN != ""
	if hasDSN {
		mysqlStore, err := store.NewMySQLStore(cfg.Store.DSN)
		if err != nil {
			slog.Error("数据库连接失败", "error", err)
			os.Exit(1)
		}
		s = mysqlStore
		slog.Info("已连接 MySQL", "dsn", cfg.Store.DSN)

		// 用户存储复用 MySQL 连接池
		userStore, err = store.NewMySQLUserStore(mysqlStore.DB())
		if err != nil {
			slog.Error("用户表初始化失败", "error", err)
			os.Exit(1)
		}
		slog.Info("用户存储（MySQL）已就绪")
	} else {
		s = store.New()
		userStore = store.NewMemoryUserStore()
		slog.Info("使用内存存储（MemoryStore）")
		slog.Info("用户存储（Memory）已就绪")
	}

	// 创建 RoomStore 和 RoomService
	var roomStore store.RoomStore
	if hasDSN {
		mysqlStore2, ok := s.(*store.MySQLStore)
		if ok {
			roomStore, err = store.NewMySQLRoomStore(mysqlStore2.DB())
			if err != nil {
				slog.Error("房间表初始化失败", "error", err)
				os.Exit(1)
			}
			slog.Info("房间存储（MySQL）已就绪")
		}
	}
	if roomStore == nil {
		roomStore = store.NewMemoryRoomStore()
		slog.Info("房间存储（Memory）已就绪")
	}
	roomSvc := service.NewRoomService(roomStore)

	// 创建认证服务
	authSvc := service.NewAuthService(
		userStore,
		cfg.Auth.JWTSecret,
		cfg.Auth.TokenExpiryHours,
	)
	authHandler := handler.NewAuthHandler(authSvc)

	// 创建 Kafka producer / consumer（如果有配置）
	hasKafka := len(cfg.Kafka.Brokers) > 0
	var kafkaProducer service.KafkaProducerInterface
	var kafkaConsumer *service.KafkaConsumer
	if hasKafka {
		kafkaClientID := cfg.Kafka.ClientID
		if kafkaClientID == "" {
			kafkaClientID = instanceID
		}
		kp, err := service.NewKafkaProducer(cfg.Kafka.Brokers, cfg.Kafka.Topic, kafkaClientID)
		if err != nil {
			slog.Warn("Kafka 连接失败，降级为 danmakuChan + 直写 MySQL",
				"brokers", cfg.Kafka.Brokers,
				"error", err,
			)
		} else {
			kafkaProducer = kp
			slog.Info("已连接 Kafka producer", "topic", cfg.Kafka.Topic)
			if hasDSN {
				kc, err := service.NewKafkaConsumer(cfg.Kafka.Brokers, cfg.Kafka.Topic, cfg.Kafka.ConsumerGroup, kafkaClientID, s)
				if err != nil {
					slog.Warn("Kafka consumer 创建失败，将使用传统直写 MySQL",
						"error", err,
					)
				} else {
					kafkaConsumer = kc
					go kafkaConsumer.Start(context.Background())
					slog.Info("Kafka consumer 已启动", "group", cfg.Kafka.ConsumerGroup)
				}
			}
		}
	}

	// 组装弹幕服务（注入 RoomStatusGetter 用于房间状态检查）
	svc := service.NewDanmakuService(s, hub, cfg.Store.AsyncBufferSize, cfg.RateLimit.MessagesPerSec, hasDSN, roomSvc, kafkaProducer, cfg.Kafka.Brokers, instanceID)
	h := handler.New(svc, hub, cfg.Store.DefaultListLimit, instanceID)

	// 将认证服务和房间状态查询器注入 Hub
	hub.SetAuthValidator(authSvc)
	hub.SetRoomStatusGetter(roomSvc)

	// 注册所有路由
	h.RegisterRoutes(r)

	// 注册认证路由（公开：register/login，受 auth 中间件保护：me）
	authHandler.RegisterAuthRoutes(r, authSvc)

	// 注册房间路由（公开查询，认证写操作）
	roomHandler := handler.NewRoomHandler(roomSvc)
	roomHandler.RegisterRoomRoutes(r, handler.AuthMiddleware(authSvc))

	// 弹幕创建接口使用强认证（需要 JWT，不再可选）
	dmGroup := r.Group("/api/room/:room_id")
	dmGroup.Use(handler.AuthMiddleware(authSvc))
	{
		dmGroup.POST("/danmaku", h.Create)
	}
	// 条件注册 pprof 路由（默认关闭）
	if cfg.Pprof.Enabled {
		pprofGroup := r.Group("/debug/pprof")
		pprofGroup.GET("/", gin.WrapF(pprof.Index))
		pprofGroup.GET("/cmdline", gin.WrapF(pprof.Cmdline))
		pprofGroup.GET("/profile", gin.WrapF(pprof.Profile))
		pprofGroup.GET("/symbol", gin.WrapF(pprof.Symbol))
		pprofGroup.GET("/trace", gin.WrapF(pprof.Trace))
		pprofGroup.GET("/allocs", gin.WrapH(pprof.Handler("allocs")))
		pprofGroup.GET("/block", gin.WrapH(pprof.Handler("block")))
		pprofGroup.GET("/goroutine", gin.WrapH(pprof.Handler("goroutine")))
		pprofGroup.GET("/heap", gin.WrapH(pprof.Handler("heap")))
		pprofGroup.GET("/mutex", gin.WrapH(pprof.Handler("mutex")))
		pprofGroup.GET("/threadcreate", gin.WrapH(pprof.Handler("threadcreate")))
		slog.Info("pprof 已启用，路径 /debug/pprof/")
	}

	// Prometheus 指标端点
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// 前端页面
	r.StaticFile("/", "./templates/index.html")
	r.StaticFile("/room", "./templates/room.html")
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

	// 1. 关闭 HTTP 服务器：停止监听，不再接受新请求
	//    必须先做，否则关闭 WS 时可能还有新 WS 握手进来
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP 服务器关闭超时", "error", err)
	}

	// 2. 关闭 Service：禁止新弹幕请求，等待在途请求完成，排空异步写入通道
	//    必须在 Hub 关闭前做，否则 in-flight 的 createAndBroadcast
	//    可能在 Hub 关闭后继续调 BroadcastToRoom
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := svc.Shutdown(drainCtx); err != nil {
		slog.Warn("Service 关闭超时，部分弹幕可能未入库", "error", err)
	}
	drainCancel()

	// 3. 关闭 Kafka consumer（提交最终 offset，防止 MySQL 关闭后仍在消费）
	if kafkaConsumer != nil {
		if err := kafkaConsumer.Close(); err != nil {
			slog.Warn("Kafka consumer 关闭失败", "error", err)
		}
		slog.Info("Kafka consumer 已关闭")
	}

	// 5. 关闭 WebSocket Hub：停止 Redis 后台，向所有房间发停止信号，等待房间退出
	hub.Shutdown()

	// 4. 关闭 Redis 连接（此时已停止订阅，不会再有跨实例广播）
	if redisClient != nil {
		redisClient.Close()
		slog.Info("Redis 连接已关闭")
	}

	// 7. 关闭 MySQL 数据库连接（此时确保无更多写入）
	if ms, ok := s.(*store.MySQLStore); ok {
		ms.Close()
		slog.Info("MySQL 连接已关闭")
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
