package api

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResetRequiresExplicitConfirmation(t *testing.T) {
	for _, body := range []string{"", `{}`, `{"confirm_reset":false}`, `{"confirm_reset":"true"}`} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		(&Server{}).resetCenterDeployment(c)
		if w.Code != 400 {
			t.Fatalf("body %q: got %d", body, w.Code)
		}
	}
}
