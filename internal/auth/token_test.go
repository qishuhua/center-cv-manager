package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAPITokenLifecycle(t *testing.T) {
	now := time.Now().UTC()
	dir := t.TempDir()
	m := &Manager{path: filepath.Join(dir, "auth.json"), users: []User{{Username: "alice", Role: RoleAdmin, CreatedAt: now}}, now: func() time.Time { return now }}
	info, raw, err := m.CreateAPIToken("alice", "测试", []string{"read", "release"}, []string{"center-a"}, false, 30)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), raw) || !strings.Contains(string(data), tokenHash(raw)) {
		t.Fatal("令牌存储不正确")
	}
	var stored authFile
	if err = json.Unmarshal(data, &stored); err != nil || len(stored.Tokens) != 1 || stored.Tokens[0].Hash != tokenHash(raw) {
		t.Fatalf("令牌持久化无效: %v", err)
	}
	if _, status := m.AuthenticateAPIToken(raw); !status.Authenticated {
		t.Fatal("令牌认证失败")
	}
	list, _ := json.Marshal(m.ListAPITokens("alice"))
	if strings.Contains(string(list), tokenHash(raw)) {
		t.Fatal("列表泄露令牌哈希")
	}
	m.path = filepath.Join(dir, "revoked.json")
	if err = m.RevokeAPIToken("alice", info.ID); err != nil {
		t.Fatal(err)
	}
	if _, status := m.AuthenticateAPIToken(raw); status.Authenticated {
		t.Fatal("撤销未生效")
	}
}
func TestAPITokenPermissionAndExpiry(t *testing.T) {
	now := time.Now().UTC()
	m := &Manager{path: filepath.Join(t.TempDir(), "auth.json"), users: []User{{Username: "alice", Role: RoleViewer, CreatedAt: now}}, now: func() time.Time { return now }}
	if _, _, err := m.CreateAPIToken("alice", "测试", []string{"read", "build"}, nil, false, 30); err == nil {
		t.Fatal("普通用户不应创建构建令牌")
	}
	_, raw, err := m.CreateAPIToken("alice", "测试", []string{"read"}, nil, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return now.Add(24 * time.Hour) }
	if _, status := m.AuthenticateAPIToken(raw); status.Authenticated {
		t.Fatal("过期令牌仍然有效")
	}
}
