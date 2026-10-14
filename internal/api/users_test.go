package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"center-cv-manager/internal/auth"
	"center-cv-manager/internal/store"
	"center-cv-manager/internal/task"
	"github.com/gin-gonic/gin"
)

func newRBACServer(t *testing.T) (*Server, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	authManager, err := auth.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tasks, err := task.New(st)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{auth: authManager, store: st, tasks: tasks}
	return s, s.Router()
}

// loginAs 登录并在必要时完成首登改密，返回携带会话 Cookie 的请求发送函数。
func loginAs(t *testing.T, router *gin.Engine, username, password string) func(method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	send := func(method, path string, body any, cookie string) *httptest.ResponseRecorder {
		var reader *bytes.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			reader = bytes.NewReader(data)
		} else {
			reader = bytes.NewReader(nil)
		}
		request := httptest.NewRequest(method, path, reader)
		request.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			request.Header.Set("Cookie", cookie)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	response := send(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": username, "password": password}, "")
	if response.Code != 200 {
		t.Fatalf("login %s failed: %d %s", username, response.Code, response.Body.String())
	}
	cookie := response.Header().Get("Set-Cookie")
	if strings.Contains(cookie, "must_change_password\":true") || strings.Contains(response.Body.String(), `"must_change_password":true`) {
		response = send(http.MethodPost, "/api/v1/auth/change-password", map[string]string{"old_password": password, "new_password": password + "-x"}, cookie)
		if response.Code != 200 {
			t.Fatalf("change password for %s failed: %d %s", username, response.Code, response.Body.String())
		}
	}
	return func(method, path string, body any) *httptest.ResponseRecorder {
		return send(method, path, body, cookie)
	}
}

func TestRBACRouteMatrix(t *testing.T) {
	_, router := newRBACServer(t)

	anonymous := func(method, path string, body any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		request := httptest.NewRequest(method, path, bytes.NewReader(data))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	if response := anonymous(http.MethodGet, "/api/v1/centers", nil); response.Code != 401 {
		t.Fatalf("anonymous GET /centers = %d", response.Code)
	}
	if response := anonymous(http.MethodGet, "/api/v1/users", nil); response.Code != 401 {
		t.Fatalf("anonymous GET /users = %d", response.Code)
	}

	admin := loginAs(t, router, "admin", "admin")
	if response := admin(http.MethodPost, "/api/v1/users", map[string]string{"username": "viewer1", "password": "viewer-pass", "role": "viewer"}); response.Code != 200 {
		t.Fatalf("create viewer = %d %s", response.Code, response.Body.String())
	}
	if response := admin(http.MethodPost, "/api/v1/users", map[string]string{"username": "viewer1", "password": "viewer-pass"}); response.Code != 409 {
		t.Fatalf("duplicate user = %d %s", response.Code, response.Body.String())
	}

	viewer := loginAs(t, router, "viewer1", "viewer-pass")
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/users"},
		{http.MethodPost, "/api/v1/centers"},
		{http.MethodPost, "/api/v1/centers/c1/reset"},
		{http.MethodPost, "/api/v1/centers/c1/modules/business/publish"},
		{http.MethodPut, "/api/v1/git-credential"},
		{http.MethodPost, "/api/v1/tasks/cancel-all"},
		{http.MethodGet, "/api/v1/centers/c1/terminal"},
	} {
		if response := viewer(tc.method, tc.path, map[string]string{}); response.Code != 403 {
			t.Fatalf("viewer %s %s = %d %s", tc.method, tc.path, response.Code, response.Body.String())
		}
	}
	if response := viewer(http.MethodGet, "/api/v1/tasks", nil); response.Code != 200 {
		t.Fatalf("viewer GET /tasks = %d %s", response.Code, response.Body.String())
	}

	// 用户管理与保护逻辑：admin 是超级管理员，任何时候都不允许删除或降级。
	if response := admin(http.MethodDelete, "/api/v1/users/admin", nil); response.Code != 409 {
		t.Fatalf("delete super admin = %d %s", response.Code, response.Body.String())
	}
	if response := admin(http.MethodPut, "/api/v1/users/admin/role", map[string]string{"role": "viewer"}); response.Code != 409 {
		t.Fatalf("demote super admin = %d %s", response.Code, response.Body.String())
	}
	if response := admin(http.MethodPut, "/api/v1/users/viewer1/role", map[string]string{"role": "admin"}); response.Code != 200 {
		t.Fatalf("promote viewer = %d %s", response.Code, response.Body.String())
	}
	if response := admin(http.MethodPut, "/api/v1/users/admin/role", map[string]string{"role": "viewer"}); response.Code != 409 {
		t.Fatalf("super admin must stay immutable even with other admins: %d %s", response.Code, response.Body.String())
	}
	// 普通管理员被降级后原会话立即失去权限。
	if response := admin(http.MethodPut, "/api/v1/users/viewer1/role", map[string]string{"role": "viewer"}); response.Code != 200 {
		t.Fatalf("demote viewer1 = %d %s", response.Code, response.Body.String())
	}
	if response := viewer(http.MethodGet, "/api/v1/users", nil); response.Code != 403 {
		t.Fatalf("demoted admin must lose admin routes: %d", response.Code)
	}
}

func TestResetPasswordRevokesSessions(t *testing.T) {
	_, router := newRBACServer(t)
	admin := loginAs(t, router, "admin", "admin")
	if response := admin(http.MethodPost, "/api/v1/users", map[string]string{"username": "user2", "password": "user2-pass"}); response.Code != 200 {
		t.Fatalf("create user2 = %d %s", response.Code, response.Body.String())
	}
	user2 := loginAs(t, router, "user2", "user2-pass")
	if response := user2(http.MethodGet, "/api/v1/tasks", nil); response.Code != 200 {
		t.Fatalf("user2 GET /tasks = %d", response.Code)
	}
	if response := admin(http.MethodPost, "/api/v1/users/user2/reset-password", map[string]string{"password": "user2-new-pass"}); response.Code != 200 {
		t.Fatalf("reset password = %d %s", response.Code, response.Body.String())
	}
	if response := user2(http.MethodGet, "/api/v1/tasks", nil); response.Code != 401 {
		t.Fatalf("reset must revoke target sessions: %d", response.Code)
	}
	// 新密码重新登录（默认 viewer 角色）。
	user2New := loginAs(t, router, "user2", "user2-new-pass")
	if response := user2New(http.MethodGet, "/api/v1/tasks", nil); response.Code != 200 {
		t.Fatalf("user2 re-login GET /tasks = %d", response.Code)
	}
	if response := user2New(http.MethodDelete, "/api/v1/users/user2", nil); response.Code != 403 {
		t.Fatalf("default role must be viewer: %d", response.Code)
	}
}

func TestMustChangePasswordBlocksProtectedRoutes(t *testing.T) {
	_, router := newRBACServer(t)
	admin := loginAs(t, router, "admin", "admin")
	if response := admin(http.MethodPost, "/api/v1/users", map[string]string{"username": "fresh", "password": "fresh-pass"}); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	data, _ := json.Marshal(map[string]string{"username": "fresh", "password": "fresh-pass"})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	cookie := response.Header().Get("Set-Cookie")
	request = httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	request.Header.Set("Cookie", cookie)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 403 || !strings.Contains(response.Body.String(), "修改密码") {
		t.Fatalf("fresh user must be blocked until password change: %d %s", response.Code, response.Body.String())
	}
}
