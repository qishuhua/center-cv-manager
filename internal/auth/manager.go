package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	DefaultUsername   = "admin"
	DefaultPassword   = "admin"
	MinPasswordLength = 4
	SessionTTL        = 12 * time.Hour
	RoleAdmin         = "admin"
	RoleViewer        = "viewer"
)

var (
	ErrInvalidCredentials = errors.New("账号或密码错误")
	ErrLoginLocked        = errors.New("登录失败次数过多，请 5 分钟后重试")
	ErrInvalidSession     = errors.New("登录状态已失效")
	ErrUserExists         = errors.New("用户已存在")
	ErrUserNotFound       = errors.New("用户不存在")
	ErrInvalidRole        = errors.New("角色无效")
	ErrInvalidUsername    = errors.New("用户名仅支持 2-32 位字母、数字、下划线、点、横线")
	ErrLastAdmin          = errors.New("系统至少需要保留一个管理员")
	ErrDeleteSelf         = errors.New("不能删除当前登录账号")
	ErrSuperAdmin         = errors.New("admin 是超级管理员，不允许删除或降级")
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{2,32}$`)

// User 是 auth.json 中的一条用户记录；密码只保存 bcrypt 哈希。
type User struct {
	Username           string    `json:"username"`
	PasswordHash       string    `json:"password_hash"`
	MustChangePassword bool      `json:"must_change_password"`
	Role               string    `json:"role"`
	CreatedAt          time.Time `json:"created_at"`
}

// UserInfo 是对外的用户信息，不包含密码哈希。
type UserInfo struct {
	Username           string    `json:"username"`
	Role               string    `json:"role"`
	MustChangePassword bool      `json:"must_change_password"`
	CreatedAt          time.Time `json:"created_at"`
}

// authFile 是当前的凭据文件格式：多用户数组。
type authFile struct {
	Users []User `json:"users"`
}

// legacyAuthFile 是旧的单用户格式，读取时自动迁移为 users 数组。
type legacyAuthFile struct {
	Username           string `json:"username"`
	PasswordHash       string `json:"password_hash"`
	MustChangePassword bool   `json:"must_change_password"`
}

type session struct {
	Username  string
	ExpiresAt time.Time
}
type failure struct {
	Count       int
	LockedUntil time.Time
}
type Status struct {
	Authenticated      bool   `json:"authenticated"`
	Username           string `json:"username,omitempty"`
	Role               string `json:"role,omitempty"`
	MustChangePassword bool   `json:"must_change_password"`
}
type Manager struct {
	mu       sync.Mutex
	path     string
	users    []User
	sessions map[string]session
	failures map[string]failure
	now      func() time.Time
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
		var file authFile
		if err = json.Unmarshal(data, &file); err != nil {
			return fmt.Errorf("读取登录凭据失败: %w", err)
		}
		migrated := false
		if len(file.Users) == 0 {
			// 兼容旧的单用户格式：迁移为多用户并立即落盘。
			var legacy legacyAuthFile
			if err = json.Unmarshal(data, &legacy); err != nil || legacy.Username == "" || legacy.PasswordHash == "" {
				return errors.New("登录凭据文件不完整")
			}
			file.Users = []User{{Username: legacy.Username, PasswordHash: legacy.PasswordHash, MustChangePassword: legacy.MustChangePassword, Role: RoleAdmin, CreatedAt: m.now()}}
			migrated = true
		}
		m.users = file.Users
		fixed := enforceSuperAdmin(m.users)
		if err = m.validateUsersLocked(); err != nil {
			return err
		}
		if !migrated && !fixed {
			return nil
		}
		return m.saveLocked()
	}
	if !os.IsNotExist(err) {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(DefaultPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	m.users = []User{{Username: DefaultUsername, PasswordHash: string(hash), MustChangePassword: true, Role: RoleAdmin, CreatedAt: m.now()}}
	return m.saveLocked()
}
func (m *Manager) validateUsersLocked() error {
	admins := 0
	for _, u := range m.users {
		if u.Username == "" || u.PasswordHash == "" || !validRole(u.Role) {
			return errors.New("登录凭据文件不完整")
		}
		if u.Role == RoleAdmin {
			admins++
		}
	}
	if admins == 0 {
		return errors.New("登录凭据文件不完整：至少需要一个管理员")
	}
	return nil
}
func validRole(role string) bool {
	return role == RoleAdmin || role == RoleViewer
}

// enforceSuperAdmin 确保内建的 admin 账号始终是管理员；返回是否发生了修正。
func enforceSuperAdmin(users []User) bool {
	changed := false
	for i := range users {
		if users[i].Username == DefaultUsername && users[i].Role != RoleAdmin {
			users[i].Role = RoleAdmin
			changed = true
		}
	}
	return changed
}
func (m *Manager) Login(clientID, username, password string) (string, Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	failed := m.failures[clientID]
	if failed.LockedUntil.After(now) {
		return "", Status{}, ErrLoginLocked
	}
	u := m.findUserLocked(username)
	valid := u != nil && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
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
	m.sessions[token] = session{Username: u.Username, ExpiresAt: now.Add(SessionTTL)}
	return token, m.statusFor(u), nil
}
func (m *Manager) Authenticate(token string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[token]
	if !ok || !sess.ExpiresAt.After(m.now()) {
		delete(m.sessions, token)
		return Status{}
	}
	// 每次请求按会话用户名重新解析：删号立即失效，角色变更即时生效。
	u := m.findUserLocked(sess.Username)
	if u == nil {
		delete(m.sessions, token)
		return Status{}
	}
	return m.statusFor(u)
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
		delete(m.sessions, token)
		return ErrInvalidSession
	}
	u := m.findUserLocked(sess.Username)
	if u == nil {
		return ErrInvalidSession
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(oldPassword)) != nil {
		return errors.New("原密码错误")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(hash)
	u.MustChangePassword = false
	if err = m.saveLocked(); err != nil {
		return err
	}
	// 只吊销同一用户的其他会话，不影响其他登录中的用户。
	kept := map[string]session{token: sess}
	for t, s := range m.sessions {
		if s.Username != sess.Username {
			kept[t] = s
		}
	}
	m.sessions = kept
	return nil
}
func (m *Manager) SkipPasswordChange(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.sessionUserLocked(token)
	if u == nil {
		return ErrInvalidSession
	}
	u.MustChangePassword = false
	return m.saveLocked()
}
func (m *Manager) ListUsers() []UserInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]UserInfo, 0, len(m.users))
	for i := range m.users {
		result = append(result, publicUser(&m.users[i]))
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].Username < result[j].Username
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result
}
func publicUser(u *User) UserInfo {
	return UserInfo{Username: u.Username, Role: u.Role, MustChangePassword: u.MustChangePassword, CreatedAt: u.CreatedAt}
}
func (m *Manager) CreateUser(username, password, role string) (UserInfo, error) {
	if !usernamePattern.MatchString(username) {
		return UserInfo{}, ErrInvalidUsername
	}
	if len(password) < MinPasswordLength {
		return UserInfo{}, fmt.Errorf("密码至少需要 %d 个字符", MinPasswordLength)
	}
	if role == "" {
		role = RoleViewer
	}
	if !validRole(role) {
		return UserInfo{}, ErrInvalidRole
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return UserInfo{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.findUserLocked(username) != nil {
		return UserInfo{}, ErrUserExists
	}
	m.users = append(m.users, User{Username: username, PasswordHash: string(hash), MustChangePassword: true, Role: role, CreatedAt: m.now()})
	created := publicUser(&m.users[len(m.users)-1])
	return created, m.saveLocked()
}
func (m *Manager) DeleteUser(actor, username string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if username == DefaultUsername {
		return ErrSuperAdmin
	}
	if username == actor {
		return ErrDeleteSelf
	}
	index := -1
	for i := range m.users {
		if m.users[i].Username == username {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrUserNotFound
	}
	if m.users[index].Role == RoleAdmin && m.adminCountLocked() <= 1 {
		return ErrLastAdmin
	}
	m.users = append(m.users[:index], m.users[index+1:]...)
	if err := m.saveLocked(); err != nil {
		return err
	}
	m.revokeUserSessionsLocked(username)
	return nil
}
func (m *Manager) SetUserRole(username, role string) (UserInfo, error) {
	if !validRole(role) {
		return UserInfo{}, ErrInvalidRole
	}
	if username == DefaultUsername && role != RoleAdmin {
		return UserInfo{}, ErrSuperAdmin
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.findUserLocked(username)
	if u == nil {
		return UserInfo{}, ErrUserNotFound
	}
	if u.Role == RoleAdmin && role != RoleAdmin && m.adminCountLocked() <= 1 {
		return UserInfo{}, ErrLastAdmin
	}
	u.Role = role
	if err := m.saveLocked(); err != nil {
		return UserInfo{}, err
	}
	return publicUser(u), nil
}
func (m *Manager) ResetUserPassword(username, newPassword string) (UserInfo, error) {
	if len(newPassword) < MinPasswordLength {
		return UserInfo{}, fmt.Errorf("密码至少需要 %d 个字符", MinPasswordLength)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return UserInfo{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.findUserLocked(username)
	if u == nil {
		return UserInfo{}, ErrUserNotFound
	}
	u.PasswordHash = string(hash)
	u.MustChangePassword = true
	if err = m.saveLocked(); err != nil {
		return UserInfo{}, err
	}
	m.revokeUserSessionsLocked(username)
	return publicUser(u), nil
}
func (m *Manager) findUserLocked(username string) *User {
	for i := range m.users {
		if m.users[i].Username == username {
			return &m.users[i]
		}
	}
	return nil
}
func (m *Manager) adminCountLocked() int {
	count := 0
	for i := range m.users {
		if m.users[i].Role == RoleAdmin {
			count++
		}
	}
	return count
}
func (m *Manager) revokeUserSessionsLocked(username string) {
	for token, sess := range m.sessions {
		if sess.Username == username {
			delete(m.sessions, token)
		}
	}
}
func (m *Manager) sessionUserLocked(token string) *User {
	sess, ok := m.sessions[token]
	if !ok || !sess.ExpiresAt.After(m.now()) {
		delete(m.sessions, token)
		return nil
	}
	return m.findUserLocked(sess.Username)
}
func (m *Manager) statusFor(u *User) Status {
	return Status{Authenticated: true, Username: u.Username, Role: u.Role, MustChangePassword: u.MustChangePassword}
}
func (m *Manager) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(authFile{Users: m.users}, "", "  ")
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
