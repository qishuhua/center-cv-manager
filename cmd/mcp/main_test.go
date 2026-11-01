package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRemoteOperationsAndSessionRenewal(t *testing.T) {
	builds, plans, actions, publishes, probes := 0, 0, 0, 0, 0
	t.Setenv("CENTER_MANAGER_URL", "https://example.com")
	t.Setenv("CENTER_MANAGER_TOKEN", "cmcp_test")
	c, err := newClient()
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		code, body := 200, `{"code":0,"data":{"id":"task-1"}}`
		header := make(http.Header)
		if r.Header.Get("Authorization") != "Bearer cmcp_test" {
			t.Error("令牌未发送")
		}
		switch r.URL.Path {
		case "/api/v1/mcp/sources/src/publish":
			builds++
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			if r.Method != "POST" || b["commit"] != "abc" || b["ref"] != "main" {
				t.Errorf("构建参数错误: %v", b)
			}
		case "/api/v1/mcp/releases/plan":
			plans++
		case "/api/v1/mcp/centers/c1/modules/business/actions":
			actions++
			if r.Method != "POST" {
				t.Errorf("模块操作应为 POST: %s", r.Method)
			}
		case "/api/v1/mcp/centers/c1/modules/gateway/publish":
			publishes++
			if r.Method != "POST" {
				t.Errorf("整体发布应为 POST: %s", r.Method)
			}
		case "/api/v1/mcp/centers/c1/detect", "/api/v1/mcp/centers/c1/test",
			"/api/v1/mcp/monitoring/status", "/api/v1/mcp/tasks/task-1/cancel":
			probes++
		default:
			t.Errorf("意外路径: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: code, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	args := map[string]json.RawMessage{"source_id": json.RawMessage(`"src"`), "ref": json.RawMessage(`"main"`), "commit": json.RawMessage(`"abc"`), "kind": json.RawMessage(`"branch"`)}
	result, err := c.execute("build_image", args)
	if err != nil || !strings.Contains(string(result), "task-1") {
		t.Fatalf("构建失败: %s %v", result, err)
	}
	if _, err = c.execute("plan_release", map[string]json.RawMessage{"center_ids": json.RawMessage(`["c1"]`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.execute("module_action", map[string]json.RawMessage{"center_id": json.RawMessage(`"c1"`), "module": json.RawMessage(`"business"`), "action": json.RawMessage(`"restart"`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.execute("compose_publish", map[string]json.RawMessage{"center_id": json.RawMessage(`"c1"`), "module": json.RawMessage(`"gateway"`), "confirm_unchanged": json.RawMessage(`true`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.execute("get_center_hardware", map[string]json.RawMessage{"center_id": json.RawMessage(`"c1"`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.execute("test_center", map[string]json.RawMessage{"center_id": json.RawMessage(`"c1"`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.execute("monitoring_status", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = c.execute("cancel_task", map[string]json.RawMessage{"task_id": json.RawMessage(`"task-1"`)}); err != nil {
		t.Fatal(err)
	}
	if builds != 1 || plans != 1 || actions != 1 || publishes != 1 || probes != 4 {
		t.Fatalf("调用次数错误: 构建 %d 预检 %d 模块操作 %d 发布 %d 探测 %d", builds, plans, actions, publishes, probes)
	}
	if _, err = c.execute("get_task", map[string]json.RawMessage{"task_id": json.RawMessage(`"../users"`)}); err == nil {
		t.Fatal("应拒绝路径注入")
	}
}
func TestToolSchemasMarshalRequiredAsArray(t *testing.T) {
	for _, tl := range tools() {
		data, err := json.Marshal(tl.InputSchema)
		if err != nil {
			t.Fatalf("%s: %v", tl.Name, err)
		}
		var s struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(data, &s); err != nil || s.Required == nil {
			t.Errorf("%s: inputSchema.required 不是数组: %s", tl.Name, data)
		}
	}
}
func TestRejectInsecureRemote(t *testing.T) {
	t.Setenv("CENTER_MANAGER_URL", "http://example.com:8099")
	t.Setenv("CENTER_MANAGER_USERNAME", "u")
	t.Setenv("CENTER_MANAGER_PASSWORD", "p")
	t.Setenv("CENTER_MANAGER_ALLOW_INSECURE_HTTP", "")
	if _, err := newClient(); err == nil {
		t.Fatal("应拒绝远程明文连接")
	}
}
