package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPublishSourceImageRequiresValidSelection(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"ref":"","commit":"a"}`,
		`{"ref":"master","commit":"not-a-commit"}`,
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "id", Value: "s1"}}
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		(&Server{}).publishSourceImage(c)
		if w.Code != 400 {
			t.Fatalf("body %q: got %d %s", body, w.Code, w.Body.String())
		}
	}
}

func TestSoftwareImagesRouteRequiresAuth(t *testing.T) {
	_, router := newRBACServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/centers/c1/software/images", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("anonymous software images = %d", response.Code)
	}
	_, router = newRBACServer(t)
	request = httptest.NewRequest(http.MethodDelete, "/api/v1/centers/c1/software/images", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("anonymous delete software image = %d", response.Code)
	}
}

func TestDeleteSoftwareImageRequiresSelection(t *testing.T) {
	for _, body := range []string{"", `{}`, `{"id":""}`, `{"id":"  "}`} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "id", Value: "c1"}}
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		(&Server{}).deleteCenterSoftwareImage(c)
		if w.Code != 400 {
			t.Fatalf("body %q: got %d %s", body, w.Code, w.Body.String())
		}
	}
}
