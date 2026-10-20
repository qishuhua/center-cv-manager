// Package templates 从文件系统读取 compose、nginx、监控等配置模板。
// Render 按部署模式把所需文件落地到 deployRoot，
// 保持 compose 内相对挂载路径（./monitoring/...、./nginx/...）的目录结构。
package templates

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// templateFiles 默认定位源码目录，服务启动时通过 SetDirectory 指定容器资源目录。
var templateFiles = sourceDirectory()

func sourceDirectory() fs.FS {
	dir, err := os.Getwd()
	if err != nil {
		return os.DirFS(".")
	}
	for {
		for _, candidate := range []string{filepath.Join(dir, "internal", "templates"), dir} {
			if info, err := os.Stat(filepath.Join(candidate, "compose")); err == nil && info.IsDir() {
				return os.DirFS(candidate)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return os.DirFS("internal/templates")
		}
		dir = parent
	}
}

// SetDirectory 在启动服务前配置模板目录，运行期间不应修改。
func SetDirectory(dir string) error {
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for _, name := range []string{"compose", "nginx", "monitoring", "secrets", "modules"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			return fmt.Errorf("读取模板目录 %s: %w", name, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("模板路径 %s 不是目录", name)
		}
	}
	templateFiles = os.DirFS(root)
	return nil
}

// FileSpec 一个模板文件：模板目录 内路径 -> 落地目标相对路径。
type FileSpec struct {
	EmbedPath  string // 模板目录 内路径，如 "compose/docker-compose.denglin.yml"
	TargetPath string // 相对 deployRoot 的目标路径，如 "docker-compose.denglin.yml"
}

// composeForMode 返回指定模式的 compose 文件名（不含目录前缀）。
func composeForMode(mode string) string {
	if mode == "tensorrt" {
		return "docker-compose.nvidia.yml"
	}
	return fmt.Sprintf("docker-compose.%s.yml", mode)
}

// nginxForMode 返回指定模式的 nginx 配置文件名。
func nginxForMode(mode string) string {
	if mode == "business" {
		return "nginx.conf"
	}
	return "nginx.full.conf"
}

// prometheusForMode 返回指定模式的 prometheus 配置文件名。
func prometheusForMode(mode string) string {
	if mode == "tensorrt" {
		return "prometheus.nvidia.yml"
	}
	return "prometheus.denglin.yml"
}

// SpecsForMode 返回指定部署模式需要落地的全部模板文件。
// business 模式精简：只有 compose + nginx，不含监控栈。
// tensorrt/denglin 模式完整：含监控、日志、grafana。
func SpecsForMode(mode string) ([]FileSpec, error) {
	var specs []FileSpec

	// 1. compose 文件
	specs = append(specs, FileSpec{
		EmbedPath:  filepath.Join("compose", composeForMode(mode)),
		TargetPath: composeForMode(mode),
	})

	// 2. nginx 配置
	nginxName := nginxForMode(mode)
	specs = append(specs, FileSpec{
		EmbedPath:  filepath.Join("nginx", nginxName),
		TargetPath: filepath.Join("nginx", nginxName),
	})

	// 3. nginx Basic Auth 凭据文件（nginx.conf 的 /elasticsearch/ 反代依赖它，缺失会导致 nginx 启动失败）
	specs = append(specs, FileSpec{
		EmbedPath:  filepath.Join("secrets", "grafana-logs.htpasswd"),
		TargetPath: filepath.Join("secrets", "grafana-logs.htpasswd"),
	})

	// business 模式到此为止（无监控栈）
	if mode == "business" {
		return specs, nil
	}

	// 3. prometheus 配置（tensorrt 用 nvidia 版）
	promName := prometheusForMode(mode)
	specs = append(specs, FileSpec{
		EmbedPath:  filepath.Join("monitoring", "prometheus", promName),
		TargetPath: filepath.Join("monitoring", "prometheus", promName),
	})
	if mode == "denglin" {
		specs = append(specs, FileSpec{
			EmbedPath:  filepath.Join("monitoring", "denglin-gpu-exporter", "denglin-gpu-exporter.json"),
			TargetPath: filepath.Join("monitoring", "denglin-gpu-exporter", "denglin-gpu-exporter.json"),
		})
	}

	// 4. rabbitmq 配置 + init 脚本
	for _, name := range []string{"rabbitmq.conf", "enabled_plugins", "init.sh"} {
		specs = append(specs, FileSpec{
			EmbedPath:  filepath.Join("monitoring", "rabbitmq", name),
			TargetPath: filepath.Join("monitoring", "rabbitmq", name),
		})
	}

	// 5. blackbox
	specs = append(specs, FileSpec{
		EmbedPath:  filepath.Join("monitoring", "blackbox", "blackbox.yml"),
		TargetPath: filepath.Join("monitoring", "blackbox", "blackbox.yml"),
	})

	// 6. filebeat
	specs = append(specs, FileSpec{
		EmbedPath:  filepath.Join("monitoring", "filebeat", "filebeat.yml"),
		TargetPath: filepath.Join("monitoring", "filebeat", "filebeat.yml"),
	})

	// 7. elasticsearch ILM 策略
	specs = append(specs, FileSpec{
		EmbedPath:  filepath.Join("monitoring", "elasticsearch", "center-cv-logs-ilm-policy.json"),
		TargetPath: filepath.Join("monitoring", "elasticsearch", "center-cv-logs-ilm-policy.json"),
	})

	// 8. Kibana Data View 初始化
	specs = append(specs, FileSpec{
		EmbedPath:  filepath.Join("monitoring", "kibana", "init.sh"),
		TargetPath: filepath.Join("monitoring", "kibana", "init.sh"),
	})

	// 9. mtail 日志指标程序
	specs = append(specs, FileSpec{
		EmbedPath:  filepath.Join("monitoring", "mtail", "log_errors.mtail"),
		TargetPath: filepath.Join("monitoring", "mtail", "log_errors.mtail"),
	})

	// 10. 告警规则、Alertmanager 配置和飞书 Webhook 示例。
	for _, rel := range []string{
		"alertmanager.yml",
		"feishu-webhook-url.example",
		"rules/gpu.yml",
		"rules/host.yml",
		"rules/rabbitmq.yml",
		"rules/exporters.yml",
		"rules/logs.yml",
		"rules/services.yml",
		"templates/feishu.tmpl",
	} {
		specs = append(specs, FileSpec{
			EmbedPath:  filepath.Join("monitoring", "alerting", filepath.FromSlash(rel)),
			TargetPath: filepath.Join("monitoring", "alerting", filepath.FromSlash(rel)),
		})
	}

	// 11. grafana provisioning + 当前硬件平台的 dashboard
	dashboardProvisioning := "provisioning/dashboards/dashboards.yml"
	dashboardName := "center-cv-overview.nvidia.json"
	if mode == "denglin" {
		dashboardProvisioning = "provisioning/dashboards-denglin/dashboards.yml"
		dashboardName = "center-cv-overview.denglin.json"
	}
	grafanaFiles := []string{
		"provisioning/datasources-host/prometheus.yml",
		"provisioning/datasources-host/elasticsearch.yml",
		dashboardProvisioning,
		"dashboards/" + dashboardName,
	}
	for _, rel := range grafanaFiles {
		specs = append(specs, FileSpec{
			EmbedPath:  filepath.Join("monitoring", "grafana", filepath.FromSlash(rel)),
			TargetPath: filepath.Join("monitoring", "grafana", filepath.FromSlash(rel)),
		})
	}

	return specs, nil
}

// Render 把指定模式的模板文件落地到 deployRoot。
// 落地后 deployRoot 下保持 compose/nginx/monitoring 目录结构，
// docker compose 在 deployRoot 下执行时相对挂载路径自然匹配。
type RenderOptions struct {
	Workers            int
	CPUSets            []string
	ModelsRoot         string
	LogsRoot           string
	MediaResourcesRoot string
	WorkerEnv          map[string]string
	ServerName         string
	FeishuWebhook      string
	// WMSURL/OrganCode/MisAuth 为错分检测配置：WMSURL 非空时 business 模块
	// 渲染错分审核服务并在 module.env 写入两个服务共用的取值。
	WMSURL    string
	OrganCode string
	MisAuth   string
}

// Render 使用统一配置渲染部署模板。WorkerEnv 会写入公共锚点，确保所有 Worker 一致。
func Render(mode, deployRoot string, opts RenderOptions, log func(string)) error {
	specs, err := SpecsForMode(mode)
	if err != nil {
		return fmt.Errorf("解析模板清单失败: %w", err)
	}
	if err := os.MkdirAll(deployRoot, 0o755); err != nil {
		return fmt.Errorf("创建部署目录失败: %w", err)
	}
	if err := cleanupStaleManagedTemplates(deployRoot, specs, log); err != nil {
		return err
	}

	for _, spec := range specs {
		content, err := fs.ReadFile(templateFiles, filepath.ToSlash(spec.EmbedPath))
		if err != nil {
			return fmt.Errorf("读取模板 %s 失败: %w", spec.EmbedPath, err)
		}
		content = []byte(strings.ReplaceAll(string(content), "\r\n", "\n"))
		if spec.EmbedPath == filepath.Join("compose", composeForMode(mode)) && mode != "business" {
			content = renderWorkerEnvironment(content, opts.WorkerEnv)
			content, err = renderDynamicWorkers(content, mode, opts.Workers, opts.CPUSets)
			if err != nil {
				return fmt.Errorf("生成 Worker 配置失败: %w", err)
			}
		}
		if spec.EmbedPath == filepath.Join("compose", composeForMode(mode)) {
			content = renderHostMounts(content, mode, opts.ModelsRoot, opts.LogsRoot, opts.MediaResourcesRoot)
			if err := validateGeneratedCompose(content); err != nil {
				return fmt.Errorf("模板 %s: %w", spec.EmbedPath, err)
			}
		}
		if spec.TargetPath == filepath.Join("monitoring", "prometheus", prometheusForMode(mode)) && mode != "business" {
			content, err = renderDynamicPrometheus(content, mode, opts.Workers)
			if err != nil {
				return fmt.Errorf("生成 Worker 监控配置失败: %w", err)
			}
		}
		if spec.TargetPath == filepath.Join("monitoring", "alerting", "alertmanager.yml") && mode != "business" && opts.ServerName != "" {
			content, err = renderAlertServerName(content, opts.ServerName)
			if err != nil {
				return fmt.Errorf("生成 Alertmanager 配置失败: %w", err)
			}
		}
		target := filepath.Join(deployRoot, spec.TargetPath)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("创建目标目录 %s 失败: %w", filepath.Dir(target), err)
		}
		// init.sh 需要可执行权限
		perm := os.FileMode(0o644)
		if strings.HasSuffix(spec.TargetPath, ".sh") {
			perm = 0o755
		}
		if err := os.WriteFile(target, content, perm); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", target, err)
		}
		if log != nil {
			log(fmt.Sprintf("落地模板: %s", spec.TargetPath))
		}
	}
	if mode != "business" {
		if err := ensureAlertWebhookFile(deployRoot, opts.FeishuWebhook, log); err != nil {
			return err
		}
	}
	return nil
}

// ensureAlertWebhookFile 优先写入中心配置中的 Webhook；未传值时保留兼容的占位文件行为。
func ensureAlertWebhookFile(deployRoot, webhook string, log func(string)) error {
	target := filepath.Join(deployRoot, "monitoring", "alerting", "feishu-webhook-url")
	if webhook != "" {
		if err := os.WriteFile(target, []byte(strings.TrimSpace(webhook)+"\n"), 0o600); err != nil {
			return fmt.Errorf("写入飞书 Webhook 文件失败: %w", err)
		}
		if log != nil {
			log("已写入中心配置中的飞书机器人 Webhook")
		}
		return nil
	}
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查飞书 Webhook 文件失败: %w", err)
	}
	source := filepath.Join(deployRoot, "monitoring", "alerting", "feishu-webhook-url.example")
	content, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("读取飞书 Webhook 示例失败: %w", err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		return fmt.Errorf("创建飞书 Webhook 占位文件失败: %w", err)
	}
	if log != nil {
		log("已创建飞书 Webhook 占位文件；启用告警前请替换 monitoring/alerting/feishu-webhook-url")
	}
	return nil
}

func renderAlertServerName(content []byte, serverName string) ([]byte, error) {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		if strings.HasPrefix(line, "    server_name:") {
			lines[index] = "    server_name: " + strconv.Quote(strings.TrimSpace(serverName))
			return []byte(strings.Join(lines, "\n")), nil
		}
	}
	return nil, fmt.Errorf("未找到 server_name 标签")
}

func renderWorkerEnvironment(content []byte, workerEnv map[string]string) []byte {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if workerEnv == nil {
		return []byte(text)
	}
	start := strings.Index(text, "x-worker-environment: &worker-environment\n")
	end := strings.Index(text, "\nx-worker-common: &worker-common\n")
	if start < 0 || end < start {
		return []byte(text)
	}
	keys := make([]string, 0, len(workerEnv))
	for key := range workerEnv {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var block strings.Builder
	block.WriteString("x-worker-environment: &worker-environment\n")
	for _, key := range keys {
		fmt.Fprintf(&block, "  %s: %s\n", key, strconv.Quote(workerEnv[key]))
	}
	return []byte(text[:start] + block.String() + text[end:])
}

func cleanupStaleManagedTemplates(deployRoot string, specs []FileSpec, log func(string)) error {
	current := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		current[filepath.Clean(spec.TargetPath)] = struct{}{}
	}
	candidates := []string{
		"docker-compose.denglin.yml",
		"docker-compose.nvidia.yml",
		"docker-compose.tensorrt.yml",
		"docker-compose.business.yml",
		filepath.Join("nginx", "nginx.full.conf"),
		filepath.Join("nginx", "nginx.conf"),
		filepath.Join("nginx", "nginx.denglin.conf"),
		filepath.Join("nginx", "nginx.tensorrt.conf"),
		filepath.Join("nginx", "nginx.business.conf"),
		filepath.Join("monitoring", "prometheus", "prometheus.yml"),
		filepath.Join("monitoring", "prometheus", "prometheus.nvidia.yml"),
		filepath.Join("monitoring", "prometheus", "prometheus.denglin.yml"),
		filepath.Join("monitoring", "grafana", "dashboards", "center-cv-overview.json"),
		filepath.Join("monitoring", "filebeat", "filebeat.yml"),
		filepath.Join("monitoring", "filebeat", "filebeat.host.yml"),
	}
	for _, rel := range candidates {
		if _, keep := current[filepath.Clean(rel)]; keep {
			continue
		}
		target := filepath.Join(deployRoot, rel)
		if err := os.Remove(target); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("移除过期模板 %s 失败: %w", target, err)
		}
		if log != nil {
			log(fmt.Sprintf("移除过期模板: %s", rel))
		}
	}
	return nil
}

func renderHostMounts(content []byte, mode, modelsRoot, logsRoot, mediaResourcesRoot string) []byte {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if modelsRoot != "" {
		mount := strconv.Quote(filepath.ToSlash(modelsRoot) + ":/data/models:ro")
		text = strings.ReplaceAll(text, "/data/models:/data/models:ro", mount)
	}
	if logsRoot != "" {
		root := filepath.ToSlash(logsRoot)
		// 一次扫描同时替换只读和读写挂载，避免新生成的主机路径再次被匹配。
		text = strings.NewReplacer(
			"/data/logs:/data/logs:ro", strconv.Quote(root+":/data/logs:ro"),
			"/data/logs:/data/logs", strconv.Quote(root+":/data/logs"),
		).Replace(text)
	}
	if mediaResourcesRoot != "" {
		mount := strconv.Quote(filepath.ToSlash(mediaResourcesRoot) + ":/data/media-resources")
		text = strings.ReplaceAll(text, "/data/media-resources:/data/media-resources", mount)
	}
	return []byte(text)
}

// renderDynamicWorkers 用物理显卡数生成一对一 Worker，并为每个 Worker 写入均分后的 CPU 集合。
func renderDynamicWorkers(content []byte, mode string, workers int, cpuSets []string) ([]byte, error) {
	if workers < 1 {
		workers = 1
	}
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	start := strings.Index(text, "  center-cv-service-0:")
	if start < 0 {
		return nil, fmt.Errorf("未找到 Worker 起始标记")
	}
	endRelative := strings.Index(text[start:], "  center-cv-ingress:")
	if endRelative < 0 {
		return nil, fmt.Errorf("未找到 Worker 结束标记")
	}
	var generated strings.Builder
	for i := 0; i < workers; i++ {
		cpuSet := ""
		if i < len(cpuSets) {
			cpuSet = cpuSets[i]
		}
		if mode == "denglin" {
			writeDenglinWorker(&generated, i, cpuSet)
		} else {
			writeNvidiaWorker(&generated, i, cpuSet)
		}
	}
	end := start + endRelative
	return []byte(text[:start] + generated.String() + text[end:]), nil
}

func writeDenglinWorker(out *strings.Builder, index int, cpuSet string) {
	logicalStart := index * 4
	devices := fmt.Sprintf("%d,%d,%d,%d", logicalStart, logicalStart+1, logicalStart+2, logicalStart+3)
	fmt.Fprintf(out, "  center-cv-service-%d:\n", index)
	out.WriteString("    <<: *worker-common\n")
	fmt.Fprintf(out, "    container_name: center-cv-service-%d\n", index)
	if cpuSet != "" {
		fmt.Fprintf(out, "    cpuset: \"%s\"\n", cpuSet)
	}
	out.WriteString("    environment:\n      <<: *worker-environment\n")
	fmt.Fprintf(out, "      APPNAME: center-cv-service-%d\n", index)
	fmt.Fprintf(out, "      CV_LISTEN_PORT: %d\n", 8181+index)
	fmt.Fprintf(out, "      DL_DEVICES: \"%s\"\n\n", devices)
}

func writeNvidiaWorker(out *strings.Builder, index int, cpuSet string) {
	fmt.Fprintf(out, "  center-cv-service-%d:\n", index)
	out.WriteString("    <<: *worker-common\n")
	fmt.Fprintf(out, "    container_name: center-cv-service-%d\n", index)
	if cpuSet != "" {
		fmt.Fprintf(out, "    cpuset: \"%s\"\n", cpuSet)
	}
	out.WriteString("    environment:\n      <<: *worker-environment\n")
	fmt.Fprintf(out, "      APPNAME: center-cv-service-%d\n", index)
	fmt.Fprintf(out, "      CV_LISTEN_PORT: %d\n", 8181+index)
	fmt.Fprintf(out, "      NVIDIA_VISIBLE_DEVICES: \"%d\"\n", index)
	out.WriteString("\n")
}

func renderDynamicPrometheus(content []byte, mode string, workers int) ([]byte, error) {
	if workers < 1 {
		workers = 1
	}
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	jobStart := strings.Index(text, "  - job_name: center-cv-service\n")
	if jobStart < 0 {
		return nil, fmt.Errorf("未找到 center-cv-service 监控任务")
	}
	staticStart := strings.Index(text[jobStart:], "    static_configs:\n")
	metricEnd := strings.Index(text[jobStart:], "    metric_relabel_configs:\n")
	if staticStart < 0 || metricEnd < 0 || staticStart >= metricEnd {
		return nil, fmt.Errorf("center-cv-service 监控区间无效")
	}
	staticStart += jobStart + len("    static_configs:\n")
	metricEnd += jobStart
	var metricTargets strings.Builder
	for i := 0; i < workers; i++ {
		fmt.Fprintf(&metricTargets, "      - targets: [\"127.0.0.1:%d\"]\n", 8181+i)
		if mode == "denglin" {
			fmt.Fprintf(&metricTargets, "        labels: {worker: \"center-cv-service-%d\", physical_gpu: \"%d\", logical_gpus: \"%d-%d\"}\n", i, i, i*4, i*4+3)
		} else {
			fmt.Fprintf(&metricTargets, "        labels: {worker: \"center-cv-service-%d\", physical_gpu: \"%d\", logical_gpus: \"%d\"}\n", i, i, i)
		}
	}
	text = text[:staticStart] + metricTargets.String() + text[metricEnd:]

	healthStart := strings.Index(text, "  - job_name: service-http-health\n")
	if healthStart < 0 {
		return nil, fmt.Errorf("未找到 Worker 健康检查任务")
	}
	healthStatic := strings.Index(text[healthStart:], "    static_configs:\n")
	ingressTarget := strings.Index(text[healthStart:], "      - targets: [\"http://127.0.0.1:8090/readyz\"]")
	if healthStatic < 0 || ingressTarget < 0 || healthStatic >= ingressTarget {
		return nil, fmt.Errorf("Worker 健康检查区间无效")
	}
	healthStatic += healthStart + len("    static_configs:\n")
	ingressTarget += healthStart
	var healthTargets strings.Builder
	for i := 0; i < workers; i++ {
		fmt.Fprintf(&healthTargets, "      - targets: [\"http://127.0.0.1:%d/api/healthz\"]\n", 8181+i)
		fmt.Fprintf(&healthTargets, "        labels: {worker: \"center-cv-service-%d\", physical_gpu: \"%d\"}\n", i, i)
	}
	text = text[:healthStatic] + healthTargets.String() + text[ingressTarget:]

	readinessStart := strings.Index(text, "  - job_name: service-readiness\n")
	if readinessStart < 0 {
		return nil, fmt.Errorf("未找到 Worker 就绪检查任务")
	}
	readinessStatic := strings.Index(text[readinessStart:], "    static_configs:\n")
	readinessIngress := strings.Index(text[readinessStart:], "      - targets: [\"http://127.0.0.1:8090/readyz\"]")
	if readinessStatic < 0 || readinessIngress < 0 || readinessStatic >= readinessIngress {
		return nil, fmt.Errorf("Worker 就绪检查区间无效")
	}
	readinessStatic += readinessStart + len("    static_configs:\n")
	readinessIngress += readinessStart
	var readinessTargets strings.Builder
	for i := 0; i < workers; i++ {
		fmt.Fprintf(&readinessTargets, "      - targets: [\"http://127.0.0.1:%d/api/readyz\"]\n", 8181+i)
		fmt.Fprintf(&readinessTargets, "        labels: {name: \"center-cv-service-%d\"}\n", i)
	}
	return []byte(text[:readinessStatic] + readinessTargets.String() + text[readinessIngress:]), nil
}

// ComposeFile 返回指定模式落地后的 compose 文件完整路径。
func ComposeFile(mode, deployRoot string) string {
	return filepath.Join(deployRoot, composeForMode(mode))
}
