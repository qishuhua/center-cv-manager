package center

import (
	"testing"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/secret"
)

func TestCenterTestAlarmWebhookEncryptedAndMasked(t *testing.T) {
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
	if saved.FeishuWebhookURL != Mask || repo.value.FeishuWebhookURL == base.FeishuWebhookURL {
		t.Fatal("接口应脱敏，入库应加密")
	}
	decrypted, err := s.TestAlarmWebhook(repo.value)
	if err != nil || decrypted != base.FeishuWebhookURL {
		t.Fatalf("解密机器人地址失败: %v", err)
	}
	base.ID, base.Password, base.FeishuWebhookURL = saved.ID, Mask, Mask
	base.TestAlarmEnabled = false
	if _, err := s.Save(base); err != nil || repo.value.FeishuWebhookURL == "" {
		t.Fatalf("关闭开关应保留机器人地址: %v", err)
	}
	base.TestAlarmEnabled = true
	if _, err := s.Save(base); err != nil {
		t.Fatalf("重新开启应沿用机器人地址: %v", err)
	}
}
