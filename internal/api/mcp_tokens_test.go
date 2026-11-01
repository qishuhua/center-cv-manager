package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"center-cv-manager/internal/auth"
	"github.com/gin-gonic/gin"
)

func TestMCPTokenScopeAndCenter(t *testing.T) {
	m, err := auth.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := m.Login("test", "admin", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.SkipPasswordChange(session); err != nil {
		t.Fatal(err)
	}
	_, raw, err := m.CreateAPIToken("admin", "测试", []string{"read", "release"}, []string{"allowed"}, false, 30)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{auth: m}
	r := gin.New()
	r.POST("/release", s.requireToken("release", true), func(c *gin.Context) { c.Status(204) })
	r.POST("/build", s.requireToken("build", false), func(c *gin.Context) { c.Status(204) })
	check := func(path, body string, want int) {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s: 状态 %d，期望 %d", path, w.Code, want)
		}
	}
	check("/release", `{"center_ids":["allowed"]}`, 204)
	check("/release", `{"center_ids":["other"]}`, 403)
	check("/release", `{"center_ids":["allowed","other"]}`, 403)
	check("/build", `{}`, 403)
}
