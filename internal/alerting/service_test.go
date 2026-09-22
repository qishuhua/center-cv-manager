package alerting

import (
	"testing"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/secret"
)

type memoryRepository struct {
	credential domain.AlertCredential
}

func (r *memoryRepository) GetAlertCredential() (domain.AlertCredential, error) {
	return r.credential, nil
}

func (r *memoryRepository) SaveAlertCredential(value domain.AlertCredential) error {
	r.credential = value
	return nil
}

func TestCredentialIsGlobalEncryptedAndMasked(t *testing.T) {
	cipher, err := secret.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository := &memoryRepository{}
	service := New(repository, cipher)
	const webhook = "https://open.feishu.cn/open-apis/bot/v2/hook/test-token"
	public, err := service.SaveCredential(domain.AlertCredential{FeishuWebhook: webhook})
	if err != nil {
		t.Fatal(err)
	}
	if !public.Configured || public.FeishuWebhook != "********" {
		t.Fatalf("public credential was not masked: %#v", public)
	}
	if repository.credential.FeishuWebhook == webhook {
		t.Fatal("webhook was stored as plaintext")
	}
	config, err := service.Config()
	if err != nil {
		t.Fatal(err)
	}
	if config.FeishuWebhook != webhook {
		t.Fatalf("decrypted webhook = %q", config.FeishuWebhook)
	}
}
