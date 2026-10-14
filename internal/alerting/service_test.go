package alerting

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestSendTextPostsToWebhookAndSkipsUnconfigured(t *testing.T) {
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = body
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	defer server.Close()

	cipher, err := secret.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	service := New(&memoryRepository{credential: domain.AlertCredential{FeishuWebhook: encrypted}}, cipher)
	if err = service.SendText(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		MsgType string `json:"msg_type"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err = json.Unmarshal(received, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MsgType != "text" || payload.Content.Text != "hello" {
		t.Fatalf("unexpected payload: %s", received)
	}

	// 未配置时静默跳过，不发起请求。
	empty := New(&memoryRepository{}, cipher)
	if err = empty.SendText(context.Background(), "hello"); err != nil {
		t.Fatalf("unconfigured send must be a no-op, got %v", err)
	}
}
