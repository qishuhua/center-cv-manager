package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type client struct {
	base  string
	token string
	http  *http.Client
}

func schema(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{} // 无必填参数时变参为 nil，序列化成 null 会被客户端校验拒绝
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
func arr(items any) map[string]any { return map[string]any{"type": "array", "items": items} }
func tools() []tool {
	selection := schema(map[string]any{"source_id": str("软件仓库 ID"), "ref": str("分支或标签"), "commit": str("完整 commit"), "kind": str("版本类型，来自版本列表")}, "source_id", "ref", "commit", "kind")
	model := schema(map[string]any{"manifest_key": str("模型清单键"), "manifest_digest": str("模型清单摘要")}, "manifest_key", "manifest_digest")
	release := schema(map[string]any{"center_ids": arr(str("中心 ID")), "items": arr(selection), "model_selections": arr(model)}, "center_ids")
	empty := schema(map[string]any{})
	id := func(name string) map[string]any { return schema(map[string]any{name: str("标识")}, name) }
	return []tool{
		{Name: "list_centers", Description: "列出可用中心", InputSchema: empty},
		{Name: "list_sources", Description: "列出软件仓库", InputSchema: empty},
		{Name: "list_versions", Description: "查询软件远端分支、标签及 commit", InputSchema: id("source_id")},
		{Name: "list_models", Description: "列出模型版本及发布所需的清单键和摘要", InputSchema: empty},
		{Name: "list_registry_versions", Description: "列出已构建并推送的镜像版本", InputSchema: empty},
		{Name: "get_center_status", Description: "查看单个中心的实时运行状态：在线、显卡平台、容器列表、正在运行的软件版本与模型版本；经 SSH 读取，可能较慢", InputSchema: schema(map[string]any{"center_id": str("中心 ID")}, "center_id")},
		{Name: "get_center_hardware", Description: "检测并返回中心硬件信息：GPU 型号与数量、CPU、内存、磁盘；同时刷新平台缓存的硬件记录", InputSchema: schema(map[string]any{"center_id": str("中心 ID")}, "center_id")},
		{Name: "test_center", Description: "测试中心的 SSH 与 Docker 连通性，返回连接状态与 host key 指纹", InputSchema: schema(map[string]any{"center_id": str("中心 ID")}, "center_id")},
		{Name: "monitoring_status", Description: "统一监控各中心健康信号汇总（开发机 Prometheus 拉取口径；监控栈不可达时降级返回 stack_online=false）", InputSchema: empty},
		{Name: "list_tasks", Description: "列出任务记录，可按类型过滤：PUBLISH_IMAGE=镜像构建、RELEASE_VERSION=统一发布、MODEL_RELEASE=模型发布等", InputSchema: schema(map[string]any{"type": str("任务类型，可选"), "limit": map[string]any{"type": "integer", "description": "返回条数，默认 100"}}, )},
		{Name: "build_image", Description: "构建指定软件版本并推送镜像仓库；此操作会创建任务", InputSchema: schema(map[string]any{"source_id": str("软件仓库 ID"), "ref": str("分支或标签"), "commit": str("完整 commit"), "kind": str("版本类型")}, "source_id", "ref", "commit", "kind"), Annotations: map[string]any{"destructiveHint": true, "openWorldHint": true}},
		{Name: "convert_model", Description: "把模型版本的 ONNX 在指定平台（nvidia/denglin）构建机上转换为 TensorRT/登临格式，产物作为新模型版本入仓（版本号自动派生）；此操作会创建任务，需平台已配置转换设置", InputSchema: schema(map[string]any{"manifest_key": str("源模型版本的清单键"), "manifest_digest": str("源模型版本的清单摘要"), "platform": str("目标平台：nvidia 或 denglin")}, "manifest_key", "manifest_digest", "platform"), Annotations: map[string]any{"destructiveHint": true, "openWorldHint": true}},
		{Name: "plan_release", Description: "预检目标中心及指定软件和模型版本；软件镜像须已构建", InputSchema: release},
		{Name: "release", Description: "向指定中心发布软件或模型，调用前应展示预检并取得用户确认；提交时服务端再次检查", InputSchema: release, Annotations: map[string]any{"destructiveHint": true, "openWorldHint": true}},
		{Name: "module_action", Description: "对指定中心的模块执行整体操作：restart=内容未变的整体重启（跳过 init 服务）、stop=暂停（容器保留，依赖方外部访问可能 502）、deploy=整体重建；调用前应向用户确认；此操作会创建任务", InputSchema: schema(map[string]any{"center_id": str("中心 ID"), "module": str("模块 ID：business/gateway/infra/logging/monitoring"), "action": str("操作：restart、stop 或 deploy")}, "center_id", "module", "action"), Annotations: map[string]any{"destructiveHint": true, "openWorldHint": false}},
		{Name: "compose_publish", Description: "整体发布指定中心的模块 Compose（配置变化后重建受影响服务）；内容未变时须传 confirm_unchanged=true 才会整体重启；调用前应向用户确认；此操作会创建任务", InputSchema: schema(map[string]any{"center_id": str("中心 ID"), "module": str("模块 ID"), "confirm_unchanged": map[string]any{"type": "boolean", "description": "内容未变仍要整体重启时传 true，默认 false"}}, "center_id", "module"), Annotations: map[string]any{"destructiveHint": true, "openWorldHint": false}},
		{Name: "cancel_task", Description: "停止一个运行中或等待中的任务（发布、构建、转换、模块操作等）；已结束的任务不受影响", InputSchema: id("task_id"), Annotations: map[string]any{"destructiveHint": true, "openWorldHint": false}},
		{Name: "get_task", Description: "查询任务状态", InputSchema: id("task_id")},
		{Name: "get_task_logs", Description: "查询任务日志", InputSchema: id("task_id")},
	}
}
func newClient() (*client, error) {
	raw := strings.TrimRight(os.Getenv("CENTER_MANAGER_URL"), "/")
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("CENTER_MANAGER_URL 必须是有效的 HTTP(S) 地址")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme != "https" && host != "localhost" && (ip == nil || !ip.IsLoopback()) && os.Getenv("CENTER_MANAGER_ALLOW_INSECURE_HTTP") != "true" {
		return nil, errors.New("远程连接需要 HTTPS；受信任的内网可显式设置 CENTER_MANAGER_ALLOW_INSECURE_HTTP=true")
	}
	token := os.Getenv("CENTER_MANAGER_TOKEN")
	if token == "" {
		return nil, errors.New("请设置 CENTER_MANAGER_TOKEN")
	}
	return &client{base: raw, token: token, http: &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *client) call(method, path string, body any) (json.RawMessage, error) {
	var data io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		data = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+"/api/v1/mcp"+path, data)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var result struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("平台响应无效: %w", err)
	}
	if res.StatusCode >= 300 || result.Code != 0 {
		return nil, fmt.Errorf("平台请求失败 (%d): %s", res.StatusCode, result.Message)
	}
	return result.Data, nil
}
func (c *client) execute(name string, args map[string]json.RawMessage) (json.RawMessage, error) {
	value := func(key string) (string, error) {
		var v string
		if err := json.Unmarshal(args[key], &v); err != nil || strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("缺少有效参数 %s", key)
		}
		return v, nil
	}
	pathID := func(key string) (string, error) {
		v, err := value(key)
		if err != nil {
			return "", err
		}
		if strings.ContainsAny(v, "/\\") || v == "." || v == ".." {
			return "", errors.New("标识无效")
		}
		return url.PathEscape(v), nil
	}
	method, path := "GET", ""
	var body any
	switch name {
	case "list_centers":
		path = "/centers"
	case "list_sources":
		path = "/sources"
	case "list_models":
		path = "/models"
	case "list_registry_versions":
		path = "/registry/versions"
	case "get_center_status":
		v, e := pathID("center_id")
		if e != nil {
			return nil, e
		}
		path = "/centers/" + v + "/overview"
	case "get_center_hardware", "test_center":
		v, e := pathID("center_id")
		if e != nil {
			return nil, e
		}
		method = "POST"
		path = "/centers/" + v + "/detect"
		if name == "test_center" {
			path = "/centers/" + v + "/test"
		}
	case "monitoring_status":
		path = "/monitoring/status"
	case "list_tasks":
		q := url.Values{}
		var kind string
		if raw, exists := args["type"]; exists && json.Unmarshal(raw, &kind) == nil && strings.TrimSpace(kind) != "" {
			q.Set("type", kind)
		}
		var limit int
		if raw, exists := args["limit"]; exists && json.Unmarshal(raw, &limit) == nil && limit > 0 && limit <= 500 {
			q.Set("limit", strconv.Itoa(limit))
		}
		path = "/tasks"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
	case "list_versions":
		v, e := pathID("source_id")
		if e != nil {
			return nil, e
		}
		path = "/sources/" + v + "/versions"
	case "get_task", "get_task_logs":
		v, e := pathID("task_id")
		if e != nil {
			return nil, e
		}
		path = "/tasks/" + v
		if name == "get_task_logs" {
			path += "/logs"
		}
	case "build_image":
		v, e := pathID("source_id")
		if e != nil {
			return nil, e
		}
		for _, k := range []string{"ref", "commit", "kind"} {
			if _, e = value(k); e != nil {
				return nil, e
			}
		}
		method = "POST"
		path = "/sources/" + v + "/publish"
		body = args
	case "convert_model":
		for _, k := range []string{"manifest_key", "manifest_digest", "platform"} {
			if _, err := value(k); err != nil {
				return nil, err
			}
		}
		method = "POST"
		path = "/models/convert"
		body = args
	case "module_action":
		v, e := pathID("center_id")
		if e != nil {
			return nil, e
		}
		module, e := pathID("module")
		if e != nil {
			return nil, e
		}
		if _, e = value("action"); e != nil {
			return nil, e
		}
		method = "POST"
		path = "/centers/" + v + "/modules/" + module + "/actions"
		body = args
	case "compose_publish":
		v, e := pathID("center_id")
		if e != nil {
			return nil, e
		}
		module, e := pathID("module")
		if e != nil {
			return nil, e
		}
		method = "POST"
		path = "/centers/" + v + "/modules/" + module + "/publish"
		body = map[string]any{}
		var confirm bool
		if raw, exists := args["confirm_unchanged"]; exists && json.Unmarshal(raw, &confirm) == nil {
			body = map[string]any{"confirm_unchanged": confirm}
		}
	case "cancel_task":
		v, e := pathID("task_id")
		if e != nil {
			return nil, e
		}
		method = "POST"
		path = "/tasks/" + v + "/cancel"
	case "plan_release", "release":
		method = "POST"
		path = "/releases"
		if name == "plan_release" {
			path += "/plan"
		}
		body = args
	default:
		return nil, errors.New("未知工具")
	}
	return c.call(method, path, body)
}
func main() {
	c, err := newClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	out := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			out.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "Parse error"}})
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		var result any
		var rpcErr any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "center-cv-manager", "version": "1.0.0"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": tools()}
		case "tools/call":
			var p struct {
				Name      string                     `json:"name"`
				Arguments map[string]json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				rpcErr = map[string]any{"code": -32602, "message": "Invalid params"}
				break
			}
			data, e := c.execute(p.Name, p.Arguments)
			if e != nil {
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": e.Error()}}, "isError": true}
			} else {
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(data)}}}
			}
		default:
			rpcErr = map[string]any{"code": -32601, "message": "Method not found"}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rpcErr != nil {
			response["error"] = rpcErr
		} else {
			response["result"] = result
		}
		if err = out.Encode(response); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
	}
	if err = scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
