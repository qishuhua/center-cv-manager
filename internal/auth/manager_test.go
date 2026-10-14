package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFirstLoginChangeOrSkip(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token, status, err := m.Login("client", "admin", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !status.MustChangePassword {
		t.Fatal("first login must suggest password change")
	}
	if status.Role != RoleAdmin {
		t.Fatalf("seeded admin role = %q", status.Role)
	}
	if err = m.SkipPasswordChange(token); err != nil {
		t.Fatal(err)
	}
	if m.Authenticate(token).MustChangePassword {
		t.Fatal("skip was not persisted")
	}
}
func TestChangePassword(t *testing.T) {
	dataDir := t.TempDir()
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	token, _, _ := m.Login("client", "admin", "admin")
	otherToken, _, _ := m.Login("other", "admin", "admin")
	if err = m.SkipPasswordChange(token); err != nil {
		t.Fatal(err)
	}
	if err = m.ChangePassword(token, "wrong", "new-password"); err == nil {
		t.Fatal("wrong current password accepted")
	}
	if err = m.ChangePassword(token, "admin", "123"); err == nil {
		t.Fatal("short password accepted")
	}
	if err = m.ChangePassword(token, "admin", "new-password"); err != nil {
		t.Fatal(err)
	}
	if !m.Authenticate(token).Authenticated || m.Authenticate(otherToken).Authenticated {
		t.Fatal("password change must retain current session and revoke other sessions")
	}
	reloaded, err := NewManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, status, err := reloaded.Login("reload", "admin", "new-password"); err != nil || status.MustChangePassword {
		t.Fatalf("password change was not persisted: %v", err)
	}
	m.Logout(token)
	if _, _, err = m.Login("client", "admin", "admin"); err == nil {
		t.Fatal("old password still works")
	}
	if _, _, err = m.Login("other", "admin", "new-password"); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyAuthFileMigrates(t *testing.T) {
	dataDir := t.TempDir()
	legacy := `{"username":"admin","password_hash":"$2a$10$abcdefghijklmnopqrstuvwxyz012345678901234567890123456789012","must_change_password":false}`
	if err := os.WriteFile(filepath.Join(dataDir, "auth.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	users := m.ListUsers()
	if len(users) != 1 || users[0].Username != "admin" || users[0].Role != RoleAdmin {
		t.Fatalf("legacy file did not migrate to one admin: %+v", users)
	}
	reloaded, err := NewManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ListUsers()[0].Username != "admin" {
		t.Fatal("migrated file must reload as users array")
	}
}

func TestCreateUserRolesAndLogin(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.CreateUser("alice", "123", RoleViewer); err == nil {
		t.Fatal("short password accepted")
	}
	if _, err = m.CreateUser("bad name!", "secret", RoleViewer); err != ErrInvalidUsername {
		t.Fatalf("invalid username error = %v", err)
	}
	if _, err = m.CreateUser("alice", "secret-pass", "root"); err != ErrInvalidRole {
		t.Fatalf("invalid role error = %v", err)
	}
	if _, err = m.CreateUser("alice", "secret-pass", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = m.CreateUser("alice", "another-pass", RoleViewer); err != ErrUserExists {
		t.Fatal("duplicate username accepted")
	}
	token, status, err := m.Login("client", "alice", "secret-pass")
	if err != nil {
		t.Fatal(err)
	}
	if status.Role != RoleViewer || !status.MustChangePassword {
		t.Fatalf("viewer status = %+v", status)
	}
	if err = m.ChangePassword(token, "secret-pass", "alice-new-pass"); err != nil {
		t.Fatal(err)
	}
	if m.Authenticate(token).MustChangePassword {
		t.Fatal("change password must clear the per-user flag")
	}
}

func TestUserManagementGuards(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// admin 是超级管理员：即使存在其他管理员也不允许降级或删除。
	if _, err = m.SetUserRole("admin", RoleViewer); err != ErrSuperAdmin {
		t.Fatalf("demote super admin error = %v", err)
	}
	if err = m.DeleteUser("admin", "admin"); err != ErrSuperAdmin {
		t.Fatalf("super admin self delete error = %v", err)
	}
	if err = m.DeleteUser("admin", "ghost"); err != ErrUserNotFound {
		t.Fatalf("missing user error = %v", err)
	}
	if _, err = m.CreateUser("bob", "bob-pass-123", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	// 其他管理员同样不能动 admin；自身删除防护对普通管理员生效。
	if err = m.DeleteUser("bob", "admin"); err != ErrSuperAdmin {
		t.Fatalf("delete super admin by another admin error = %v", err)
	}
	if _, err = m.SetUserRole("admin", RoleViewer); err != ErrSuperAdmin {
		t.Fatalf("demote super admin with another admin present error = %v", err)
	}
	if _, err = m.SetUserRole("admin", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err = m.DeleteUser("bob", "bob"); err != ErrDeleteSelf {
		t.Fatalf("self delete error = %v", err)
	}
	if _, err = m.SetUserRole("bob", RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err = m.DeleteUser("admin", "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err = m.SetUserRole("admin", RoleViewer); err != ErrSuperAdmin {
		t.Fatal("super admin guard must always hold")
	}
}

func TestDeleteAndResetKillSessions(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.CreateUser("carol", "carol-pass-1", RoleViewer); err != nil {
		t.Fatal(err)
	}
	token, _, err := m.Login("client", "carol", "carol-pass-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.ResetUserPassword("carol", "carol-pass-2"); err != nil {
		t.Fatal(err)
	}
	if m.Authenticate(token).Authenticated {
		t.Fatal("password reset must revoke existing sessions")
	}
	token, status, err := m.Login("client", "carol", "carol-pass-2")
	if err != nil {
		t.Fatal(err)
	}
	if !status.MustChangePassword {
		t.Fatal("reset password must force change at next login")
	}
	if err = m.DeleteUser("admin", "carol"); err != nil {
		t.Fatal(err)
	}
	if m.Authenticate(token).Authenticated {
		t.Fatal("deleted user's session must be revoked")
	}
	if _, _, err = m.Login("client", "carol", "carol-pass-2"); err != ErrInvalidCredentials {
		t.Fatalf("deleted user login error = %v", err)
	}
}

func TestChangePasswordOnlyRevokesSameUserSessions(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.CreateUser("dave", "dave-pass-123", RoleViewer); err != nil {
		t.Fatal(err)
	}
	adminToken, _, _ := m.Login("a", "admin", "admin")
	viewerToken, _, _ := m.Login("b", "dave", "dave-pass-123")
	if err = m.ChangePassword(adminToken, "admin", "admin-new-pass"); err != nil {
		t.Fatal(err)
	}
	if !m.Authenticate(viewerToken).Authenticated {
		t.Fatal("password change must not revoke other users' sessions")
	}
	if m.Authenticate(adminToken).Role != RoleAdmin {
		t.Fatal("role must be reported on authentication")
	}
	if m.Authenticate(viewerToken).Role != RoleViewer {
		t.Fatal("viewer role must be reported on authentication")
	}
}

func TestSuperAdminRoleRestoredOnLoad(t *testing.T) {
	dataDir := t.TempDir()
	m, err := NewManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.SetUserRole("admin", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	// 手工改库把 admin 降级后，重新加载必须恢复为管理员。
	if err = os.WriteFile(filepath.Join(dataDir, "auth.json"), []byte(`{"users":[{"username":"admin","password_hash":"$2a$10$abcdefghijklmnopqrstuvwxyz012345678901234567890123456789012","must_change_password":false,"role":"viewer","created_at":"2026-01-01T00:00:00Z"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range reloaded.ListUsers() {
		if u.Username == DefaultUsername && u.Role != RoleAdmin {
			t.Fatalf("super admin role must be restored on load, got %q", u.Role)
		}
	}
}
