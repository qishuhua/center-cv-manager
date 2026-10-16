package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"center-cv-manager/internal/build"
	"center-cv-manager/internal/domain"
)

// TestListVersionsAndPlatformFilter 用假 registry 验证版本聚合：前缀外仓库
// 剔除、无来源标签镜像剔除、平台标签按版本聚合、按中心平台过滤。
func TestListVersionsAndPlatformFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		case request.URL.Path == "/v2/_catalog":
			_, _ = w.Write([]byte(`{"repositories":["center-cv/business","test/busybox"]}`))
		case request.URL.Path == "/v2/center-cv/business/tags/list":
			_, _ = w.Write([]byte(`{"tags":["v1-abc-nvidia","v1-abc-denglin","v2-def-nvidia","raw-tag"]}`))
		case request.URL.Path == "/v2/test/busybox/tags/list":
			_, _ = w.Write([]byte(`{"tags":["latest"]}`))
		case strings.HasPrefix(request.URL.Path, "/v2/center-cv/business/manifests/"):
			tag := strings.TrimPrefix(request.URL.Path, "/v2/center-cv/business/manifests/")
			if tag == "raw-tag" {
				// 无来源标签的镜像（如手工推送）应被剔除。
				w.Header().Set("Docker-Content-Digest", "sha256:"+tag)
				_, _ = w.Write([]byte(`{"config":{"digest":"sha256:config-raw"}}`))
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:manifest-"+tag)
			fmt.Fprintf(w, `{"config":{"digest":"sha256:config-%s"}}`, tag)
		case strings.HasPrefix(request.URL.Path, "/v2/center-cv/business/blobs/"):
			digest := strings.TrimPrefix(request.URL.Path, "/v2/center-cv/business/blobs/")
			switch digest {
			case "sha256:config-raw":
				_, _ = w.Write([]byte(`{"created":"2026-10-16T10:00:00Z","config":{}}`))
			case "sha256:config-v1-abc-nvidia", "sha256:config-v1-abc-denglin":
				fmt.Fprintf(w, `{"created":"2026-10-16T10:00:00Z","config":{"Labels":{"%s":"s1","%s":"business","%s":"v1","%s":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`,
					build.LabelSourceID, build.LabelSourceName, build.LabelSourceRef, build.LabelCommit)
			case "sha256:config-v2-def-nvidia":
				fmt.Fprintf(w, `{"created":"2026-10-16T11:00:00Z","config":{"Labels":{"%s":"s1","%s":"business","%s":"v2","%s":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}`,
					build.LabelSourceID, build.LabelSourceName, build.LabelSourceRef, build.LabelCommit)
			default:
				w.WriteHeader(http.StatusNotFound)
				return
			}
		default:
			t.Errorf("意外请求路径 %s", request.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	store := &fakeStore{settings: domain.RegistrySettings{Endpoint: server.URL}}
	service := New(store, nil, t.TempDir()+"/config.yml")
	list, err := service.ListVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Repository != "center-cv/business" || list[0].SourceID != "s1" || list[0].SourceName != "business" {
		t.Fatalf("ListVersions = %+v", list)
	}
	versions := list[0].Versions
	if len(versions) != 2 {
		t.Fatalf("版本数 = %d，期望 2（raw-tag 应被剔除）: %+v", len(versions), versions)
	}
	if versions[0].Version != "v2-def" || len(versions[0].Platforms) != 1 || versions[0].Platforms[0].Platform != "nvidia" {
		t.Fatalf("最新版本 = %+v", versions[0])
	}
	if versions[1].Version != "v1-abc" || len(versions[1].Platforms) != 2 {
		t.Fatalf("双平台版本 = %+v", versions[1])
	}

	nvidia := VersionsForPlatform(list, "nvidia")
	if len(nvidia) != 1 || len(nvidia[0].Versions) != 2 {
		t.Fatalf("nvidia 可用版本 = %+v", nvidia)
	}
	denglin := VersionsForPlatform(list, "denglin")
	if len(denglin) != 1 || len(denglin[0].Versions) != 1 || denglin[0].Versions[0].Version != "v1-abc" {
		t.Fatalf("denglin 可用版本 = %+v", denglin)
	}
	if got := TagForPlatform(versions[1], "denglin"); got != "v1-abc-denglin" {
		t.Fatalf("TagForPlatform = %q", got)
	}
	if version, platform := splitTag("plain"); version != "plain" || platform != "shared" {
		t.Fatalf("splitTag(plain) = %q, %q", version, platform)
	}
}

// TestDeleteVersionRemovesAllPlatformTags 验证按版本删除：命中版本的全部平台
// 标签 manifest 都被删除，其他版本不受影响，越界仓库被拒绝。
func TestDeleteVersionRemovesAllPlatformTags(t *testing.T) {
	deleted := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/v2/_catalog":
			_, _ = w.Write([]byte(`{"repositories":["center-cv/business"]}`))
		case request.URL.Path == "/v2/center-cv/business/tags/list":
			_, _ = w.Write([]byte(`{"tags":["v1-abc-nvidia","v1-abc-denglin","v2-def-nvidia"]}`))
		case request.Method == http.MethodHead && strings.HasPrefix(request.URL.Path, "/v2/center-cv/business/manifests/"):
			w.Header().Set("Docker-Content-Digest", "sha256:manifest-"+strings.TrimPrefix(request.URL.Path, "/v2/center-cv/business/manifests/"))
			w.WriteHeader(http.StatusOK)
		case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/v2/center-cv/business/manifests/"):
			deleted[strings.TrimPrefix(request.URL.Path, "/v2/center-cv/business/manifests/")] = true
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("意外请求 %s %s", request.Method, request.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	store := &fakeStore{settings: domain.RegistrySettings{Endpoint: server.URL}}
	service := New(store, nil, t.TempDir()+"/config.yml")

	if err := service.DeleteVersion(context.Background(), "center-cv/business", "v1-abc"); err != nil {
		t.Fatal(err)
	}
	// 版本的 nvidia/denglin 两个 manifest 都删了，v2 不受影响。
	if len(deleted) != 2 || !deleted["sha256:manifest-v1-abc-nvidia"] || !deleted["sha256:manifest-v1-abc-denglin"] {
		t.Fatalf("删除的 manifest = %#v", deleted)
	}
	if err := service.DeleteVersion(context.Background(), "center-cv/business", "v9-none"); err == nil {
		t.Fatal("不存在的版本应报错")
	}
	if err := service.DeleteVersion(context.Background(), "test/busybox", "latest"); err == nil {
		t.Fatal("前缀外仓库应被拒绝")
	}
}

// TestVersionsForPlatformSharedTag 验证通用（无平台后缀）标签对所有平台可用：
// 平台无关构建一次发布、单个通用标签，发布链路同样回退到通用标签取 digest。
func TestVersionsForPlatformSharedTag(t *testing.T) {
	list := []SourceVersions{{
		Repository: "center-cv/x", SourceID: "s1", SourceName: "x",
		Versions: []SoftwareVersion{{Version: "v0-aaa", Platforms: []PlatformTag{{Platform: "shared", Tag: "v0-aaa"}}}},
	}}
	for _, platform := range []string{"nvidia", "denglin", "cpu"} {
		filtered := VersionsForPlatform(list, platform)
		if len(filtered) != 1 || len(filtered[0].Versions) != 1 {
			t.Fatalf("平台 %s 应看到通用标签版本: %+v", platform, filtered)
		}
		if got := TagForPlatform(filtered[0].Versions[0], platform); got != "v0-aaa" {
			t.Fatalf("平台 %s 的标签 = %q", platform, got)
		}
	}
	// 平台后缀标签优先于通用标签。
	mixed := SoftwareVersion{Version: "v1-bbb", Platforms: []PlatformTag{
		{Platform: "shared", Tag: "v1-bbb"},
		{Platform: "nvidia", Tag: "v1-bbb-nvidia"},
	}}
	if got := TagForPlatform(mixed, "nvidia"); got != "v1-bbb-nvidia" {
		t.Fatalf("平台后缀应优先: %q", got)
	}
}
