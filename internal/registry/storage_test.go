package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"center-cv-manager/internal/center"
	"center-cv-manager/internal/domain"
)

func TestSaveStorageConfigRendersWorkingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry", "config.yml")
	service := New(&fakeStore{}, path)

	out, err := service.SaveStorageConfig(domain.RegistryStorage{
		Region: "oss-cn-shanghai", RegionEndpoint: "https://oss-cn-shanghai.aliyuncs.com",
		Bucket: "pro-alg-train-store", AccessKeyID: "ak-1", AccessKeySecret: "sk-1",
		RootDirectory: "center-cv-registry",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.AccessKeySecret != center.Mask {
		t.Fatalf("返回的 secret 应打码: %q", out.AccessKeySecret)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	// 以下字段来自实测验证的可用组合（registry-test）：registry:3 必需
	// forcepathstyle: false，否则 OSS 拒绝；rootdirectory 进 bucket 隔离。
	for _, want := range []string{
		"forcepathstyle: false",
		"regionendpoint: https://oss-cn-shanghai.aliyuncs.com",
		"bucket: pro-alg-train-store",
		"rootdirectory: /center-cv-registry",
		"secretkey: sk-1",
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
	if !readBack.Exists || readBack.Bucket != "pro-alg-train-store" || readBack.AccessKeySecret != center.Mask {
		t.Fatalf("回读 = %+v", readBack)
	}

	// 掩码保存沿用旧 secret，且修改其他字段不丢凭据。
	if _, err = service.SaveStorageConfig(domain.RegistryStorage{
		Region: "us-east-1", RegionEndpoint: "https://oss-cn-shanghai.aliyuncs.com",
		Bucket: "other-bucket", AccessKeyID: "ak-1", AccessKeySecret: center.Mask,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "secretkey: sk-1") {
		t.Fatalf("掩码保存不应丢失旧 secret:\n%s", raw)
	}

	// 空输入被拒绝。
	if _, err = service.SaveStorageConfig(domain.RegistryStorage{}); err == nil {
		t.Fatal("缺少 Bucket/AK 应当被拒绝")
	}
}
