package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"center-cv-manager/internal/domain"
)

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantBase string
		wantHost string
		wantErr  bool
	}{
		{name: "标准地址", endpoint: "http://127.0.0.1:5000", wantBase: "http://127.0.0.1:5000", wantHost: "127.0.0.1:5000"},
		{name: "省略协议默认 http", endpoint: "127.0.0.1:5000", wantBase: "http://127.0.0.1:5000", wantHost: "127.0.0.1:5000"},
		{name: "https 保留", endpoint: "https://registry.internal:5000", wantBase: "https://registry.internal:5000", wantHost: "registry.internal:5000"},
		{name: "缺少端口", endpoint: "http://127.0.0.1", wantErr: true},
		{name: "非法地址", endpoint: "http://", wantErr: true},
		{name: "空地址", endpoint: " ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, host, err := ParseEndpoint(tt.endpoint)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseEndpoint(%q) 期望报错", tt.endpoint)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseEndpoint(%q) 报错: %v", tt.endpoint, err)
			}
			if base != tt.wantBase || host != tt.wantHost {
				t.Fatalf("ParseEndpoint(%q) = %q, %q；期望 %q, %q", tt.endpoint, base, host, tt.wantBase, tt.wantHost)
			}
		})
	}
}

func TestRepoPathAndReference(t *testing.T) {
	src := domain.Source{ID: "s1", Name: "Center-CV Service"}
	if got := RepoPath(src); got != "center-cv/center-cv-service" {
		t.Fatalf("RepoPath = %q", got)
	}
	if got := Reference("127.0.0.1:5000", "center-cv/business", "v1-abc-nvidia"); got != "127.0.0.1:5000/center-cv/business:v1-abc-nvidia" {
		t.Fatalf("Reference = %q", got)
	}
}

func TestClientManifestDigest(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case "/v2/center-cv/business/manifests/v1-abc-nvidia":
			if request.Method != http.MethodHead {
				t.Errorf("manifest 请求方法 = %s，期望 HEAD", request.Method)
			}
			w.Header().Set("Docker-Content-Digest", digest)
			w.WriteHeader(http.StatusOK)
		case "/v2/center-cv/business/manifests/missing":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("意外请求路径 %s", request.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	got, err := client.ManifestDigest(context.Background(), "center-cv/business", "v1-abc-nvidia")
	if err != nil || got != digest {
		t.Fatalf("ManifestDigest = %q, %v；期望 %q", got, err, digest)
	}
	got, err = client.ManifestDigest(context.Background(), "center-cv/business", "missing")
	if err != nil || got != "" {
		t.Fatalf("ManifestDigest(缺失) = %q, %v；期望空串", got, err)
	}
	if _, err = client.ManifestDigest(context.Background(), "center-cv/../business", "v1"); err == nil {
		t.Fatal("越界仓库路径应当被拒绝")
	}
}

type fakeStore struct {
	settings domain.RegistrySettings
}

func (f *fakeStore) GetRegistrySettings() (domain.RegistrySettings, error) { return f.settings, nil }
func (f *fakeStore) SaveRegistrySettings(v domain.RegistrySettings) error  { f.settings = v; return nil }

func TestServiceSettings(t *testing.T) {
	store := &fakeStore{}
	service := New(store, t.TempDir()+"/config.yml")
	if _, err := service.SaveSettings(domain.RegistrySettings{Endpoint: "http://127.0.0.1:5000"}); err != nil {
		t.Fatal(err)
	}
	v, err := service.Settings()
	if err != nil || !v.Configured || v.Endpoint != "http://127.0.0.1:5000" {
		t.Fatalf("Settings = %+v, %v", v, err)
	}
	if _, err = service.SaveSettings(domain.RegistrySettings{Endpoint: "http://127.0.0.1"}); err == nil {
		t.Fatal("缺少端口的地址应当被拒绝")
	}
	if _, err = service.SaveSettings(domain.RegistrySettings{Endpoint: ""}); err != nil {
		t.Fatalf("清空地址（关闭仓库模式）报错: %v", err)
	}
	if v, err = service.Settings(); err != nil || v.Configured {
		t.Fatalf("清空后 Configured 应为 false: %+v, %v", v, err)
	}
	if _, _, err := ParseEndpoint(""); err == nil {
		t.Fatal("空地址应当报错")
	}
}
