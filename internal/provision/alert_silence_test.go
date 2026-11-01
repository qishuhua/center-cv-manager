package provision

import (
	"strings"
	"testing"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/templates"
)

func TestAlertWebhookValue(t *testing.T) {
	cases := []struct {
		name      string
		testMode  bool
		configured string
		want      string
	}{
		{"静默中心一律占位地址", true, "https://open.feishu.cn/hook/real", templates.SilencedFeishuWebhook},
		{"静默且未配置也是占位地址", true, "", templates.SilencedFeishuWebhook},
		{"启用取全局 Webhook", false, "https://open.feishu.cn/hook/real", "https://open.feishu.cn/hook/real"},
		{"启用但未配置为空（渲染同口径）", false, "", ""},
	}
	for _, tc := range cases {
		if got := alertWebhookValue(tc.testMode, tc.configured); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
	// 占位地址必须是合法 URL 且指向本机不可达端口：alertmanager 可加载、发送必败。
	if !strings.HasPrefix(templates.SilencedFeishuWebhook, "http://127.0.0.1:") {
		t.Fatalf("占位地址应指向本机回环: %q", templates.SilencedFeishuWebhook)
	}
}

func TestAlertWebhookFileUnderMonitoringModule(t *testing.T) {
	m := domain.ComposeModule{ID: "monitoring", ComposeFile: "/home/user/.local/share/center-manager/deploy/monitoring/compose.yml"}
	got := alertWebhookFile(m)
	want := "/home/user/.local/share/center-manager/deploy/monitoring/alerting/feishu-webhook-url"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
