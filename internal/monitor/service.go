// Package monitor 渲染开发机统一监控栈（prometheus / alertmanager / grafana /
// dashboard-generator，静态配置在仓库 monitoring/ 目录）的动态配置，并把各
// 中心的健康拉取状态回传给管理端状态页。前身是独立的 center-cv-monitor 仓库：
// 指标和日志继续保存在中心本地，开发机只拉每个中心一条健康信号、发送失联
// 告警，Grafana 经中心公网 /prometheus/ 与 /elasticsearch/ 直接查询。
package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/secret"
	"github.com/docker/docker/client"
)

// logsPassword 是 Grafana 查询中心 Elasticsearch 日志的 Basic Auth 密码
// （用户 grafana_logs），与中心网关模板 internal/templates/secrets/
// grafana-logs.htpasswd 的哈希对应；service_test 保证两者不漂移。
const logsPassword = "_p-0UXyShh8Fwh5rdoDbJualwO3j9LSkY-A2mEAPVIE"

// alertmanagerContainer 是 compose 中 alertmanager 的容器名：url_file 变更后
// SIGHUP 触发整份配置重载。
const alertmanagerContainer = "center-cv-alertmanager"

// unconfiguredWebhook 是飞书 Webhook 未配置时写入 url_file 的占位地址：
// alertmanager 正常启动与接收告警，发送必然失败仅记日志，配置后重载生效。
const unconfiguredWebhook = "http://127.0.0.1:9/unconfigured-feishu-webhook"

// sitePattern 与 dashboard-generator 的中心标识校验一致；不匹配的中心直接
// 跳过，避免单条坏记录让生成器停止更新全部仪表盘。
var sitePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

type Store interface {
	ListCenters() ([]domain.Center, error)
	GetAlertCredential() (domain.AlertCredential, error)
}

type Service struct {
	store         Store
	cipher        *secret.Cipher
	root          string // 渲染根目录：root/targets/centers.json + root/secrets/*
	prometheusURL string
}

func New(store Store, cipher *secret.Cipher, root string, prometheusURL string) *Service {
	return &Service{store: store, cipher: cipher, root: root, prometheusURL: strings.TrimRight(prometheusURL, "/")}
}

// targetGroup 是 prometheus file_sd 的清单格式，也是 dashboard-generator 的
// 输入；site 用中心 ID 前 8 位短 slug，gpu_vendor 取检测平台。平台告警
// 静默中心（test_mode）不写入清单（完全不探测）。
type targetGroup struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

// Sync 渲染统一监控的全部动态配置：中心清单 centers.json、飞书 Webhook 与
// 日志查询密码两个 secret 文件。原子写且内容不变时跳过；prometheus file_sd
// 每分钟、dashboard-generator 每 15 秒轮询读取，渲染后无需通知监控栈。
// 唯一例外是飞书 Webhook 变更：url_file 只在配置加载时读取，需另行调用
// ReloadAlertmanager。
func (s *Service) Sync() error {
	centers, err := s.store.ListCenters()
	if err != nil {
		return fmt.Errorf("读取中心清单: %w", err)
	}
	groups := make([]targetGroup, 0, len(centers))
	seen := map[string]bool{}
	for _, c := range centers {
		// 平台告警静默中心完全不探测：不写入 targets（prometheus 不抓取，
		// 生成器随之自动清理其数据源与仪表盘），重新打开平台告警后自动恢复。
		if c.TestMode {
			continue
		}
		vendor := monitorVendor(c)
		slug := siteSlug(c.ID)
		if (vendor != "nvidia" && vendor != "denglin") || seen[slug] || !sitePattern.MatchString(slug) {
			continue
		}
		seen[slug] = true
		labels := map[string]string{"site": slug, "site_name": c.Name, "gpu_vendor": vendor}
		groups = append(groups, targetGroup{Targets: []string{centerTarget(c)}, Labels: labels})
	}
	rendered, err := json.MarshalIndent(groups, "", "  ")
	if err != nil {
		return err
	}
	if err = s.write(filepath.Join("targets", "centers.json"), append(rendered, '\n')); err != nil {
		return fmt.Errorf("渲染监控中心清单: %w", err)
	}

	credential, err := s.store.GetAlertCredential()
	if err != nil {
		return fmt.Errorf("读取飞书告警凭据: %w", err)
	}
	webhook := unconfiguredWebhook
	if credential.FeishuWebhook != "" {
		if webhook, err = s.cipher.Decrypt(credential.FeishuWebhook); err != nil {
			return fmt.Errorf("解密飞书 Webhook: %w", err)
		}
	}
	if err = s.write(filepath.Join("secrets", "feishu-webhook-url"), []byte(webhook+"\n")); err != nil {
		return fmt.Errorf("渲染飞书 Webhook: %w", err)
	}
	if err = s.write(filepath.Join("secrets", "center-logs-password"), []byte(logsPassword+"\n")); err != nil {
		return fmt.Errorf("渲染日志查询密码: %w", err)
	}
	return nil
}

// write 原子写入渲染文件（tmp + rename），内容未变时跳过，保持 mtime 稳定
// 供 generator 的签名比对短路。文件 0644 / 目录 0755：prometheus、
// alertmanager 以 nobody 运行，manager 属主的 0600/0700 会让它们无法读取
// （与旧项目 compose secrets 挂载 0444 的开放程度一致，卷内均为可信容器）。
func (s *Service) write(rel string, content []byte) error {
	path := filepath.Join(s.root, rel)
	if info, err := os.Stat(path); err == nil && info.Mode().Perm() == 0o644 {
		if old, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(old, content) {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 存量目录可能是早期 0700 创建的（nobody 进不去），幂等修正。
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// ReloadAlertmanager 向容器发 SIGHUP 重载配置（飞书 Webhook 变更后调用）。
// 经 FromEnv 使用本地引擎（compose 形态挂载 docker.sock）；本地 go run 或
// 监控栈未部署时返回错误，调用方按尽力而为提示即可。不依赖 build 包，
// 避免与 center 包形成导入环。
func (s *Service) ReloadAlertmanager(ctx context.Context) error {
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	defer docker.Close()
	return docker.ContainerKill(ctx, alertmanagerContainer, "SIGHUP")
}

// CenterStatus 是状态页里单个中心的监控情况。
type CenterStatus struct {
	CenterID  string `json:"center_id"`
	Name      string `json:"name"`
	// Site 是监控清单里的短标识，前端据此拼 Grafana 仪表盘链接
	// （/d/center-cv-<site>）。
	Site string `json:"site,omitempty"`
	// Target 是开发机拉取该中心的地址（host:port，与中心 Grafana 入口同址）。
	Target    string `json:"target,omitempty"`
	TestMode  bool   `json:"test_mode,omitempty"`
	Monitored bool   `json:"monitored"`
	// Reason 说明未纳入监控的原因（平台未检测或暂不支持）。
	Reason string `json:"reason,omitempty"`
	// Up 是开发机 Prometheus 最近一次健康信号：nil 表示尚未采集到
	// （新登记的目标等待第一轮抓取）。
	Up *bool `json:"up,omitempty"`
}

type Status struct {
	// StackOnline 表示开发机 Prometheus 可达（统一监控栈在运行）。
	StackOnline bool           `json:"stack_online"`
	Error       string         `json:"error,omitempty"`
	Centers     []CenterStatus `json:"centers"`
}

// Status 汇总各中心的纳入情况与健康信号；监控栈不可达时不报错，仅置
// StackOnline=false，状态页据此提示。
func (s *Service) Status(ctx context.Context) Status {
	out := Status{Centers: []CenterStatus{}}
	centers, err := s.store.ListCenters()
	if err != nil {
		out.Error = err.Error()
		return out
	}
	var upBySite map[string]bool
	if upBySite, err = s.queryUp(ctx); err != nil {
		out.Error = err.Error()
	} else {
		out.StackOnline = true
	}
	for _, c := range centers {
		item := CenterStatus{CenterID: c.ID, Name: c.Name, TestMode: c.TestMode}
		vendor := monitorVendor(c)
		switch {
		case c.TestMode:
			item.Reason = "平台告警已关闭：不探测，重新打开后自动纳入"
		case vendor != "nvidia" && vendor != "denglin":
			if vendor == "auto" || vendor == "" {
				item.Reason = "硬件平台未检测，请先执行硬件检测"
			} else {
				item.Reason = "平台暂不支持统一监控仪表盘（" + vendor + "）"
			}
		default:
			item.Monitored = true
			item.Site = siteSlug(c.ID)
			item.Target = centerTarget(c)
			if up, ok := upBySite[item.Site]; ok {
				item.Up = &up
			}
		}
		out.Centers = append(out.Centers, item)
	}
	return out
}

// centerTarget 返回开发机拉取该中心的地址：SSH 主机 + 访问端口，与运行详情
// 的中心 Grafana 入口（openCenterConsole）同址——中心网关 nginx 在同一入口
// 暴露 /prometheus/ 与 /elasticsearch/，路径由 dashboard-generator 拼接。
func centerTarget(c domain.Center) string {
	port := c.ConsolePort
	if port <= 0 {
		port = 8080
	}
	return net.JoinHostPort(strings.TrimSpace(c.Host), strconv.Itoa(port))
}

// siteSlug 把中心 ID 压成 Grafana 全链路可用的短标识：数据源 uid
// （center-prometheus-/center-elasticsearch- 前缀）与仪表盘 uid
// （center-cv-）都受 Grafana 40 字符上限约束，完整 uuid 放不下；取前 8 位
// 十六进制，几十个中心的碰撞概率可忽略，同一批渲染冲突时跳过后者
// （generator 遇到重复 site 会整体报错，宁缺勿坏）。
func siteSlug(centerID string) string {
	if len(centerID) > 8 {
		centerID = centerID[:8]
	}
	return strings.ToLower(centerID)
}

// monitorVendor 返回纳入监控使用的 gpu_vendor：优先硬件检测结果，仪表盘
// 模板仅覆盖 nvidia 与 denglin 两种平台。
func monitorVendor(c domain.Center) string {
	if c.DetectedAccelerator != "" {
		return c.DetectedAccelerator
	}
	return c.Accelerator
}

func (s *Service) queryUp(ctx context.Context) (map[string]bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.prometheusURL+"/api/v1/query", nil)
	if err != nil {
		return nil, err
	}
	query := request.URL.Query()
	query.Set("query", `up{job="federate-centers"}`)
	request.URL.RawQuery = query.Encode()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("开发机 Prometheus 不可达（统一监控栈未启动？）: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("开发机 Prometheus 返回状态 %d", response.StatusCode)
	}
	var document struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err = json.NewDecoder(response.Body).Decode(&document); err != nil {
		return nil, fmt.Errorf("解析 Prometheus 查询结果: %w", err)
	}
	upBySite := map[string]bool{}
	for _, result := range document.Data.Result {
		site, ok := result.Metric["site"]
		if !ok || len(result.Value) < 2 {
			continue
		}
		value, _ := result.Value[1].(string)
		upBySite[site] = value == "1"
	}
	return upBySite, nil
}
