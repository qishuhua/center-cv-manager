// Package centertask 经中心网关查询业务任务的运行状态。数据源是中心本地
// Redis（infra 模块部署，ingress 是唯一写入方），ingress 在网关
// /api/v1/tasks 提供只读聚合接口；本包只负责拉取与错误翻译，不改变远端
// 状态，因此不套 centerlock、不做任务记录。任务量千级，整包返回全量清单，
// 分页展示由前端完成。
package centertask

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"center-cv-manager/internal/domain"
)

// Task 是中心任务状态的传输结构。时间为 epoch 毫秒，由前端负责格式化；
// Status 约定 created/running/finished/failed，未知值原样透传。
type Task struct {
	TaskID        string `json:"task_id"`
	Camera        string `json:"camera,omitempty"`
	Status        string `json:"status"`
	ViolationType int    `json:"violation_type,omitempty"`
	VideoType     string `json:"video_type,omitempty"` // "1" 历史回放 / "2" 实时
	StartTime     int64  `json:"start_time"`
	EndTime       *int64 `json:"end_time,omitempty"`
	Error         string `json:"error,omitempty"`
}

type Service struct {
	client *http.Client
}

func New() *Service {
	return &Service{client: &http.Client{Timeout: 5 * time.Second}}
}

// gatewayBase 与统一监控拉取健康信号、中心 Grafana 入口同址：SSH 主机 +
// 访问端口即中心网关 nginx，任务查询走网关统一入口而不是直连 Redis。
func gatewayBase(c domain.Center) string {
	port := c.ConsolePort
	if port <= 0 {
		port = 8080
	}
	return "http://" + net.JoinHostPort(strings.TrimSpace(c.Host), strconv.Itoa(port))
}

// List 拉取一个中心的全部任务清单（按 start_time 降序）。响应兼容两种
// 形态：裸 JSON 数组（当前契约），或 {"tasks":[...]} 信封（若中心侧后续
// 演进为分页接口）。网关不可达、软件版本不支持（404）与接口异常分别
// 翻译成可操作的错误信息，前端直接展示。
func (s *Service) List(ctx context.Context, c domain.Center) ([]Task, error) {
	endpoint := gatewayBase(c) + "/api/v1/tasks"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("中心网关不可达（检查中心网络或网关模块状态）: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("中心软件不支持任务查询：请先整体发布网关与业务模块")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("中心任务接口返回状态 %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("读取中心任务响应: %w", err)
	}
	var tasks []Task
	if err := json.Unmarshal(body, &tasks); err != nil {
		var envelope struct {
			Tasks []Task `json:"tasks"`
		}
		if envelopeErr := json.Unmarshal(body, &envelope); envelopeErr != nil || envelope.Tasks == nil {
			return nil, fmt.Errorf("解析中心任务响应: %w", err)
		}
		tasks = envelope.Tasks
	}
	if tasks == nil {
		tasks = []Task{}
	}
	// 网关返回顺序不确定（Redis 集合无序），统一按开始时间降序，保证
	// 前端分页顺序稳定、最新任务在前。
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].StartTime > tasks[j].StartTime })
	return tasks, nil
}
