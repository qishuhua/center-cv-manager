package center

import (
	"testing"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/secret"
)

func TestCenterTestAlarmWebhookPlain(t *testing.T) {
	cipher, err := secret.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := &resetRepository{}
	s := New(repo, cipher)
	base := domain.Center{Name: "中心", Host: "host", Username: "root", AuthType: "password", Password: "pw", TestAlarmEnabled: true}
	if _, err := s.Save(base); err == nil {
		t.Fatal("开启时缺少 URL 应拒绝")
	}
	base.FeishuWebhookURL = "http://example.com/hook"
	if _, err := s.Save(base); err == nil {
		t.Fatal("非飞书 HTTPS 地址应拒绝")
	}
	base.FeishuWebhookURL = "https://open.feishu.cn/open-apis/bot/v2/hook/test-token"
	saved, err := s.Save(base)
	if err != nil {
		t.Fatal(err)
	}
	if saved.FeishuWebhookURL != base.FeishuWebhookURL || repo.value.FeishuWebhookURL != base.FeishuWebhookURL {
		t.Fatal("接口返回与入库都应是明文机器人地址")
	}
	base.ID, base.Password = saved.ID, Mask
	base.TestAlarmEnabled = false
	if _, err := s.Save(base); err != nil || repo.value.FeishuWebhookURL != base.FeishuWebhookURL {
		t.Fatalf("关闭开关应保留机器人地址: %v", err)
	}
	base.FeishuWebhookURL = ""
	if _, err := s.Save(base); err != nil || repo.value.FeishuWebhookURL != "" {
		t.Fatalf("清空字段应清除机器人地址: %v", err)
	}
	base.TestAlarmEnabled = true
	if _, err := s.Save(base); err == nil {
		t.Fatal("重新开启时缺少 URL 应拒绝")
	}
}

func TestCenterMisSortSwitchRequiresWMSURL(t *testing.T) {
	cipher, err := secret.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(&resetRepository{}, cipher)
	base := domain.Center{Name: "中心", Host: "host", Username: "root", AuthType: "password", Password: "pw", MisSortEnabled: true}
	if _, err := s.Save(base); err == nil {
		t.Fatal("开启错分检测时缺少 WMS 服务地址应拒绝")
	}
	base.MisSortEnabled = false
	if _, err := s.Save(base); err != nil {
		t.Fatalf("关闭开关允许地址留空: %v", err)
	}
}
