package centertask

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"center-cv-manager/internal/domain"
)

// centerFor 返回指向测试服务器（模拟中心网关）的中心记录。
func centerFor(t *testing.T, server *httptest.Server) domain.Center {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return domain.Center{ID: "c1", Name: "测试中心", Host: parsed.Hostname(), ConsolePort: port, Port: 22}
}

func TestListDecodesTasks(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"task_id":"task-1","camera":"北门-01","status":"running","created_time":1762000500000,"start_time":1762000000000,"end_time":1762003600000,"heartbeat_time":1762000530000},
			{"task_id":"task-2","camera":"南门-02","status":"finished","created_time":1761900500000,"start_time":1761900000000,"end_time":1761903600000,"finished_time":1761900900000}
		]`))
	}))
	defer server.Close()
	tasks, err := New().List(context.Background(), centerFor(t, server))
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/tasks" {
		t.Fatalf("应请求网关任务入口，实际 %s", gotPath)
	}
	if len(tasks) != 2 {
		t.Fatalf("应解析 2 条任务，实际 %d", len(tasks))
	}
	// end_time 是流窗口结束（与执行状态无关），运行中任务同样返回。
	if tasks[0].TaskID != "task-1" || tasks[0].Status != "running" || tasks[0].EndTime != 1762003600000 || tasks[0].CreatedTime != 1762000500000 {
		t.Fatalf("运行中任务解析错误: %+v", tasks[0])
	}
	// finished_time 是任务执行终态时间（预留，Worker 终态上报）：已上报的
	// 原样解析，未上报（运行中/上游暂未写入）为零值由前端显示占位。
	if tasks[0].FinishedTime != 0 {
		t.Fatalf("未上报终态时间的任务应为零值: %+v", tasks[0])
	}
	// heartbeat_time 是 Worker 上报心跳：已上报的原样解析，未上报（旧版
	// 中心/终态后 Worker 停写）为零值，失联判定在前端按阈值完成。
	if tasks[0].HeartbeatTime != 1762000530000 {
		t.Fatalf("运行中任务应解析 Worker 心跳: %+v", tasks[0])
	}
	if tasks[1].HeartbeatTime != 0 {
		t.Fatalf("未上报心跳的任务应为零值: %+v", tasks[1])
	}
	if tasks[1].EndTime != 1761903600000 || tasks[1].FinishedTime != 1761900900000 {
		t.Fatalf("已结束任务应带窗口结束与任务终态时间: %+v", tasks[1])
	}
}

func TestListUnsupportedTranslates404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	_, err := New().List(context.Background(), centerFor(t, server))
	if err == nil || !strings.Contains(err.Error(), "不支持任务查询") {
		t.Fatalf("404 应翻译为版本不支持的可操作错误，实际 %v", err)
	}
}

func TestListUnreachableGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	center := centerFor(t, server)
	server.Close() // 立即关闭模拟网关不可达
	_, err := New().List(context.Background(), center)
	if err == nil || !strings.Contains(err.Error(), "中心网关不可达") {
		t.Fatalf("连接失败应提示网关不可达，实际 %v", err)
	}
}

func TestGatewayURLDefaultsConsolePort(t *testing.T) {
	if got := gatewayBase(domain.Center{Host: "center.example"}); got != "http://center.example:8080" {
		t.Fatalf("ConsolePort 未配置应回退 8080，实际 %s", got)
	}
	if got := gatewayBase(domain.Center{Host: " center.example ", ConsolePort: 9443}); got != "http://center.example:9443" {
		t.Fatalf("自定义端口与首尾空白应正确处理，实际 %s", got)
	}
}

func TestListSortsByCreatedTimeDesc(t *testing.T) {
	// 按创建时间降序（最新投递在前）；历史回放任务窗口在过去、创建时间
	// 最新，应排在最前；无创建时间的旧台账条目回退窗口开始时间垫底。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"task_id":"old-entry","status":"created","start_time":1761900000000,"end_time":1761903600000},
			{"task_id":"new-playback","status":"running","created_time":1762000500000,"start_time":1761000000000,"end_time":1761003600000},
			{"task_id":"mid","status":"created","created_time":1761950000000,"start_time":1761950000000,"end_time":1761953600000}
		]`))
	}))
	defer server.Close()
	tasks, err := New().List(context.Background(), centerFor(t, server))
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 || tasks[0].TaskID != "new-playback" || tasks[1].TaskID != "mid" || tasks[2].TaskID != "old-entry" {
		t.Fatalf("应按创建时间降序排列（缺失回退窗口开始），实际 %v", tasks)
	}
}

func TestListAcceptsTasksEnvelope(t *testing.T) {
	// 中心侧若演进为 {"tasks":[...]} 信封，管理端应能取到同一份清单。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tasks":[{"task_id":"task-1","status":"running","start_time":1762000000000}]}`))
	}))
	defer server.Close()
	tasks, err := New().List(context.Background(), centerFor(t, server))
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskID != "task-1" {
		t.Fatalf("信封形态应解析出任务清单，实际 %+v", tasks)
	}
}
