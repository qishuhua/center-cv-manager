package monitor

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/secret"
	"center-cv-manager/internal/store"
)

func newTestService(t *testing.T, centers []domain.Center, webhook string) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cipher, err := secret.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range centers {
		if err = st.SaveCenter(centers[i]); err != nil {
			t.Fatal(err)
		}
	}
	if webhook != "" {
		encrypted, encryptErr := cipher.Encrypt(webhook)
		if encryptErr != nil {
			t.Fatal(encryptErr)
		}
		if err = st.SaveAlertCredential(domain.AlertCredential{FeishuWebhook: encrypted}); err != nil {
			t.Fatal(err)
		}
	}
	renderRoot := filepath.Join(dir, "render")
	return New(st, cipher, renderRoot, "http://127.0.0.1:19090"), renderRoot
}

func TestSyncRendersTargets(t *testing.T) {
	centers := []domain.Center{
		{ID: "11111111-2222-3333-4444-555555555555", Name: "郑州中心", Host: "zhengzhou.example", ConsolePort: 8080, Accelerator: "nvidia"},
		{ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "测试中心", Host: "test.example", ConsolePort: 9443, Accelerator: "denglin", TestMode: true},
		{ID: "bbbbbbbb-cccc-dddd-eeee-ffffffffffff", Name: "重庆中心", Host: "chongqing.example", ConsolePort: 9443, Accelerator: "auto", DetectedAccelerator: "denglin"},
		{ID: "77777777-8888-9999-0000-111111111111", Name: "待检测", Host: "pending.example", ConsolePort: 8080, Accelerator: "auto"},
		{ID: "不合法ID", Name: "坏标识", Host: "bad.example", ConsolePort: 8080, Accelerator: "nvidia"},
	}
	service, root := newTestService(t, centers, "https://open.feishu.cn/hook/test")
	if err := service.Sync(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "targets", "centers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var groups []targetGroup
	if err = json.Unmarshal(raw, &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("期望渲染 2 个中心（告警控制与 auto 未检测都不纳入），实际 %d：%s", len(groups), raw)
	}
	first := groups[0]
	if len(first.Targets) != 1 || first.Targets[0] != "zhengzhou.example:8080" {
		t.Errorf("第一个中心目标应为 SSH 主机+访问端口: %v", first.Targets)
	}
	if first.Labels["site"] != centers[0].ID[:8] || first.Labels["site_name"] != "郑州中心" || first.Labels["gpu_vendor"] != "nvidia" {
		t.Errorf("第一个中心标签不符（site 应为 ID 前 8 位短标识）: %v", first.Labels)
	}
	if _, has := first.Labels["test"]; has {
		t.Errorf("清单不应再包含 test 标签: %v", first.Labels)
	}
	second := groups[1]
	if second.Labels["site"] != centers[2].ID[:8] || second.Labels["gpu_vendor"] != "denglin" {
		t.Errorf("第二个中心应为重庆（gpu_vendor 取检测平台 denglin）: %v", second.Labels)
	}
	if second.Targets[0] != "chongqing.example:9443" {
		t.Errorf("第二个中心目标不符: %v", second.Targets)
	}
	for _, group := range groups {
		if group.Labels["site_name"] == "测试中心" {
			t.Error("告警控制中心不应写入监控清单")
		}
	}

	webhook, err := os.ReadFile(filepath.Join(root, "secrets", "feishu-webhook-url"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(webhook)) != "https://open.feishu.cn/hook/test" {
		t.Errorf("webhook 渲染不符: %q", webhook)
	}
	password, err := os.ReadFile(filepath.Join(root, "secrets", "center-logs-password"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(password)) != logsPassword {
		t.Errorf("日志密码渲染不符: %q", password)
	}
}

func TestSyncWithoutWebhookUsesPlaceholder(t *testing.T) {
	service, root := newTestService(t, []domain.Center{{ID: "11111111-2222-3333-4444-555555555555", Name: "郑州", Host: "a.example", ConsolePort: 8080, Accelerator: "nvidia"}}, "")
	if err := service.Sync(); err != nil {
		t.Fatal(err)
	}
	webhook, err := os.ReadFile(filepath.Join(root, "secrets", "feishu-webhook-url"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(webhook)) != unconfiguredWebhook {
		t.Errorf("未配置 Webhook 应写占位地址: %q", webhook)
	}
}

// TestLogsPasswordMatchesTemplate 防止渲染密码与中心网关 htpasswd 漂移：
// htpasswd 的 {SHA} 条目就是 sha1(密码) 的 base64。
func TestLogsPasswordMatchesTemplate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "templates", "secrets", "grafana-logs.htpasswd"))
	if err != nil {
		t.Skipf("读取中心网关模板失败: %v", err)
	}
	line := strings.TrimSpace(string(raw))
	const prefix = "grafana_logs:{SHA}"
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("htpasswd 条目格式不符: %q", line)
	}
	digest, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, prefix))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha1.Sum([]byte(logsPassword))) != fmt.Sprintf("%x", digest) {
		t.Error("logsPassword 与模板 grafana-logs.htpasswd 哈希不一致，Grafana 日志查询会被中心拒绝")
	}
}

func TestStatusMergesUpSignals(t *testing.T) {
	centers := []domain.Center{
		{ID: "11111111-2222-3333-4444-555555555555", Name: "郑州中心", Host: "a.example", ConsolePort: 8080, Accelerator: "nvidia"},
		{ID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Name: "重庆中心", Host: "b.example", ConsolePort: 8080, Accelerator: "denglin"},
		{ID: "cccccccc-dddd-eeee-ffff-000000000000", Name: "测试中心", Host: "c.example", ConsolePort: 8080, Accelerator: "nvidia", TestMode: true},
	}
	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != `up{job="federate-centers"}` {
			t.Errorf("查询表达式不符: %q", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"site":"11111111","site_name":"郑州中心"},"value":[1760000000,"1"]},
			{"metric":{"site":"aaaaaaaa","site_name":"重庆中心"},"value":[1760000000,"0"]}]}}`)
	}))
	defer prometheus.Close()

	service, _ := newTestService(t, centers, "")
	service.prometheusURL = prometheus.URL
	status := service.Status(context.Background())
	if !status.StackOnline || status.Error != "" {
		t.Fatalf("监控栈应在线: %+v", status)
	}
	if len(status.Centers) != 3 {
		t.Fatalf("期望 3 个中心状态，实际 %d", len(status.Centers))
	}
	byName := map[string]CenterStatus{}
	for _, item := range status.Centers {
		byName[item.Name] = item
	}
	first := byName["郑州中心"]
	if !first.Monitored || first.Site != "11111111" || first.Target != "a.example:8080" || first.Up == nil || !*first.Up {
		t.Errorf("郑州中心应为已纳入、带短 site 与 host:port 目标且在线: %+v", first)
	}
	second := byName["重庆中心"]
	if second.Up == nil || *second.Up {
		t.Errorf("重庆中心应离线: %+v", second)
	}
	if third := byName["测试中心"]; third.Monitored || third.Reason == "" {
		t.Errorf("告警控制中心应显示未纳入与原因: %+v", third)
	}
}

func TestStatusWhenStackDown(t *testing.T) {
	service, _ := newTestService(t, []domain.Center{{ID: "11111111-2222-3333-4444-555555555555", Name: "郑州", Host: "a.example", ConsolePort: 8080, Accelerator: "auto"}}, "")
	status := service.Status(context.Background())
	if status.StackOnline || status.Error == "" {
		t.Fatalf("监控栈不可达时应降级为离线: %+v", status)
	}
	// auto 未检测：不纳入监控并提示原因。
	if status.Centers[0].Monitored || status.Centers[0].Reason == "" {
		t.Errorf("未检测平台中心应给出未纳入原因: %+v", status.Centers[0])
	}
}
