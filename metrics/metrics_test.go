package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestAsyncChanMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(AsyncChanLen, AsyncChanCap)

	// 初始值应为 0
	if val := testutil.ToFloat64(AsyncChanLen); val != 0 {
		t.Errorf("AsyncChanLen 初始值应为 0，实际 %f", val)
	}
	if val := testutil.ToFloat64(AsyncChanCap); val != 0 {
		t.Errorf("AsyncChanCap 初始值应为 0，实际 %f", val)
	}

	// 模拟 Service 初始化
	AsyncChanCap.Set(1024)
	AsyncChanLen.Set(42)

	if val := testutil.ToFloat64(AsyncChanCap); val != 1024 {
		t.Errorf("AsyncChanCap 应等于 1024，实际 %f", val)
	}
	if val := testutil.ToFloat64(AsyncChanLen); val != 42 {
		t.Errorf("AsyncChanLen 应等于 42，实际 %f", val)
	}

	// 模拟 close 排空
	AsyncChanLen.Set(0)
	if val := testutil.ToFloat64(AsyncChanLen); val != 0 {
		t.Errorf("排空后 AsyncChanLen 应为 0，实际 %f", val)
	}
}

func TestWSConnectionMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(WSConnections, WSConnTotal, WSConnRejects)

	// 初始值
	if val := testutil.ToFloat64(WSConnections); val != 0 {
		t.Errorf("WSConnections 初始值应为 0，实际 %f", val)
	}
	if val := testutil.ToFloat64(WSConnTotal); val != 0 {
		t.Errorf("WSConnTotal 初始值应为 0，实际 %f", val)
	}

	// 模拟 3 个连接建立
	WSConnections.Inc()
	WSConnections.Inc()
	WSConnections.Inc()
	WSConnTotal.Inc()
	WSConnTotal.Inc()
	WSConnTotal.Inc()

	if val := testutil.ToFloat64(WSConnections); val != 3 {
		t.Errorf("WSConnections 应等于 3，实际 %f", val)
	}
	if val := testutil.ToFloat64(WSConnTotal); val != 3 {
		t.Errorf("WSConnTotal 应等于 3，实际 %f", val)
	}

	// 模拟 1 个断开
	WSConnections.Dec()

	if val := testutil.ToFloat64(WSConnections); val != 2 {
		t.Errorf("断开后 WSConnections 应等于 2，实际 %f", val)
	}
	if val := testutil.ToFloat64(WSConnTotal); val != 3 {
		t.Errorf("断开不影响 WSConnTotal，应仍为 3，实际 %f", val)
	}
}

func TestWSRejectMetrics(t *testing.T) {
	WSConnRejects.WithLabelValues("per_ip").Inc()
	WSConnRejects.WithLabelValues("per_room").Inc()
	WSConnRejects.WithLabelValues("per_room").Inc()
}

func TestRedisPubQueueMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(RedisPubQueueLen, RedisPubQueueCap)

	RedisPubQueueCap.Set(256)
	RedisPubQueueLen.Set(0)

	if val := testutil.ToFloat64(RedisPubQueueCap); val != 256 {
		t.Errorf("RedisPubQueueCap 应等于 256，实际 %f", val)
	}
	if val := testutil.ToFloat64(RedisPubQueueLen); val != 0 {
		t.Errorf("RedisPubQueueLen 初始应为 0，实际 %f", val)
	}

	// 模拟入队
	RedisPubQueueLen.Set(float64(5))
	if val := testutil.ToFloat64(RedisPubQueueLen); val != 5 {
		t.Errorf("入队后 RedisPubQueueLen 应等于 5，实际 %f", val)
	}

	// 模拟排空
	RedisPubQueueLen.Set(0)
	if val := testutil.ToFloat64(RedisPubQueueLen); val != 0 {
		t.Errorf("排空后 RedisPubQueueLen 应等于 0，实际 %f", val)
	}
}

func TestHTTPReqTotalNumericStatus(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(HTTPReqTotal)

	// 使用数字状态码标签（非 "OK" "Bad Request" 等文本）
	HTTPReqTotal.With(prometheus.Labels{
		"method": "GET",
		"route":  "/healthz",
		"status": "200",
	}).Inc()
	HTTPReqTotal.With(prometheus.Labels{
		"method": "POST",
		"route":  "/api/room/:room_id/danmaku",
		"status": "400",
	}).Inc()
	HTTPReqTotal.With(prometheus.Labels{
		"method": "POST",
		"route":  "/api/room/:room_id/danmaku",
		"status": "503",
	}).Inc()

	// 验证所有标签值都存在
	count, err := testutil.GatherAndCount(reg, "danmakuflow_http_requests_total")
	if err != nil {
		t.Fatalf("GatherAndCount 失败: %v", err)
	}
	if count < 3 {
		t.Errorf("预期至少 3 个样本，实际 %d", count)
	}
}

func TestActiveRoomsMetric(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(WSActiveRooms)

	if val := testutil.ToFloat64(WSActiveRooms); val != 0 {
		t.Errorf("WSActiveRooms 初始应为 0，实际 %f", val)
	}

	WSActiveRooms.Set(5)
	if val := testutil.ToFloat64(WSActiveRooms); val != 5 {
		t.Errorf("WSActiveRooms 应等于 5，实际 %f", val)
	}

	WSActiveRooms.Set(0)
	if val := testutil.ToFloat64(WSActiveRooms); val != 0 {
		t.Errorf("清空后 WSActiveRooms 应等于 0，实际 %f", val)
	}
}
