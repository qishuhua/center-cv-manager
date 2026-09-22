package alerting

import (
	"fmt"
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
