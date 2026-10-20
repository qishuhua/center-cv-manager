package center

import (
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/secret"
	"testing"
)

func TestSaveDefaultsAndValidatesConsolePort(t *testing.T) {
	cipher, err := secret.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := &resetRepository{}
	s := New(repo, cipher)
	saved, err := s.Save(domain.Center{Name: "中心", Host: "host", Username: "root", AuthType: "password", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Port != 22 || repo.value.Port != 22 {
		t.Fatalf("SSH 端口默认值应为 22，实际 %d", repo.value.Port)
	}
	if repo.value.ConsolePort != 8080 {
		t.Fatalf("对外访问端口默认值应为 8080，实际 %d", repo.value.ConsolePort)
	}
	if _, err = s.Save(domain.Center{ID: repo.value.ID, Name: "中心", Host: "host", Username: "root", AuthType: "password", Password: Mask, Port: 22, ConsolePort: 9090}); err != nil {
		t.Fatal(err)
	}
	if repo.value.ConsolePort != 9090 {
		t.Fatalf("自定义对外访问端口应为 9090，实际 %d", repo.value.ConsolePort)
	}
	if _, err = s.Save(domain.Center{ID: repo.value.ID, Name: "中心", Host: "host", Username: "root", AuthType: "password", Password: Mask, Port: 22, ConsolePort: 70000}); err == nil {
		t.Fatal("非法对外访问端口应被拒绝")
	}
}

func TestValidateMisSortFields(t *testing.T) {
	base := domain.Center{Name: "中心", Host: "host", Port: 22, ConsolePort: 8080, Username: "root", AuthType: "password", Accelerator: "auto", ComposeFile: "~/.local/share/center-manager/deploy/docker-compose.yml"}
	cases := []struct {
		name    string
		wms     string
		organ   string
		misAuth string
		wantErr bool
	}{
		{"空配置可用", "", "", "", false},
		{"完整配置可用", "http://10.21.61.48:31995", "637101", "20260606123a", false},
		{"缺机构编码", "http://h:31995", "", "auth", true},
		{"缺 misAuth", "http://h:31995", "637101", "", true},
		{"无协议纯地址", "10.21.61.48:31995", "637101", "auth", true},
		{"带业务路径", "http://h:31995/missort", "637101", "auth", true},
		{"带查询参数", "http://h:31995?a=1", "637101", "auth", true},
		{"非 http 协议", "ftp://h:31995", "637101", "auth", true},
		{"带凭据", "http://user:pass@h", "637101", "auth", true},
		{"机构编码含空白", "http://h", "63 7101", "auth", true},
		{"misAuth 含控制字符", "http://h", "637101", "auth\x00x", true},
	}
	for _, tc := range cases {
		v := base
		v.WMSURL, v.OrganCode, v.MisAuth = tc.wms, tc.organ, tc.misAuth
		if err := validate(v); (err != nil) != tc.wantErr {
			t.Fatalf("%s: 期望错误=%v，实际 err=%v", tc.name, tc.wantErr, err)
		}
	}
}

func TestSaveMisSortNormalizesAndRejectsLegacy(t *testing.T) {
	cipher, err := secret.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := &resetRepository{domain.Center{ID: "c1", Name: "中心", Host: "host", Username: "root", AuthType: "password", Password: "encrypted", ComposeFile: "/srv/deploy/docker-compose.yml"}}
	repo.value.Deployment = modular.NewDeployment(repo.value.ComposeFile, 1)
	s := New(repo, cipher)
	saved, err := s.Save(domain.Center{ID: "c1", Name: "中心", Host: "host", Username: "root", AuthType: "password", Password: Mask, ComposeFile: "/srv/deploy/docker-compose.yml", WMSURL: " http://h:31995/ ", OrganCode: "637101"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.WMSURL != "http://h:31995" {
		t.Fatalf("WMS 地址应规范化为 http://h:31995，实际 %q", saved.WMSURL)
	}
	if saved.MisAuth != "20260606123a" {
		t.Fatalf("misAuth 留空应回填默认值，实际 %q", saved.MisAuth)
	}
	repo.value.Deployment = nil
	if _, err = s.Save(domain.Center{ID: "c1", Name: "中心", Host: "host", Username: "root", AuthType: "password", Password: Mask, ComposeFile: "/srv/deploy/docker-compose.yml", WMSURL: "http://h", OrganCode: "637101", MisAuth: "auth"}); err == nil {
		t.Fatal("旧中心（未模块化）应拒绝错分检测配置")
	}
}
