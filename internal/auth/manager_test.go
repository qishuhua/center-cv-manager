package auth

import "testing"

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

func TestVerifyPasswordRequiresValidSessionAndCurrentPassword(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := m.Login("client", "admin", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.VerifyPassword(token, "wrong"); err != ErrInvalidAdminPassword {
		t.Fatalf("wrong password error = %v", err)
	}
	if err = m.VerifyPassword(token, "admin"); err != nil {
		t.Fatal(err)
	}
	m.Logout(token)
	if err = m.VerifyPassword(token, "admin"); err != ErrInvalidSession {
		t.Fatalf("expired session error = %v", err)
	}
}
