package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	DefaultUsername   = "admin"
	DefaultPassword   = "admin"
	MinPasswordLength = 4
	SessionTTL        = 12 * time.Hour
)

var (
	ErrInvalidCredentials   = errors.New("账号或密码错误")
	ErrInvalidAdminPassword = errors.New("管理员密码错误")
	ErrLoginLocked          = errors.New("登录失败次数过多，请 5 分钟后重试")
	ErrInvalidSession       = errors.New("登录状态已失效")
)

type credentials struct {
	Username           string `json:"username"`
	PasswordHash       string `json:"password_hash"`
	MustChangePassword bool   `json:"must_change_password"`
}
type session struct{ ExpiresAt time.Time }
type failure struct {
	Count       int
	LockedUntil time.Time
}
type Status struct {
	Authenticated      bool   `json:"authenticated"`
	Username           string `json:"username,omitempty"`
	MustChangePassword bool   `json:"must_change_password"`
}
type Manager struct {
	mu          sync.Mutex
	path        string
	credentials credentials
	sessions    map[string]session
	failures    map[string]failure
	now         func() time.Time
}

func NewManager(dataDir string) (*Manager, error) {
	m := &Manager{path: filepath.Join(dataDir, "auth.json"), sessions: map[string]session{}, failures: map[string]failure{}, now: time.Now}
	if err := m.loadOrCreate(); err != nil {
		return nil, err
	}
	return m, nil
}
func (m *Manager) loadOrCreate() error {
	data, err := os.ReadFile(m.path)
	if err == nil {
		if err = json.Unmarshal(data, &m.credentials); err != nil {
			return fmt.Errorf("读取登录凭据失败: %w", err)
		}
		if m.credentials.Username == "" || m.credentials.PasswordHash == "" {
			return errors.New("登录凭据文件不完整")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(DefaultPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	m.credentials = credentials{Username: DefaultUsername, PasswordHash: string(hash), MustChangePassword: true}
	return m.saveLocked()
}
func (m *Manager) Login(clientID, username, password string) (string, Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	failed := m.failures[clientID]
	if failed.LockedUntil.After(now) {
		return "", Status{}, ErrLoginLocked
	}
	valid := username == m.credentials.Username && bcrypt.CompareHashAndPassword([]byte(m.credentials.PasswordHash), []byte(password)) == nil
	if !valid {
		failed.Count++
		if failed.Count >= 5 {
			failed.Count = 0
			failed.LockedUntil = now.Add(5 * time.Minute)
		}
		m.failures[clientID] = failed
		return "", Status{}, ErrInvalidCredentials
	}
	delete(m.failures, clientID)
	token, err := randomToken()
	if err != nil {
		return "", Status{}, err
	}
	m.sessions[token] = session{ExpiresAt: now.Add(SessionTTL)}
	return token, m.statusLocked(true), nil
}
func (m *Manager) Authenticate(token string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[token]
	if !ok || !sess.ExpiresAt.After(m.now()) {
		delete(m.sessions, token)
		return Status{}
	}
	return m.statusLocked(true)
}
func (m *Manager) VerifyPassword(token, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[token]
	if !ok || !sess.ExpiresAt.After(m.now()) {
		delete(m.sessions, token)
		return ErrInvalidSession
	}
	if bcrypt.CompareHashAndPassword([]byte(m.credentials.PasswordHash), []byte(password)) != nil {
		return ErrInvalidAdminPassword
	}
	return nil
}
func (m *Manager) Logout(token string) { m.mu.Lock(); defer m.mu.Unlock(); delete(m.sessions, token) }
func (m *Manager) ChangePassword(token, oldPassword, newPassword string) error {
	if len(newPassword) < MinPasswordLength {
		return fmt.Errorf("新密码至少需要 %d 个字符", MinPasswordLength)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[token]
	if !ok || !sess.ExpiresAt.After(m.now()) {
		return ErrInvalidSession
	}
	if bcrypt.CompareHashAndPassword([]byte(m.credentials.PasswordHash), []byte(oldPassword)) != nil {
		return errors.New("原密码错误")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	m.credentials.PasswordHash = string(hash)
	m.credentials.MustChangePassword = false
	if err = m.saveLocked(); err != nil {
		return err
	}
	m.sessions = map[string]session{token: sess}
	return nil
}
func (m *Manager) SkipPasswordChange(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[token]
	if !ok || !sess.ExpiresAt.After(m.now()) {
		return ErrInvalidSession
	}
	m.credentials.MustChangePassword = false
	return m.saveLocked()
}
func (m *Manager) statusLocked(authenticated bool) Status {
	return Status{Authenticated: authenticated, Username: m.credentials.Username, MustChangePassword: m.credentials.MustChangePassword}
}
func (m *Manager) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.credentials, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err = os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return err
	}
	if err = os.Rename(tmp, m.path); err != nil {
		return err
	}
	return os.Chmod(m.path, 0600)
}
func randomToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
