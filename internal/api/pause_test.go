package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPauseAndResumeRequireExplicitConfirmation(t *testing.T) {
	for _, body := range []string{"", `{}`, `{"confirm":false}`, `{"confirm":"true"}`} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		(&Server{}).pauseCenter(c)
		if w.Code != 400 {
			t.Fatalf("pause body %q: got %d", body, w.Code)
		}
		w = httptest.NewRecorder()
		c, _ = gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		(&Server{}).resumeCenter(c)
		if w.Code != 400 {
			t.Fatalf("resume body %q: got %d", body, w.Code)
		}
	}
}
