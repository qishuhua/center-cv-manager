package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"center-cv-manager/internal/center"
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

// StorageConfig 读取已渲染的 registry config.yml（secretkey 打码返回）。
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
	out.Region = document.Storage.S3.Region
	out.RegionEndpoint = document.Storage.S3.RegionEndpoint
	out.Bucket = document.Storage.S3.Bucket
	out.AccessKeyID = document.Storage.S3.AccessKey
	if document.Storage.S3.SecretKey != "" {
		out.AccessKeySecret = center.Mask
	}
	out.RootDirectory = strings.Trim(document.Storage.S3.RootDirectory, "/")
	out.Exists = true
	return out, nil
}

// SaveStorageConfig 校验输入并把 config.yml 原子写入数据目录；secret 传入掩码
// 时沿用文件中的旧值。registry 容器只在启动时读取，保存后需重启该容器生效。
func (s *Service) SaveStorageConfig(input domain.RegistryStorage) (domain.RegistryStorage, error) {
	input.Region = strings.TrimSpace(input.Region)
	input.RegionEndpoint = strings.TrimSpace(input.RegionEndpoint)
	input.Bucket = strings.TrimSpace(input.Bucket)
	input.AccessKeyID = strings.TrimSpace(input.AccessKeyID)
	input.RootDirectory = strings.Trim(strings.TrimSpace(input.RootDirectory), "/")
	if input.Bucket == "" || input.AccessKeyID == "" {
		return domain.RegistryStorage{}, fmt.Errorf("Bucket 和 AccessKey ID 不能为空")
	}
	if input.RegionEndpoint == "" {
		input.RegionEndpoint = "https://oss-cn-shanghai.aliyuncs.com"
	}
	if !strings.HasPrefix(input.RegionEndpoint, "https://") && !strings.HasPrefix(input.RegionEndpoint, "http://") {
		return domain.RegistryStorage{}, fmt.Errorf("Region Endpoint 需为完整 URL（例如 https://oss-cn-shanghai.aliyuncs.com）")
	}
	if input.RootDirectory == "" {
		input.RootDirectory = "registry"
	}
	if strings.Contains(input.RootDirectory, "..") || strings.ContainsAny(input.RootDirectory, " \t\n") {
		return domain.RegistryStorage{}, fmt.Errorf("根目录前缀不能包含 .. 或空白字符")
	}
	secret := input.AccessKeySecret
	if secret == "" || secret == center.Mask {
		existing, err := s.StorageConfig()
		if err != nil {
			return domain.RegistryStorage{}, err
		}
		if existing.Exists {
			raw, readErr := os.ReadFile(s.configPath)
			if readErr != nil {
				return domain.RegistryStorage{}, readErr
			}
			var document storageDocument
			if readErr = yaml.Unmarshal(raw, &document); readErr != nil {
				return domain.RegistryStorage{}, readErr
			}
			secret = document.Storage.S3.SecretKey
		}
	}
	if secret == "" || secret == center.Mask {
		return domain.RegistryStorage{}, fmt.Errorf("AccessKey Secret 不能为空")
	}

	document := storageDocument{Version: "0.1"}
	document.Log.Level = "info"
	document.Storage.S3.AccessKey = input.AccessKeyID
	document.Storage.S3.SecretKey = secret
	document.Storage.S3.Region = input.Region
	document.Storage.S3.RegionEndpoint = input.RegionEndpoint
	document.Storage.S3.Bucket = input.Bucket
	document.Storage.S3.RootDirectory = "/" + input.RootDirectory
	document.Storage.S3.Secure = strings.HasPrefix(input.RegionEndpoint, "https://")
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
	input.AccessKeySecret = center.Mask
	input.Exists = true
	input.UpdatedAt = time.Now()
	return input, nil
}
