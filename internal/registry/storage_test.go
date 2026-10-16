package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/secret"
)

// TestSaveStorageConfigRendersFromGlobalOSS 验证渲染：凭据、Bucket 与 Endpoint
// 全部来自模型仓库的全局 OSS 配置，镜像仓库只贡献存储前缀。
func TestSaveStorageConfigRendersFromGlobalOSS(t *testing.T) {
	keyDir := t.TempDir()
	cipher, err := secret.LoadOrCreate(keyDir)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("sk-1")
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{oss: domain.OSSCredential{
		Endpoint: "https://oss-cn-shanghai.aliyuncs.com", Bucket: "pro-alg-train-store",
		AccessKeyID: "ak-1", AccessKeySecret: encrypted, Configured: true,
	}}
	path := filepath.Join(t.TempDir(), "registry", "config.yml")
	service := New(store, cipher, path)

	out, err := service.SaveStorageConfig(domain.RegistryStorage{RootDirectory: "center-cv-registry"})
	if err != nil {
		t.Fatal(err)
	}
	if out.RootDirectory != "center-cv-registry" || !out.Exists {
		t.Fatalf("out = %+v", out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	// 关键字段来自实测验证的可用组合（registry-test）：registry:3 必需
	// forcepathstyle: false；凭据与 Bucket 复用全局 OSS 配置。
	for _, want := range []string{
		"forcepathstyle: false",
		"regionendpoint: https://oss-cn-shanghai.aliyuncs.com",
		"region: oss-cn-shanghai",
		"bucket: pro-alg-train-store",
		"accesskey: ak-1",
		"secretkey: sk-1",
		"rootdirectory: /center-cv-registry",
		"addr: :5000",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("渲染的 config.yml 缺少 %q:\n%s", want, content)
		}
	}

	readBack, err := service.StorageConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !readBack.Exists || readBack.RootDirectory != "center-cv-registry" {
		t.Fatalf("回读 = %+v", readBack)
	}

	// 全局 OSS 未配置时给出明确指引，而不是渲染半套配置。
	empty := New(&fakeStore{}, cipher, path)
	if _, err = empty.SaveStorageConfig(domain.RegistryStorage{}); err == nil || !strings.Contains(err.Error(), "模型仓库") {
		t.Fatalf("缺少全局 OSS 配置时应报错并指引: %v", err)
	}
	// 空前缀回退默认值；非法前缀拒绝。
	if out, err = service.SaveStorageConfig(domain.RegistryStorage{}); err != nil || out.RootDirectory != "registry" {
		t.Fatalf("空前缀应回退默认 registry: %+v, %v", out, err)
	}
	if _, err = service.SaveStorageConfig(domain.RegistryStorage{RootDirectory: "../escape"}); err == nil {
		t.Fatal("越界前缀应当被拒绝")
	}
}
