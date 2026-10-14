package center

import (
	"center-cv-manager/internal/domain"
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
