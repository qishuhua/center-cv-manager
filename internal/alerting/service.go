package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"center-cv-manager/internal/center"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/secret"
)

type Repository interface {
	GetAlertCredential() (domain.AlertCredential, error)
	SaveAlertCredential(domain.AlertCredential) error
}

type Service struct {
	store  Repository
	cipher *secret.Cipher
}

func New(store Repository, cipher *secret.Cipher) *Service {
	return &Service{store: store, cipher: cipher}
}

func (s *Service) Credential() (domain.AlertCredential, error) {
	value, err := s.store.GetAlertCredential()
	if err == nil && value.FeishuWebhook != "" {
		value.FeishuWebhook = center.Mask
		value.Configured = true
	}
	return value, err
}

func (s *Service) SaveCredential(input domain.AlertCredential) (domain.AlertCredential, error) {
	old, err := s.store.GetAlertCredential()
	if err != nil {
		return domain.AlertCredential{}, err
	}
	if input.FeishuWebhook == "" || input.FeishuWebhook == center.Mask {
		input.FeishuWebhook = old.FeishuWebhook
	} else {
		normalized, normalizeErr := normalizeWebhook(input.FeishuWebhook)
		if normalizeErr != nil {
			return domain.AlertCredential{}, normalizeErr
		}
		input.FeishuWebhook, err = s.cipher.Encrypt(normalized)
		if err != nil {
			return domain.AlertCredential{}, err
		}
	}
	if input.FeishuWebhook == "" {
		return domain.AlertCredential{}, fmt.Errorf("飞书机器人 Webhook 不能为空")
	}
	input.Configured = true
	input.UpdatedAt = time.Now()
	if err = s.store.SaveAlertCredential(input); err != nil {
		return domain.AlertCredential{}, err
	}
	input.FeishuWebhook = center.Mask
	return input, nil
}

func (s *Service) Config() (domain.AlertCredential, error) {
	value, err := s.store.GetAlertCredential()
	if err != nil || value.FeishuWebhook == "" {
		return value, err
	}
	value.FeishuWebhook, err = s.cipher.Decrypt(value.FeishuWebhook)
	if err != nil {
		return domain.AlertCredential{}, fmt.Errorf("解密飞书机器人 Webhook 失败: %w", err)
	}
	value.Configured = true
	return value, nil
}

func normalizeWebhook(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("飞书机器人 Webhook 必须是有效的 HTTPS 地址")
	}
	return value, nil
}

// SendText 向全局飞书机器人发送一条文本消息；未配置 Webhook 时静默跳过（返回 nil）。
func (s *Service) SendText(ctx context.Context, text string) error {
	config, err := s.Config()
	if err != nil {
		return err
	}
	if !config.Configured || config.FeishuWebhook == "" {
		return nil
	}
	payload, err := json.Marshal(map[string]any{"msg_type": "text", "content": map[string]string{"text": text}})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.FeishuWebhook, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("发送飞书消息失败: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("飞书返回状态 %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err = json.Unmarshal(body, &result); err == nil && result.Code != 0 {
		return fmt.Errorf("飞书拒绝消息: %s", result.Msg)
	}
	return nil
}
