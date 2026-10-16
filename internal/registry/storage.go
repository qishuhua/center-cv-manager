package registry

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"center-cv-manager/internal/domain"
	"gopkg.in/yaml.v3"
)

// storageDocument 是渲染给 registry:3 容器的 config.yml 结构。关键字段来自
// 实测验证（registry-test）：必须 registry:3（2.8.3 硬编码 path-style 与 OSS
// 不兼容）、forcepathstyle: false、共享 endpoint；配置路径为容器内
// /etc/distribution/config.yml（本部署经 command 参数指向数据卷）。
type storageDocument struct {
	Version string `yaml:"version"`
	Log     struct {
		Level string `yaml:"level"`
	} `yaml:"log"`
	Storage struct {
		S3 struct {
			AccessKey      string `yaml:"accesskey"`
			SecretKey      string `yaml:"secretkey"`
			Region         string `yaml:"region"`
			RegionEndpoint string `yaml:"regionendpoint"`
			Bucket         string `yaml:"bucket"`
			RootDirectory  string `yaml:"rootdirectory"`
			Secure         bool   `yaml:"secure"`
			ForcePathStyle bool   `yaml:"forcepathstyle"`
		} `yaml:"s3"`
		Delete struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"delete"`
	} `yaml:"storage"`
	HTTP struct {
		Addr string `yaml:"addr"`
	} `yaml:"http"`
}

// StorageConfig 读取已渲染的 registry config.yml，只返回非敏感字段（存储前缀）。
func (s *Service) StorageConfig() (domain.RegistryStorage, error) {
	out := domain.RegistryStorage{}
	raw, err := os.ReadFile(s.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	var document storageDocument
	if err = yaml.Unmarshal(raw, &document); err != nil {
		return out, fmt.Errorf("解析 registry 配置失败: %w", err)
	}
	out.RootDirectory = strings.Trim(document.Storage.S3.RootDirectory, "/")
	out.Exists = true
	return out, nil
}

// SaveStorageConfig 渲染并写入 registry config.yml。OSS 凭据、Bucket 与
// Endpoint 全部取自模型仓库的全局 OSS 配置（oss_credentials）——镜像仓库不
// 单独持有凭据，只在共享 Bucket 内使用独立前缀（与 center-images/、
// center-models/ 并列）。registry 容器只在启动时读取，保存后需重启该容器生效。
func (s *Service) SaveStorageConfig(input domain.RegistryStorage) (domain.RegistryStorage, error) {
	input.RootDirectory = strings.Trim(strings.TrimSpace(input.RootDirectory), "/")
	if input.RootDirectory == "" {
		input.RootDirectory = "registry"
	}
	if strings.Contains(input.RootDirectory, "..") || strings.ContainsAny(input.RootDirectory, " \t\n") {
		return domain.RegistryStorage{}, fmt.Errorf("根目录前缀不能包含 .. 或空白字符")
	}
	oss, err := s.store.GetOSSCredential()
	if err != nil {
		return domain.RegistryStorage{}, err
	}
	if !oss.Configured {
		return domain.RegistryStorage{}, fmt.Errorf("请先在「模型仓库」配置 OSS 凭据，镜像仓库复用同一套存储")
	}
	secretKey, err := s.cipher.Decrypt(oss.AccessKeySecret)
	if err != nil {
		return domain.RegistryStorage{}, fmt.Errorf("解密全局 OSS 凭据失败: %w", err)
	}
	if oss.AccessKeyID == "" || secretKey == "" || oss.Bucket == "" {
		return domain.RegistryStorage{}, fmt.Errorf("全局 OSS 凭据不完整，请先在「模型仓库」补全")
	}

	document := storageDocument{Version: "0.1"}
	document.Log.Level = "info"
	document.Storage.S3.AccessKey = oss.AccessKeyID
	document.Storage.S3.SecretKey = secretKey
	document.Storage.S3.Region = regionFromEndpoint(oss.Endpoint)
	document.Storage.S3.RegionEndpoint = normalizeOSSEndpoint(oss.Endpoint)
	document.Storage.S3.Bucket = oss.Bucket
	document.Storage.S3.RootDirectory = "/" + input.RootDirectory
	document.Storage.S3.Secure = true
	document.Storage.S3.ForcePathStyle = false
	document.Storage.Delete.Enabled = true
	document.HTTP.Addr = ":5000"
	rendered, err := yaml.Marshal(&document)
	if err != nil {
		return domain.RegistryStorage{}, err
	}
	if err = os.MkdirAll(filepath.Dir(s.configPath), 0700); err != nil {
		return domain.RegistryStorage{}, err
	}
	temporary := s.configPath + ".tmp"
	if err = os.WriteFile(temporary, rendered, 0600); err != nil {
		return domain.RegistryStorage{}, err
	}
	if err = os.Rename(temporary, s.configPath); err != nil {
		return domain.RegistryStorage{}, err
	}
	input.Exists = true
	input.UpdatedAt = time.Now()
	return input, nil
}

// regionFromEndpoint 从 OSS endpoint 提取 region（oss-cn-shanghai.aliyuncs.com
// → oss-cn-shanghai）。registry 的 S3 驱动对 region 取值不敏感，仅为可读保留。
func regionFromEndpoint(endpoint string) string {
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Host != "" {
		endpoint = parsed.Host
	}
	if index := strings.Index(endpoint, "."); index > 0 {
		return endpoint[:index]
	}
	return endpoint
}

// normalizeOSSEndpoint 补全协议（无 scheme 时默认 https）。
func normalizeOSSEndpoint(endpoint string) string {
	if endpoint == "" {
		return "https://oss-cn-shanghai.aliyuncs.com"
	}
	if !strings.Contains(endpoint, "://") {
		return "https://" + endpoint
	}
	return endpoint
}
