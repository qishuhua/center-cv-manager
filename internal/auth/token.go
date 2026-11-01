package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"strings"
	"time"
)

type APIToken struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Prefix        string     `json:"prefix"`
	Hash          string     `json:"-"`
	Username      string     `json:"username"`
	UserCreatedAt time.Time  `json:"user_created_at"`
	Scopes        []string   `json:"scopes"`
	CenterIDs     []string   `json:"center_ids"`
	AllCenters    bool       `json:"all_centers"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
}

// tokenRecord 单独保存哈希，避免对外返回令牌摘要。
type tokenRecord struct {
	APIToken
	Hash string `json:"hash"`
}

func (t APIToken) MarshalJSON() ([]byte, error)  { return marshalToken(t) }
func (t *APIToken) UnmarshalJSON(b []byte) error { return unmarshalToken(b, t) }
func tokenHash(raw string) string                { h := sha256.Sum256([]byte(raw)); return hex.EncodeToString(h[:]) }
func (m *Manager) CreateAPIToken(username, name string, scopes, centers []string, all bool, days int) (APIToken, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.findUserLocked(username)
	if u == nil || u.MustChangePassword {
		return APIToken{}, "", errors.New("账号不可用")
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 || days < 1 || days > 365 {
		return APIToken{}, "", errors.New("名称或有效期无效（有效期为 1 至 365 天）")
	}
	seen := map[string]bool{}
	for _, s := range scopes {
		if s != "read" && s != "build" && s != "release" {
			return APIToken{}, "", errors.New("权限无效")
		}
		seen[s] = true
	}
	if !seen["read"] || (u.Role != RoleAdmin && (seen["build"] || seen["release"])) {
		return APIToken{}, "", errors.New("权限超出账号权限，且必须包含读取权限")
	}
	if seen["release"] && !all && len(centers) == 0 {
		return APIToken{}, "", errors.New("请选择发布中心")
	}
	for _, id := range centers {
		if strings.TrimSpace(id) == "" {
			return APIToken{}, "", errors.New("中心 ID 无效")
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return APIToken{}, "", err
	}
	raw := "cmcp_" + base64.RawURLEncoding.EncodeToString(b)
	now := m.now().UTC()
	t := APIToken{ID: tokenHash(raw)[:24], Name: name, Prefix: raw[:13], Hash: tokenHash(raw), Username: username, UserCreatedAt: u.CreatedAt, Scopes: scopes, CenterIDs: centers, AllCenters: all, CreatedAt: now, ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour)}
	m.tokens = append(m.tokens, t)
	if err := m.saveLocked(); err != nil {
		m.tokens = m.tokens[:len(m.tokens)-1]
		return APIToken{}, "", err
	}
	return t, raw, nil
}
func (m *Manager) ListAPITokens(username string) []APIToken {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []APIToken{}
	for _, t := range m.tokens {
		if t.Username == username {
			t.Hash = ""
			out = append(out, t)
		}
	}
	return out
}
func (m *Manager) RevokeAPIToken(username, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, t := range m.tokens {
		if t.Username == username && t.ID == id {
			old := m.tokens
			m.tokens = append(append([]APIToken{}, old[:i]...), old[i+1:]...)
			if err := m.saveLocked(); err != nil {
				m.tokens = old
				return err
			}
			return nil
		}
	}
	return ErrUserNotFound
}
func (m *Manager) AuthenticateAPIToken(raw string) (APIToken, Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !strings.HasPrefix(raw, "cmcp_") {
		return APIToken{}, Status{}
	}
	h := tokenHash(raw)
	for i, t := range m.tokens {
		if t.Hash != h || !m.now().Before(t.ExpiresAt) {
			continue
		}
		u := m.findUserLocked(t.Username)
		if u == nil || !u.CreatedAt.Equal(t.UserCreatedAt) || u.MustChangePassword {
			return APIToken{}, Status{}
		}
		now := m.now().UTC()
		if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= time.Minute {
			previous := m.tokens[i].LastUsedAt
			m.tokens[i].LastUsedAt = &now
			if err := m.saveLocked(); err != nil {
				m.tokens[i].LastUsedAt = previous
				log.Printf("记录令牌使用时间失败: %v", err)
			} else {
				t.LastUsedAt = &now
			}
		}
		return t, m.statusFor(u)
	}
	return APIToken{}, Status{}
}

func (m *Manager) removeUserTokensLocked(username string) {
	kept := make([]APIToken, 0, len(m.tokens))
	for _, t := range m.tokens {
		if t.Username != username {
			kept = append(kept, t)
		}
	}
	m.tokens = kept
}
