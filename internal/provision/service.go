package provision

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"center-cv-manager/internal/alerting"
	"center-cv-manager/internal/build"
	"center-cv-manager/internal/center"
	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/centerpath"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/sshx"
	"center-cv-manager/internal/templates"
	containertypes "github.com/docker/docker/api/types/container"
	"golang.org/x/crypto/ssh"
)

type Reporter interface {
	Log(string)
	Logf(string, ...any)
	Progress(float64)
	Message(string)
}

type Service struct {
	centers *center.Service
	alerts  *alerting.Service
}

func New(centers *center.Service, alerts *alerting.Service) *Service {
	return &Service{centers: centers, alerts: alerts}
}

type ComposeDocument struct {
	Revision string `json:"revision"`
	CenterID string `json:"center_id"`
	Path     string `json:"path"`
	Content  string `json:"content"`
}

type HardwareInfo struct {
	Accelerator string     `json:"accelerator"`
	CPU         CPUInfo    `json:"cpu"`
	GPUs        []GPUInfo  `json:"gpus"`
	Memory      MemoryInfo `json:"memory"`
	Disks       []DiskInfo `json:"disks"`
	DetectedAt  time.Time  `json:"detected_at"`
}

type CPUInfo struct {
	Model         string `json:"model"`
	Sockets       int    `json:"sockets"`
	PhysicalCores int    `json:"physical_cores"`
	LogicalCores  int    `json:"logical_cores"`
}

type cpuThread struct {
	ID     int
	Node   int
	Socket int
	Core   int
}

type GPUInfo struct {
	Model       string `json:"model"`
	Count       int    `json:"count"`
	MemoryBytes int64  `json:"memory_bytes,omitempty"`
}

type MemoryInfo struct {
	TotalBytes int64 `json:"total_bytes"`
}

type DiskInfo struct {
	Device         string `json:"device"`
	MountPoint     string `json:"mount_point"`
	TotalBytes     int64  `json:"total_bytes"`
	UsedBytes      int64  `json:"used_bytes"`
	AvailableBytes int64  `json:"available_bytes"`
	Usage          string `json:"usage"`
}

type discardReporter struct{}

func (discardReporter) Log(string)          {}
func (discardReporter) Logf(string, ...any) {}
func (discardReporter) Progress(float64)    {}
func (discardReporter) Message(string)      {}

func (s *Service) Initialize(ctx context.Context, centerID string, r Reporter) error {
	release, cancelErr := centerlock.AcquireContext(ctx, centerID)
	if cancelErr != nil {
		return cancelErr
	}
	defer release()
	r.Message("正在检测中心硬件")
	r.Progress(5)
	remote, stored, err := s.connect(centerID)
	if err != nil {
		return err
	}
	defer remote.Close()
	composeFile := stored.ComposeFile
	if composeFile == "" {
		composeFile = centerpath.DefaultComposeFile
	}
	accelerator, cpuCount, gpuCount, err := probeHardware(ctx, remote, stored, composeFile, r)
	if err != nil {
		return err
	}
	r.Progress(20)
	resolved, resolveErr := writableComposeFile(ctx, remote.SSH, composeFile)
	if resolveErr != nil {
		return resolveErr
	}
	if resolved != composeFile {
		r.Logf("部署路径已解析为 SSH 用户目录：%s", resolved)
		composeFile = resolved
	}

	preserveCompose := false
	existingCompose := ""
	state, stateErr := runSSH(ctx, remote.SSH, "if [ -e "+shellQuote(composeFile)+" ]; then printf exists; else printf missing; fi", "")
	if stateErr != nil {
		return fmt.Errorf("检查中心 Compose 是否存在失败: %w", stateErr)
	}
	if strings.TrimSpace(state) == "exists" {
		existing, readErr := readRemoteFile(ctx, remote.SSH, composeFile)
		if readErr != nil {
			return fmt.Errorf("发现已有 Compose 但无法读取，为避免覆盖已停止初始化: %w", readErr)
		}
		if strings.TrimSpace(existing) == "" {
			return fmt.Errorf("发现已有但内容为空的 Compose，为避免覆盖已停止初始化；请检查中心上的 Compose 文件")
		}
		preserveCompose = true
		existingCompose = existing
		r.Logf("发现已有 Compose，将保留所有现有部署配置：%s", composeFile)
	}
	if preserveCompose && stored.Deployment != nil && stored.Deployment.Status == "preparing" && allPending(stored.Deployment.Modules) {
		if err = s.centers.UpdateDeployment(centerID, nil); err != nil {
			return err
		}
		stored.Deployment = nil
		r.Log("发现原有单 Compose，已按兼容模式接管；可在运行详情中单独生成迁移计划")
	}
	if preserveCompose {
		if _, err = runSSH(ctx, remote.SSH, "docker compose -f "+shellQuote(composeFile)+" config --quiet", ""); err != nil {
			return err
		}
		if err = s.centers.UpdateRuntime(centerID, accelerator, cpuCount, gpuCount, composeFile, existingCompose); err != nil {
			return err
		}
		r.Progress(100)
		r.Message("已验证并保留现有配置；采用新内置模板请先重置中心")
		return nil
	}
	if accelerator == "cpu" && !preserveCompose {
		return fmt.Errorf("当前内置完整部署模板需要 NVIDIA 或登临 GPU")
	}
	r.Message("正在按硬件生成 Compose 模板")
	root, err := os.MkdirTemp("", "center-compose-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	mode := "business"
	if accelerator == "nvidia" {
		mode = "tensorrt"
	} else if accelerator == "denglin" {
		mode = "denglin"
	}
	workers := gpuCount
	if workers < 1 {
		workers = 1
	}
	cpuSets := []string(nil)
	if mode != "business" {
		topologyOutput, topologyErr := runSSH(ctx, remote.SSH, "LC_ALL=C lscpu -p=CPU,NODE,SOCKET,CORE,ONLINE 2>/dev/null || LC_ALL=C lscpu -p=CPU,NODE,SOCKET,CORE 2>/dev/null", "")
		topology := parseCPUTopology(topologyOutput)
		if topologyErr == nil && len(topology) > 0 {
			cpuSets = distributeCPUTopology(topology, workers)
			r.Logf("CPU 绑定按实际拓扑生成：%d 个在线逻辑 CPU，%d 个 NUMA/Socket 分组", len(topology), cpuTopologyNodeCount(topology))
			if len(topology) != cpuCount {
				r.Logf("lscpu 拓扑数量 %d 与在线 CPU 计数 %d 不一致，cpuset 仅使用 lscpu 返回的实际 CPU ID", len(topology), cpuCount)
			}
		} else {
			r.Log("未读取到可靠的 CPU ID/NUMA 拓扑，将不限制 Worker cpuset，由 Linux 调度器分配 CPU")
		}
	}
	alertConfig, err := s.alerts.Config()
	if err != nil {
		return err
	}
	modelsRoot := centerpath.ModelsRoot(composeFile)
	logsRoot := centerpath.LogsRoot(composeFile)
	mediaResourcesRoot := centerpath.MediaResourcesRoot(composeFile)
	if stored.Deployment != nil && stored.Deployment.Status != "preparing" {
		modelsRoot, logsRoot, mediaResourcesRoot = stored.Deployment.ModelsRoot, stored.Deployment.LogsRoot, stored.Deployment.MediaRoot
	}
	prepareData := "umask 022; mkdir -p " + shellQuote(modelsRoot) + " " + shellQuote(logsRoot) + " " + shellQuote(mediaResourcesRoot) +
		" && test -w " + shellQuote(modelsRoot) + " && test -w " + shellQuote(logsRoot) + " && test -w " + shellQuote(mediaResourcesRoot)
	if output, prepareErr := runSSH(ctx, remote.SSH, prepareData, ""); prepareErr != nil {
		return fmt.Errorf("创建中心用户数据目录失败: %s %w", strings.TrimSpace(output), prepareErr)
	}
	r.Logf("中心工作目录：%s", centerpath.WorkspaceRoot(composeFile))
	if stored.Deployment != nil {
		return s.prepareModules(ctx, remote, stored, composeFile, accelerator, cpuCount, gpuCount, templates.RenderOptions{
			Workers: workers, CPUSets: cpuSets, ModelsRoot: modelsRoot, LogsRoot: logsRoot, MediaResourcesRoot: mediaResourcesRoot,
			ServerName: stored.Name, FeishuWebhook: alertConfig.FeishuWebhook,
			WMSURL: stored.WMSURL, MisAuth: stored.MisAuth, MisSortEnabled: stored.MisSortEnabled,
			DebugLogEnabled: stored.DebugLogEnabled, SaveImageEnabled: stored.SaveImageEnabled,
		}, r)
	}
	if err = templates.Render(mode, root, templates.RenderOptions{
		Workers: workers, CPUSets: cpuSets, ModelsRoot: modelsRoot, LogsRoot: logsRoot, MediaResourcesRoot: mediaResourcesRoot,
		WorkerEnv: map[string]string{"TZ": "Asia/Shanghai"}, ServerName: stored.Name, FeishuWebhook: alertConfig.FeishuWebhook,
	}, r.Log); err != nil {
		return err
	}
	generatedCompose := templates.ComposeFile(mode, root)
	composeContent, err := os.ReadFile(generatedCompose)
	if err != nil {
		return err
	}
	r.Progress(35)
	r.Message("正在上传中心部署模板")
	files := 0
	err = filepath.WalkDir(root, func(localPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(root, localPath)
		if relErr != nil {
			return relErr
		}
		remotePath := path.Join(path.Dir(composeFile), filepath.ToSlash(relative))
		if localPath == generatedCompose {
			remotePath = composeFile
		}
		content, readErr := os.ReadFile(localPath)
		if readErr != nil {
			return readErr
		}
		mode := "0644"
		if strings.HasSuffix(remotePath, ".sh") {
			mode = "0755"
		}
		if uploadErr := uploadRemoteFile(ctx, remote.SSH, remotePath, string(content), mode); uploadErr != nil {
			return uploadErr
		}
		files++
		return nil
	})
	if err != nil {
		return fmt.Errorf("上传部署模板失败: %w", err)
	}
	r.Logf("已上传 %d 个模板文件", files)
	r.Progress(85)
	if output, validateErr := runSSH(ctx, remote.SSH, "docker compose -f "+shellQuote(composeFile)+" config >/dev/null", ""); validateErr != nil {
		return fmt.Errorf("生成的 Compose 校验失败: %s %w", strings.TrimSpace(output), validateErr)
	}
	if err = s.centers.UpdateRuntime(centerID, accelerator, cpuCount, gpuCount, composeFile, string(composeContent)); err != nil {
		return err
	}
	r.Progress(100)
	r.Message("中心硬件检测与 Compose 初始化完成")
	return nil
}

// DetectHardware performs an on-demand probe and returns the result directly.
// It deliberately bypasses the task manager because this is a read-only detail
// query initiated from the center page, not an operational job.
func (s *Service) DetectHardware(ctx context.Context, centerID string) (HardwareInfo, error) {
	release, cancelErr := centerlock.AcquireContext(ctx, centerID)
	if cancelErr != nil {
		return HardwareInfo{}, cancelErr
	}
	defer release()
	remote, stored, err := s.connect(centerID)
	if err != nil {
		return HardwareInfo{}, err
	}
	defer remote.Close()
	composeFile := stored.ComposeFile
	if composeFile == "" {
		composeFile = centerpath.DefaultComposeFile
	}
	accelerator, logicalCores, gpuCount, err := probeHardware(ctx, remote, stored, composeFile, discardReporter{})
	if err != nil {
		return HardwareInfo{}, err
	}
	if err = s.centers.UpdateHardware(centerID, accelerator, logicalCores, gpuCount); err != nil {
		return HardwareInfo{}, err
	}

	model, _ := runSSH(ctx, remote.SSH, "model=$(LC_ALL=C lscpu 2>/dev/null | sed -n 's/^Model name:[[:space:]]*//p' | head -n 1); if [ -z \"$model\" ]; then model=$(sed -n 's/^model name[[:space:]]*:[[:space:]]*//p' /proc/cpuinfo 2>/dev/null | head -n 1); fi; printf '%s' \"$model\"", "")
	sockets := remoteCount(ctx, remote.SSH, "LC_ALL=C lscpu -p=Socket 2>/dev/null | grep -v '^#' | sort -u | wc -l")
	physicalCores := remoteCount(ctx, remote.SSH, "LC_ALL=C lscpu -p=Core,Socket 2>/dev/null | grep -v '^#' | sort -u | wc -l")
	if sockets < 1 {
		sockets = 1
	}
	if physicalCores < 1 {
		physicalCores = logicalCores
	}

	info := HardwareInfo{
		Accelerator: accelerator,
		CPU: CPUInfo{
			Model:         strings.TrimSpace(model),
			Sockets:       sockets,
			PhysicalCores: physicalCores,
			LogicalCores:  logicalCores,
		},
		GPUs:       detectGPUDetails(ctx, remote.SSH, accelerator, gpuCount),
		Memory:     MemoryInfo{TotalBytes: remoteInt64(ctx, remote.SSH, "awk '/^MemTotal:/{printf \"%.0f\", $2 * 1024; exit}' /proc/meminfo")},
		Disks:      detectDisks(ctx, remote.SSH),
		DetectedAt: time.Now(),
	}
	if info.CPU.Model == "" {
		info.CPU.Model = "未读取到 CPU 型号"
	}
	return info, nil
}

func detectGPUDetails(ctx context.Context, client *ssh.Client, accelerator string, expectedCount int) []GPUInfo {
	if accelerator == "cpu" || expectedCount < 1 {
		return []GPUInfo{}
	}
	if accelerator == "nvidia" {
		output, err := runSSH(ctx, client, "nvidia-smi --query-gpu=name,memory.total --format=csv,noheader,nounits", "")
		if err == nil {
			if result := parseNvidiaGPUInfo(output); gpuInfoCount(result) == expectedCount {
				return result
			}
		}
	}
	model := build.AcceleratorName(accelerator) + " GPU"
	if output, err := runSSH(ctx, client, "lspci 2>/dev/null | grep -Ei 'VGA|3D|Display|Denglin|Goldwasser|NVIDIA' | sed -E 's/^[^ ]+[[:space:]]+//' | sort -u", ""); err == nil {
		models := make([]string, 0)
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				models = append(models, line)
			}
		}
		if len(models) > 0 {
			model = strings.Join(models, " / ")
		}
	}
	return []GPUInfo{{Model: model, Count: expectedCount}}
}

func parseNvidiaGPUInfo(output string) []GPUInfo {
	result := make([]GPUInfo, 0)
	indexes := map[string]int{}
	for _, raw := range strings.Split(output, "\n") {
		parts := strings.Split(raw, ",")
		if len(parts) < 2 {
			continue
		}
		model := strings.TrimSpace(strings.Join(parts[:len(parts)-1], ","))
		memoryMiB, err := strconv.ParseInt(strings.TrimSpace(parts[len(parts)-1]), 10, 64)
		if model == "" || err != nil || memoryMiB < 1 {
			continue
		}
		key := model + "\x00" + strconv.FormatInt(memoryMiB, 10)
		if index, found := indexes[key]; found {
			result[index].Count++
			continue
		}
		indexes[key] = len(result)
		result = append(result, GPUInfo{Model: model, Count: 1, MemoryBytes: memoryMiB * 1024 * 1024})
	}
	return result
}

func gpuInfoCount(values []GPUInfo) int {
	total := 0
	for _, value := range values {
		total += value.Count
	}
	return total
}

func detectDisks(ctx context.Context, client *ssh.Client) []DiskInfo {
	output, err := runSSH(ctx, client, "LC_ALL=C df -B1 -P -x tmpfs -x devtmpfs 2>/dev/null | tail -n +2", "")
	if err != nil {
		return []DiskInfo{}
	}
	result := make([]DiskInfo, 0)
	for _, raw := range strings.Split(output, "\n") {
		fields := strings.Fields(raw)
		if len(fields) < 6 {
			continue
		}
		total, totalErr := strconv.ParseInt(fields[1], 10, 64)
		used, usedErr := strconv.ParseInt(fields[2], 10, 64)
		available, availableErr := strconv.ParseInt(fields[3], 10, 64)
		if totalErr != nil || usedErr != nil || availableErr != nil {
			continue
		}
		result = append(result, DiskInfo{Device: fields[0], TotalBytes: total, UsedBytes: used, AvailableBytes: available, Usage: fields[4], MountPoint: strings.Join(fields[5:], " ")})
	}
	return result
}

func remoteInt64(ctx context.Context, client *ssh.Client, command string) int64 {
	output, err := runSSH(ctx, client, command, "")
	if err != nil {
		return 0
	}
	value, _ := strconv.ParseInt(strings.TrimSpace(output), 10, 64)
	return value
}

func probeHardware(ctx context.Context, remote *sshx.DockerClient, stored domain.Center, composeFile string, r Reporter) (string, int, int, error) {
	accelerator, err := build.ResolveAccelerator(ctx, remote, stored)
	if err != nil {
		return "", 0, 0, err
	}
	cpuCount := remoteCount(ctx, remote.SSH, "getconf _NPROCESSORS_ONLN 2>/dev/null || nproc 2>/dev/null || grep -c '^processor' /proc/cpuinfo")
	if cpuCount < 1 && stored.CPUCount > 0 {
		cpuCount = stored.CPUCount
		r.Logf("实时 CPU 探测失败，沿用上次检测值：%d", cpuCount)
	}
	if cpuCount < 1 {
		return "", 0, 0, fmt.Errorf("无法读取中心 CPU 数量")
	}
	gpuCount, gpuMethod, gpuDiagnostics := detectGPUCount(ctx, remote, accelerator, composeFile, stored.GPUCount)
	if gpuCount > 0 {
		r.Logf("物理 GPU 探测：%d（%s）", gpuCount, gpuMethod)
	}
	if accelerator != "cpu" && gpuCount < 1 {
		for _, diagnostic := range gpuDiagnostics {
			r.Log("GPU 探测未命中：" + diagnostic)
		}
		return "", 0, 0, fmt.Errorf("已识别 %s 环境，但没有读取到物理 GPU；请检查显卡管理工具和驱动状态", build.AcceleratorName(accelerator))
	}
	r.Logf("硬件检测完成：%s，CPU %d，物理 GPU %d", build.AcceleratorName(accelerator), cpuCount, gpuCount)
	return accelerator, cpuCount, gpuCount, nil
}

func writableComposeFile(ctx context.Context, client *ssh.Client, requested string) (string, error) {
	command := "target=" + shellQuote(requested) + "; parent=$(dirname -- \"$target\"); if [ -e \"$target\" ]; then printf '%s' \"$target\"; elif mkdir -p \"$parent\" 2>/dev/null && [ -w \"$parent\" ]; then printf '%s' \"$target\"; else fallback=\"$HOME/.local/share/center-manager/deploy/docker-compose.yml\"; mkdir -p \"${fallback%/*}\" && printf '%s' \"$fallback\"; fi"
	output, err := runSSH(ctx, client, command, "")
	if err != nil {
		return "", fmt.Errorf("中心 Compose 目录不可写，且无法创建用户目录: %s %w", strings.TrimSpace(output), err)
	}
	resolved := strings.TrimSpace(output)
	if !strings.HasPrefix(resolved, "/") || strings.ContainsAny(resolved, "\r\n\x00") {
		return "", fmt.Errorf("中心返回了无效的 Compose 路径 %q", resolved)
	}
	return resolved, nil
}

func (s *Service) ReadCompose(ctx context.Context, centerID string, files ...string) (ComposeDocument, error) {
	remote, stored, err := s.connect(centerID)
	if err != nil {
		return ComposeDocument{}, err
	}
	defer remote.Close()
	if stored.Deployment != nil {
		return ComposeDocument{}, fmt.Errorf("模块化中心请在模块列表中选择 Compose")
	}
	target, err := legacyComposeTarget(ctx, remote, stored.ComposeFile, files)
	if err != nil {
		return ComposeDocument{}, err
	}
	content, err := readRemoteFile(ctx, remote.SSH, target)
	if err != nil {
		return ComposeDocument{}, fmt.Errorf("读取中心 Compose 失败: %w", err)
	}
	return ComposeDocument{CenterID: centerID, Path: target, Content: content, Revision: modular.Hash(content)}, nil
}

func (s *Service) SaveCompose(ctx context.Context, centerID, content string, files ...string) (ComposeDocument, error) {
	release, cancelErr := centerlock.AcquireContext(ctx, centerID)
	if cancelErr != nil {
		return ComposeDocument{}, cancelErr
	}
	defer release()
	if strings.TrimSpace(content) == "" || len(content) > 2<<20 {
		return ComposeDocument{}, fmt.Errorf("Compose 内容为空或超过 2 MiB")
	}
	remote, stored, err := s.connect(centerID)
	if err != nil {
		return ComposeDocument{}, err
	}
	defer remote.Close()
	if stored.Deployment != nil {
		return ComposeDocument{}, fmt.Errorf("模块化中心请在模块列表中选择 Compose")
	}
	target, err := legacyComposeTarget(ctx, remote, stored.ComposeFile, files)
	if err != nil {
		return ComposeDocument{}, err
	}
	if len(files) > 1 && files[1] != "" {
		old, e := readRemoteFile(ctx, remote.SSH, target)
		if e != nil {
			return ComposeDocument{}, e
		}
		if modular.Hash(old) != files[1] {
			return ComposeDocument{}, fmt.Errorf("配置已变更，请重新读取后保存")
		}
	}
	if err = s.captureComposeBaseline(ctx, remote, stored, path.Base(target)); err != nil {
		return ComposeDocument{}, err
	}
	temporary := target + ".validate." + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err = uploadRemoteFile(ctx, remote.SSH, temporary, content, "0600"); err != nil {
		return ComposeDocument{}, err
	}
	defer func() { _, _ = runSSH(context.Background(), remote.SSH, "rm -f "+shellQuote(temporary), "") }()
	if output, validateErr := runSSH(ctx, remote.SSH, "docker compose -f "+shellQuote(temporary)+" config >/dev/null", ""); validateErr != nil {
		return ComposeDocument{}, fmt.Errorf("Compose 校验失败: %s %w", strings.TrimSpace(output), validateErr)
	}
	backup := target + ".bak." + strconv.FormatInt(time.Now().Unix(), 10)
	command := "if [ -f " + shellQuote(target) + " ]; then cp " + shellQuote(target) + " " + shellQuote(backup) + "; fi; chmod 0644 " + shellQuote(temporary) + " && mv " + shellQuote(temporary) + " " + shellQuote(target)
	if output, saveErr := runSSH(ctx, remote.SSH, command, ""); saveErr != nil {
		return ComposeDocument{}, fmt.Errorf("保存 Compose 失败: %s %w", strings.TrimSpace(output), saveErr)
	}
	accelerator := stored.DetectedAccelerator
	if accelerator == "" {
		accelerator = stored.Accelerator
	}
	if target == stored.ComposeFile {
		if err = s.centers.UpdateRuntime(centerID, accelerator, stored.CPUCount, stored.GPUCount, stored.ComposeFile, content); err != nil {
			return ComposeDocument{}, err
		}
	}
	return ComposeDocument{CenterID: centerID, Path: target, Content: content, Revision: modular.Hash(content)}, nil
}

func (s *Service) connect(centerID string) (*sshx.DockerClient, domain.Center, error) {
	cfg, socket, stored, err := s.centers.Connection(centerID)
	if err != nil {
		return nil, domain.Center{}, err
	}
	remote, fingerprint, err := sshx.NewDocker(cfg, socket)
	if err != nil {
		return nil, domain.Center{}, err
	}
	_ = s.centers.RememberHostKey(stored, fingerprint)
	return remote, stored, nil
}

type gpuProbe struct {
	name    string
	command string
	parser  func(string) int
}

const denglinLogicalDevicesPerPhysicalGPU = 4
const denglinSupportedPhysicalGPUs = 8

var (
	gpuLabelPattern = regexp.MustCompile(`(?i)^(?:GPU|Device|Card)\s*(?:#\s*)?(\d+)\b`)
	tableIDPattern  = regexp.MustCompile(`^\|?\s*(\d+)\s*(?:\||\s{2,})`)
	workerPattern   = regexp.MustCompile(`^center-cv-service-(\d+)$`)
)

func detectGPUCount(ctx context.Context, remote *sshx.DockerClient, accelerator, composeFile string, storedCount int) (int, string, []string) {
	if accelerator == "cpu" {
		return 0, "CPU 环境", nil
	}
	probes := []gpuProbe{
		{name: "nvidia-smi", command: "nvidia-smi", parser: parseGPUList},
		{name: "nvidia-smi 索引", command: "nvidia-smi --query-gpu=index --format=csv,noheader,nounits", parser: parseSequentialIDs},
		{name: "nvidia-smi -L", command: "nvidia-smi -L", parser: parseGPUList},
		{name: "NVIDIA 驱动目录", command: "for d in /proc/driver/nvidia/gpus/*; do [ -d \"$d\" ] && basename \"$d\"; done", parser: countNonEmptyLines},
	}
	if accelerator == "denglin" {
		dlsmi := resolveToolCommand("dlsmi")
		dlSmi := resolveToolCommand("dl-smi")
		probes = []gpuProbe{
			{name: "dlsmi 逻辑设备（每张物理卡 4 个）", command: dlsmi, parser: parseDenglinPhysicalGPUCount},
			{name: "dlsmi -L 逻辑设备（每张物理卡 4 个）", command: dlsmi + " -L", parser: parseDenglinPhysicalGPUCount},
			{name: "dl-smi 逻辑设备（每张物理卡 4 个）", command: dlSmi, parser: parseDenglinPhysicalGPUCount},
			{name: "dl-smi -L 逻辑设备（每张物理卡 4 个）", command: dlSmi + " -L", parser: parseDenglinPhysicalGPUCount},
			{name: "登临逻辑设备节点（每张物理卡 4 个）", command: "for d in /dev/dl[0-9]*; do [ -e \"$d\" ] && basename \"$d\"; done", parser: parseDenglinDeviceNodes},
		}
	}
	diagnostics := make([]string, 0, len(probes)+3)
	for _, probe := range probes {
		output, err := runSSH(ctx, remote.SSH, probe.command, "")
		if count := probe.parser(output); count > 0 {
			return count, probe.name, diagnostics
		}
		diagnostics = append(diagnostics, formatProbeDiagnostic(probe.name, output, err))
	}

	// 已部署过的中心可能没有把厂商管理工具加入非交互 SSH 的 PATH。
	// 此时已有 Compose/容器中的 worker 数仍是比猜测设备节点数量更可靠的兜底。
	if strings.TrimSpace(composeFile) != "" {
		output, err := runSSH(ctx, remote.SSH, "docker compose -f "+shellQuote(composeFile)+" config --services", "")
		if count := countWorkerServices(output); count > 0 {
			count, correction := normalizeDenglinLegacyCount(accelerator, count)
			return count, "已有 Compose 的 worker 配置" + correction, diagnostics
		}
		diagnostics = append(diagnostics, formatProbeDiagnostic("已有 Compose", output, err))
	}
	if containers, err := remote.Client.ContainerList(ctx, containertypes.ListOptions{All: true}); err == nil {
		ids := map[string]struct{}{}
		for _, item := range containers {
			for _, name := range item.Names {
				name = strings.TrimPrefix(name, "/")
				if workerPattern.MatchString(name) {
					ids[name] = struct{}{}
				}
			}
		}
		if len(ids) > 0 {
			count, correction := normalizeDenglinLegacyCount(accelerator, len(ids))
			return count, "已有 Docker worker 容器" + correction, diagnostics
		}
		diagnostics = append(diagnostics, "已有 Docker 容器：未找到 center-cv-service-N")
	} else {
		diagnostics = append(diagnostics, formatProbeDiagnostic("Docker 容器列表", "", err))
	}
	if storedCount > 0 {
		count, correction := normalizeDenglinLegacyCount(accelerator, storedCount)
		return count, "上次成功检测值" + correction, diagnostics
	}
	if accelerator == "denglin" {
		output, err := runSSH(ctx, remote.SSH, "lspci -D 2>/dev/null | grep -Ei 'Denglin|Goldwasser' | head -n 20", "")
		diagnostics = append(diagnostics, formatProbeDiagnostic("登临 PCI 设备（仅诊断，不据此猜测物理卡数）", output, err))
	}
	return 0, "", diagnostics
}

// Versions before the physical/logical split could persist 32 Denglin workers
// on the supported eight-card topology. Correct that legacy value when live
// probes are unavailable so retrying initialization does not reproduce it.
func normalizeDenglinLegacyCount(accelerator string, count int) (int, string) {
	if accelerator == "denglin" && count == denglinSupportedPhysicalGPUs*denglinLogicalDevicesPerPhysicalGPU {
		return count / denglinLogicalDevicesPerPhysicalGPU, "（旧逻辑设备计数已按 4:1 修正）"
	}
	return count, ""
}

// Denglin exposes four DL runtime device indexes for each physical GPU. dlsmi
// lists those logical indexes, so treating its 0..31 output as physical cards
// creates 32 workers and assigns non-existent device indexes 32..127.
func parseDenglinPhysicalGPUCount(output string) int {
	logicalDevices := parseGPUList(output)
	if logicalDevices < denglinLogicalDevicesPerPhysicalGPU || logicalDevices%denglinLogicalDevicesPerPhysicalGPU != 0 {
		return 0
	}
	return logicalDevices / denglinLogicalDevicesPerPhysicalGPU
}

func parseDenglinDeviceNodes(output string) int {
	ids := map[int]struct{}{}
	for _, raw := range strings.Split(output, "\n") {
		name := strings.TrimSpace(raw)
		if !strings.HasPrefix(name, "dl") {
			continue
		}
		id, err := strconv.Atoi(strings.TrimPrefix(name, "dl"))
		if err == nil && id >= 0 && id < 256 {
			ids[id] = struct{}{}
		}
	}
	logicalDevices := contiguousIDCount(ids)
	if logicalDevices < denglinLogicalDevicesPerPhysicalGPU || logicalDevices%denglinLogicalDevicesPerPhysicalGPU != 0 {
		return 0
	}
	return logicalDevices / denglinLogicalDevicesPerPhysicalGPU
}

func resolveToolCommand(name string) string {
	paths := []string{"/usr/bin/" + name, "/usr/local/bin/" + name, "/usr/local/dl/bin/" + name, "/opt/dlrt/bin/" + name, "/opt/denglin/bin/" + name}
	return "tool=$(command -v " + name + " 2>/dev/null || true); if [ -z \"$tool\" ]; then for candidate in " + strings.Join(paths, " ") + "; do if [ -x \"$candidate\" ]; then tool=\"$candidate\"; break; fi; done; fi; if [ -z \"$tool\" ]; then echo '" + name + " not found' >&2; exit 127; fi; \"$tool\""
}

func parseGPUList(output string) int {
	ids := map[int]struct{}{}
	tableIDs := map[int]struct{}{}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if match := gpuLabelPattern.FindStringSubmatch(line); len(match) == 2 {
			if id, err := strconv.Atoi(match[1]); err == nil {
				ids[id] = struct{}{}
			}
		}
		if match := tableIDPattern.FindStringSubmatch(line); len(match) == 2 {
			if id, err := strconv.Atoi(match[1]); err == nil && id >= 0 && id < 256 {
				tableIDs[id] = struct{}{}
			}
		}
	}
	if count := contiguousIDCount(ids); count > 0 {
		return count
	}
	return contiguousIDCount(tableIDs)
}

func parseSequentialIDs(output string) int {
	ids := map[int]struct{}{}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		id, err := strconv.Atoi(line)
		if err == nil && id >= 0 && id < 256 {
			ids[id] = struct{}{}
		}
	}
	return contiguousIDCount(ids)
}

func contiguousIDCount(ids map[int]struct{}) int {
	if len(ids) == 0 {
		return 0
	}
	for index := 0; index < len(ids); index++ {
		if _, ok := ids[index]; !ok {
			return 0
		}
	}
	return len(ids)
}

func countNonEmptyLines(output string) int {
	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func countWorkerServices(output string) int {
	ids := map[string]struct{}{}
	for _, line := range strings.Split(output, "\n") {
		name := strings.TrimSpace(line)
		if workerPattern.MatchString(name) {
			ids[name] = struct{}{}
		}
	}
	return len(ids)
}

func formatProbeDiagnostic(name, output string, err error) string {
	detail := strings.Join(strings.Fields(strings.ReplaceAll(output, "\x00", "")), " ")
	if len(detail) > 500 {
		detail = detail[:500] + "…"
	}
	if detail == "" {
		detail = "无输出"
	}
	if err != nil {
		return fmt.Sprintf("%s：%s（%v）", name, detail, err)
	}
	return fmt.Sprintf("%s：%s", name, detail)
}

func remoteCount(ctx context.Context, client *ssh.Client, command string) int {
	output, err := runSSH(ctx, client, command, "")
	if err != nil {
		return 0
	}
	for _, field := range strings.Fields(output) {
		if value, parseErr := strconv.Atoi(field); parseErr == nil && value > 0 {
			return value
		}
	}
	return 0
}

func parseCPUTopology(output string) []cpuThread {
	threads := make([]cpuThread, 0)
	seen := map[int]struct{}{}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 4 {
			continue
		}
		id, idErr := strconv.Atoi(strings.TrimSpace(fields[0]))
		node, nodeErr := strconv.Atoi(strings.TrimSpace(fields[1]))
		socket, socketErr := strconv.Atoi(strings.TrimSpace(fields[2]))
		core, coreErr := strconv.Atoi(strings.TrimSpace(fields[3]))
		if idErr != nil || socketErr != nil || coreErr != nil || id < 0 {
			continue
		}
		if len(fields) >= 5 {
			online := strings.ToLower(strings.TrimSpace(fields[4]))
			if online != "" && online != "y" && online != "yes" && online != "1" {
				continue
			}
		}
		if nodeErr != nil || node < 0 {
			node = socket
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		threads = append(threads, cpuThread{ID: id, Node: node, Socket: socket, Core: core})
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].ID < threads[j].ID })
	return threads
}

func cpuTopologyNodeCount(threads []cpuThread) int {
	nodes := map[int]struct{}{}
	for _, thread := range threads {
		nodes[thread.Node] = struct{}{}
	}
	return len(nodes)
}

func distributeCPUTopology(threads []cpuThread, workers int) []string {
	if len(threads) == 0 || workers < 1 {
		return nil
	}
	type coreGroup struct {
		Socket  int
		Core    int
		Threads []int
	}
	nodes := map[int]map[string]*coreGroup{}
	for _, thread := range threads {
		cores := nodes[thread.Node]
		if cores == nil {
			cores = map[string]*coreGroup{}
			nodes[thread.Node] = cores
		}
		key := strconv.Itoa(thread.Socket) + ":" + strconv.Itoa(thread.Core)
		group := cores[key]
		if group == nil {
			group = &coreGroup{Socket: thread.Socket, Core: thread.Core}
			cores[key] = group
		}
		group.Threads = append(group.Threads, thread.ID)
	}
	nodeIDs := make([]int, 0, len(nodes))
	for node := range nodes {
		nodeIDs = append(nodeIDs, node)
	}
	sort.Ints(nodeIDs)
	if workers < len(nodeIDs) {
		// With fewer workers than NUMA nodes, preserve all CPUs rather than
		// silently leaving a node unused.
		all := make([]int, 0, len(threads))
		for _, thread := range threads {
			all = append(all, thread.ID)
		}
		return distributeCPUIds(all, workers)
	}
	result := make([]string, workers)
	workerStart := 0
	baseWorkers, extraWorkers := workers/len(nodeIDs), workers%len(nodeIDs)
	for nodeIndex, node := range nodeIDs {
		nodeWorkers := baseWorkers
		if nodeIndex < extraWorkers {
			nodeWorkers++
		}
		groups := make([]*coreGroup, 0, len(nodes[node]))
		for _, group := range nodes[node] {
			sort.Ints(group.Threads)
			groups = append(groups, group)
		}
		sort.Slice(groups, func(i, j int) bool {
			if groups[i].Socket != groups[j].Socket {
				return groups[i].Socket < groups[j].Socket
			}
			return groups[i].Core < groups[j].Core
		})
		baseCores, extraCores, cursor := len(groups)/nodeWorkers, len(groups)%nodeWorkers, 0
		for localWorker := 0; localWorker < nodeWorkers; localWorker++ {
			count := baseCores
			if localWorker < extraCores {
				count++
			}
			ids := make([]int, 0)
			for _, group := range groups[cursor : cursor+count] {
				ids = append(ids, group.Threads...)
			}
			result[workerStart+localWorker] = formatCPUSet(ids)
			cursor += count
		}
		workerStart += nodeWorkers
	}
	return result
}

func distributeCPUIds(ids []int, workers int) []string {
	if len(ids) == 0 || workers < 1 {
		return nil
	}
	sort.Ints(ids)
	sets := make([]string, workers)
	base, remainder, start := len(ids)/workers, len(ids)%workers, 0
	for index := 0; index < workers; index++ {
		count := base
		if index < remainder {
			count++
		}
		if count == 0 {
			sets[index] = strconv.Itoa(ids[index%len(ids)])
			continue
		}
		sets[index] = formatCPUSet(ids[start : start+count])
		start += count
	}
	return sets
}

func formatCPUSet(ids []int) string {
	if len(ids) == 0 {
		return ""
	}
	sort.Ints(ids)
	unique := ids[:0]
	for _, id := range ids {
		if len(unique) == 0 || unique[len(unique)-1] != id {
			unique = append(unique, id)
		}
	}
	var parts []string
	for start := 0; start < len(unique); {
		end := start
		for end+1 < len(unique) && unique[end+1] == unique[end]+1 {
			end++
		}
		if start == end {
			parts = append(parts, strconv.Itoa(unique[start]))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", unique[start], unique[end]))
		}
		start = end + 1
	}
	return strings.Join(parts, ",")
}

func readRemoteFile(ctx context.Context, client *ssh.Client, remotePath string) (string, error) {
	return runSSH(ctx, client, "cat "+shellQuote(remotePath), "")
}

func uploadRemoteFile(ctx context.Context, client *ssh.Client, remotePath, content, mode string) error {
	temporary := remotePath + ".tmp." + strconv.FormatInt(time.Now().UnixNano(), 10)
	command := "mkdir -p " + shellQuote(path.Dir(remotePath)) + " && umask 077; cat > " + shellQuote(temporary) + " && chmod " + mode + " " + shellQuote(temporary) + " && mv " + shellQuote(temporary) + " " + shellQuote(remotePath)
	output, err := runSSH(ctx, client, command, content)
	if err != nil {
		return fmt.Errorf("写入 %s 失败: %s %w", remotePath, strings.TrimSpace(output), err)
	}
	return nil
}

func runSSH(ctx context.Context, client *ssh.Client, command, stdin string) (string, error) {
	return modular.Run(ctx, client, command, stdin)
}

func shellQuote(value string) string { return centerpath.ShellQuote(value) }
