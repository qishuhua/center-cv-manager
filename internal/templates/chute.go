package templates

import (
	"fmt"
	"strings"
)

// 错分检测（WMS）共享边界：chute-verification-service 只做两路落格事件审核，
// 摄像机配置轮询收口在 center-cv-ingress。两侧取值统一来自 business 模块
// module.env 中的 CV_WMS_URL / CV_MIS_AUTH / CV_ORGAN_CODE，路径由各服务自拼。

// MisSortOptions 描述中心的错分检测配置，用于维护 business 模块 module.env。
type MisSortOptions struct {
	Enabled   bool
	WMSURL    string
	OrganCode string
	MisAuth   string
}

// ChuteServiceName 是错分审核服务的软件名/服务名（软件仓库登记名一致）。
const ChuteServiceName = "chute-verification-service"

const (
	chuteServiceHeader = "  " + ChuteServiceName + ":"
	chuteInsertAnchor  = "  video-transcode-service:"
	ingressServiceKey  = "center-cv-ingress"
	// module.env 中由中心 WMS 设置托管的变量，启用时按此顺序写入。
	misSortEnvMarker = "# 错分检测（WMS）配置：由中心 WMS 设置维护，修改请回中心编辑"
)

// ChuteServiceBlock 返回错分审核服务的标准 Compose 块（含结尾空行）。
// logsRoot 为空时使用 /data/logs 占位，渲染路径随后由 renderHostMounts
// 重写为实际日志目录；事后应用路径传入实际 LogsRoot。服务始终部署，
// WMS 取值用插值默认兜底（与两份 business 模板保持一致）。
func ChuteServiceBlock(logsRoot string) string {
	if logsRoot == "" {
		logsRoot = "/data/logs"
	}
	return strings.Join([]string{
		"  chute-verification-service:",
		"    labels:",
		"      com.center-manager.image.repository: chute-verification-service",
		"    image: chute-verification-service:latest",
		"    pull_policy: never",
		"    container_name: chute-verification-service",
		"    restart: unless-stopped",
		"    network_mode: host",
		"    environment:",
		"      # 错分审核：只做两路落格事件匹配与结果发送，始终部署；module.env 提供",
		"      # 取值（开启错分检测时），未配置时使用这里的默认值。",
		"      CV_LISTEN_HOST: 127.0.0.1",
		"      CV_ORGAN_CODE: ${CV_ORGAN_CODE:-637101}",
		"      CV_MIS_AUTH: ${CV_MIS_AUTH:-20260606123a}",
		"      CV_WMS_URL: ${CV_WMS_URL:-http://10.21.61.48:31995}",
		"    volumes:",
		"      - " + logsRoot + ":/data/logs",
		"    healthcheck:",
		`      test: ["CMD-SHELL", "timeout 2 bash -c '</dev/tcp/127.0.0.1/8091' || exit 1"]`,
		"      interval: 10s",
		"      timeout: 3s",
		"      retries: 12",
		"      start_period: 10s",
		"",
	}, "\n")
}

// locateServiceBlock 返回服务块 [start,end) 的行区间：从服务头行起，到
// 下一个缩进小于 4 的非空行（兄弟服务键或顶级键）为止。
func locateServiceBlock(lines []string, header string) (start, end int, found bool) {
	for i, line := range lines {
		if line != header {
			continue
		}
		j := i + 1
		for ; j < len(lines); j++ {
			if lines[j] != "" && !strings.HasPrefix(lines[j], "    ") {
				break
			}
		}
		return i, j, true
	}
	return 0, 0, false
}

// ApplyChuteService 幂等保证 business Compose 中存在错分审核服务块（服务始终
// 部署，是否开启错分检测只影响 ingress 轮询）：不存在才以
// video-transcode-service 为锚插入标准块；已存在（含手工添加、或发布流程已
// 改写 image/labels 的块）一律不动，永不剔除。
func ApplyChuteService(compose string, logsRoot string) (string, error) {
	lines := strings.Split(compose, "\n")
	_, _, found := locateServiceBlock(lines, chuteServiceHeader)
	if found {
		return compose, nil
	}
	anchor := -1
	for i, line := range lines {
		if line == chuteInsertAnchor {
			anchor = i
			break
		}
	}
	if anchor < 0 {
		return "", fmt.Errorf("Compose 中未找到插入锚点 %s，无法添加错分审核服务", strings.TrimSpace(chuteInsertAnchor))
	}
	block := strings.Split(ChuteServiceBlock(logsRoot), "\n")
	out := make([]string, 0, len(lines)+len(block))
	out = append(out, lines[:anchor]...)
	out = append(out, block...)
	out = append(out, lines[anchor:]...)
	return strings.Join(out, "\n"), nil
}

var ingressPollEnvLines = []string{
	"      CV_CAMERA_POLL_ENABLED: ${CV_CAMERA_POLL_ENABLED:-0}",
	"      CV_WMS_URL: ${CV_WMS_URL:-}",
	"      CV_MIS_AUTH: ${CV_MIS_AUTH:-}",
	"      CV_ORGAN_CODE: ${CV_ORGAN_CODE:-}",
	"      CV_CAMERA_POLL_INTERVAL_SECONDS: ${CV_CAMERA_POLL_INTERVAL_SECONDS:-300}",
}

// EnsureIngressPollEnv 在 center-cv-ingress 的 environment 段内幂等补齐
// 错分检测轮询插值行（只增不删）。渲染模板已内置这些行，本函数服务于
// 模板更新前已初始化中心的存量 Compose。
func EnsureIngressPollEnv(compose string) (string, error) {
	lines := strings.Split(compose, "\n")
	start, end, found := locateServiceBlock(lines, "  "+ingressServiceKey+":")
	if !found {
		return "", fmt.Errorf("Compose 中未找到 %s 服务，无法接入错分检测轮询配置", ingressServiceKey)
	}
	envStart := -1
	for i := start + 1; i < end; i++ {
		if lines[i] == "    environment:" {
			envStart = i
			break
		}
	}
	if envStart < 0 {
		return "", fmt.Errorf("%s 服务缺少 environment 段，无法接入错分检测轮询配置", ingressServiceKey)
	}
	envEnd := end
	for i := envStart + 1; i < end; i++ {
		if lines[i] != "" && !strings.HasPrefix(lines[i], "      ") {
			envEnd = i
			break
		}
	}
	present := map[string]bool{}
	for i := envStart + 1; i < envEnd; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if idx := strings.Index(trimmed, ":"); idx > 0 {
			present[trimmed[:idx]] = true
		}
	}
	missing := make([]string, 0, len(ingressPollEnvLines))
	for _, want := range ingressPollEnvLines {
		trimmed := strings.TrimSpace(want)
		key := trimmed[:strings.Index(trimmed, ":")]
		if !present[key] {
			missing = append(missing, want)
		}
	}
	if len(missing) == 0 {
		return compose, nil
	}
	out := make([]string, 0, len(lines)+len(missing))
	out = append(out, lines[:envEnd]...)
	out = append(out, missing...)
	out = append(out, lines[envEnd:]...)
	return strings.Join(out, "\n"), nil
}

func envKeyOf(line string) string {
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return ""
	}
	idx := strings.Index(line, "=")
	if idx <= 0 {
		return ""
	}
	return line[:idx]
}

// ApplyMisSortModuleEnv 幂等维护 module.env 中由中心 WMS 设置托管的变量：
// 启用时写入四个变量，关闭时移除；用户自行添加的其它行与注释保持不动。
func ApplyMisSortModuleEnv(env string, opts MisSortOptions) string {
	managed := map[string]bool{
		"CV_CAMERA_POLL_ENABLED": true,
		"CV_WMS_URL":             true,
		"CV_ORGAN_CODE":          true,
		"CV_MIS_AUTH":            true,
	}
	lines := strings.Split(env, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if key := envKeyOf(line); key != "" && managed[key] {
			continue
		}
		if strings.TrimSpace(line) == misSortEnvMarker {
			continue
		}
		kept = append(kept, line)
	}
	result := strings.TrimRight(strings.Join(kept, "\n"), "\n ")
	if opts.Enabled {
		block := misSortEnvMarker + "\n" +
			"CV_CAMERA_POLL_ENABLED=1\n" +
			"CV_WMS_URL=" + opts.WMSURL + "\n" +
			"CV_ORGAN_CODE=" + opts.OrganCode + "\n" +
			"CV_MIS_AUTH=" + opts.MisAuth
		if result == "" {
			result = block
		} else {
			result = result + "\n\n" + block
		}
	}
	if result == "" {
		result = "# Module-specific Compose variables"
	}
	return result + "\n"
}
