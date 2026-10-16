// Package registry 管理共享镜像仓库（registry:3 + OSS S3 兼容存储后端）。
// 发布模型：管理端本地构建一次并推送仓库，各中心发布时经 SSH 反向隧道按
// digest 拉取镜像，manifest 经隧道回到管理端，镜像层由 registry 307 重定向
// 到 OSS 签名 URL 直下，不占用管理端出口带宽。软件发布硬依赖镜像仓库：版本
// 必须先经「软件仓库」页构建发布。
package registry

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/secret"
)

// DefaultEndpoint 是 registry sidecar 的默认地址（与管理端共享网络命名空间，
// 经 127.0.0.1:5000 访问；127.0.0.0/8 是 dockerd 默认豁免 TLS 的网段）。
const DefaultEndpoint = "http://127.0.0.1:5000"

var ErrNotConfigured = errors.New("尚未配置镜像仓库")

type Store interface {
	GetRegistrySettings() (domain.RegistrySettings, error)
	SaveRegistrySettings(domain.RegistrySettings) error
	GetOSSCredential() (domain.OSSCredential, error)
}

type Service struct {
	store      Store
	cipher     *secret.Cipher
	configPath string

	clientMu     sync.Mutex
	cachedClient *Client
	cachedURL    string
}

func New(store Store, cipher *secret.Cipher, configPath string) *Service {
	return &Service{store: store, cipher: cipher, configPath: configPath}
}

// Client 返回共享的 registry 客户端（按 endpoint 缓存）：Client 内部的
// digest 缓存因此跨请求生效，版本清单不必每次重拉全部 manifest 与 config。
func (s *Service) Client() (*Client, error) {
	settings, err := s.Settings()
	if err != nil {
		return nil, err
	}
	if !settings.Configured {
		return nil, ErrNotConfigured
	}
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if s.cachedClient != nil && s.cachedURL == settings.Endpoint {
		return s.cachedClient, nil
	}
	client, err := NewClient(settings.Endpoint)
	if err != nil {
		return nil, err
	}
	s.cachedClient, s.cachedURL = client, settings.Endpoint
	return client, nil
}

func (s *Service) Settings() (domain.RegistrySettings, error) {
	v, err := s.store.GetRegistrySettings()
	if err == nil {
		v.Configured = strings.TrimSpace(v.Endpoint) != ""
	}
	return v, err
}

func (s *Service) SaveSettings(input domain.RegistrySettings) (domain.RegistrySettings, error) {
	input.Endpoint = strings.TrimSpace(input.Endpoint)
	if input.Endpoint != "" {
		if _, _, err := ParseEndpoint(input.Endpoint); err != nil {
			return domain.RegistrySettings{}, err
		}
	}
	// Configured 完全由 endpoint 推导：清空地址即关闭，避免回显/持久化请求体
	// 携带的旧值。
	input.Configured = input.Endpoint != ""
	input.UpdatedAt = time.Now()
	if err := s.store.SaveRegistrySettings(input); err != nil {
		return domain.RegistrySettings{}, err
	}
	return input, nil
}

// ParseEndpoint 校验镜像仓库地址，返回管理端 HTTP 直连的 base URL 和镜像
// 引用中的 host:port（registry sidecar 形态下两者相同）。
func ParseEndpoint(endpoint string) (baseURL, host string, err error) {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" {
		return "", "", fmt.Errorf("镜像仓库地址不能为空")
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "http://" + trimmed
	}
	parsed, parseErr := url.Parse(trimmed)
	if parseErr != nil || parsed.Host == "" {
		return "", "", fmt.Errorf("镜像仓库地址无效: %s", endpoint)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", fmt.Errorf("镜像仓库地址仅支持 http/https: %s", endpoint)
	}
	if parsed.Port() == "" {
		return "", "", fmt.Errorf("镜像仓库地址需带端口（例如 %s）", DefaultEndpoint)
	}
	return parsed.Scheme + "://" + parsed.Host, parsed.Host, nil
}
