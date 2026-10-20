package templates

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// 错分检测（WMS）共享边界：chute-verification-service 只做两路落格事件审核，
// 摄像机配置轮询收口在 center-cv-ingress。两侧取值统一来自 business 模块
// module.env 中的 CV_WMS_URL / CV_MIS_AUTH，路径由各服务自拼；机构编码
// 不做配置，两服务各用 Compose 插值默认值（chute 637101）。

// MisSortOptions 描述中心的错分检测配置，用于维护 business 模块 module.env。
type MisSortOptions struct {
	Enabled bool
	WMSURL  string
	MisAuth string
}

// ChuteServiceName 是错分审核服务的软件名/服务名（软件仓库登记名一致）。
const ChuteServiceName = "chute-verification-service"

const (
	chuteInsertAnchor = "video-transcode-service"
	ingressServiceKey = "center-cv-ingress"
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

// lineIndent 返回行首空格数。YAML 禁止制表符缩进，遇到按 0 处理，
// 由插入后的 YAML 终检兜底报错。
func lineIndent(line string) int {
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	return n
}

// serviceHeaderAt 返回首个「去掉缩进后恰为 <name>:」的行号（没有返回 -1），
// 任意缩进都命中——存量中心手写或重排过的 Compose 服务键不一定是模板的
// 2 空格缩进，按头行精确匹配会整体落空。
func serviceHeaderAt(lines []string, name string) int {
	want := name + ":"
	for i, line := range lines {
		if strings.TrimLeft(line, " \t") == want {
			return i
		}
	}
	return -1
}

// serviceBlockEnd 返回服务块头行之后的结束行号：首个非空且缩进不深于头行
// 的行（兄弟服务键或顶级键）；块内空行与更深缩进的行都属于该块。
func serviceBlockEnd(lines []string, header int) int {
	indent := lineIndent(lines[header])
	for j := header + 1; j < len(lines); j++ {
		if lines[j] != "" && lineIndent(lines[j]) <= indent {
			return j
		}
	}
	return len(lines)
}

// serviceStep 返回服务块字段的缩进步长：块内首个更深的非空、非注释行与
// 服务头行的缩进差。模板为 2，手写或编辑器重排的 Compose 常见 4；识别
// 不到（空块等）回落 2。
func serviceStep(lines []string, header int) int {
	hdr := lineIndent(lines[header])
	for j := header + 1; j < len(lines); j++ {
		line := lines[j]
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if lineIndent(line) <= hdr {
			return 2
		}
		return lineIndent(line) - hdr
	}
	return 2
}

// rescaleLines 把按模板 2 空格步长书写的块平移到 target 服务键缩进，并按
// step 重放层级（行内层级差 (k-2)*step/2）。空行不动。
func rescaleLines(lines []string, target, step int) []string {
	if step < 1 {
		step = 2
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		if line == "" {
			out[i] = ""
			continue
		}
		depth := max((lineIndent(line)-2)*step/2, 0)
		out[i] = strings.Repeat(" ", target+depth) + strings.TrimLeft(line, " \t")
	}
	return out
}

// reindentTo 把每个非空行的行首缩进替换为 indent（空行不动）。
func reindentTo(lines []string, indent int) []string {
	pad := strings.Repeat(" ", indent)
	out := make([]string, len(lines))
	for i, line := range lines {
		if line == "" {
			out[i] = ""
			continue
		}
		out[i] = pad + strings.TrimLeft(line, " \t")
	}
	return out
}

// ApplyChuteService 幂等保证 business Compose 中存在错分审核服务块（服务始终
// 部署，是否开启错分检测只影响 ingress 轮询）：不存在才插入标准块；已存在
// （含手工添加、或发布流程已改写 image/labels 的块）一律不动，永不剔除。
// 幂等判定优先经 YAML 看 services 段是否已含该服务（任意缩进与书写形态），
// 解析失败的文件退回头行文本判定。插入点按可靠性依次选择：
// video-transcode-service 锚点（与模板渲染位置一致）→ 经 YAML 解析定位
// services 段最后一个服务块的末尾（结构感知，对任意合法 Compose 都落在
// 真实服务边界上）→ 文本级兜底（services 段末尾）。插入块按锚点/服务键的
// 实际缩进平移——手写或编辑器重排过的 Compose 服务缩进不一定是模板的
// 2 空格，硬编码缩进插入会与服务错位导致 YAML 解析失败。插入产物在本地
// 做 YAML 终检（chute 必须位于 services 段），坏文件不会上传到中心。
func ApplyChuteService(compose string, logsRoot string) (string, error) {
	compose = strings.ReplaceAll(compose, "\r\n", "\n")
	lines := strings.Split(compose, "\n")
	if chuteInServices(compose) || serviceHeaderAt(lines, ChuteServiceName) >= 0 {
		return compose, nil
	}
	insertAt, indent, step := -1, -1, 2
	if anchor := serviceHeaderAt(lines, chuteInsertAnchor); anchor >= 0 {
		insertAt, indent, step = anchor, lineIndent(lines[anchor]), serviceStep(lines, anchor)
	} else if last, ok := lastServiceKey(compose); ok {
		if header := serviceHeaderAt(lines, last); header >= 0 {
			insertAt, indent, step = serviceBlockEnd(lines, header), lineIndent(lines[header]), serviceStep(lines, header)
		}
	}
	if insertAt < 0 {
		if end, first := servicesSectionEnd(lines); end >= 0 && first >= 0 {
			insertAt, indent, step = end, lineIndent(lines[first]), serviceStep(lines, first)
		}
	}
	if insertAt < 0 {
		return "", fmt.Errorf("Compose 中未找到 services 段，无法添加错分审核服务")
	}
	if indent < 1 {
		return "", fmt.Errorf("Compose services 段没有服务条目，无法确定插入缩进")
	}
	block := rescaleLines(strings.Split(ChuteServiceBlock(logsRoot), "\n"), indent, step)
	out := make([]string, 0, len(lines)+len(block))
	out = append(out, lines[:insertAt]...)
	out = append(out, block...)
	out = append(out, lines[insertAt:]...)
	result := strings.Join(out, "\n")
	if err := validateChuteInsertion(result); err != nil {
		return "", fmt.Errorf("添加错分审核服务失败: %w", err)
	}
	return result, nil
}

// chuteInServices 经 YAML 解析判断 services 段是否已含错分审核服务，覆盖
// 任意缩进与书写形态；解析失败返回 false，由头行判定与插入终检兜底。
func chuteInServices(compose string) bool {
	var document map[string]any
	if err := yaml.Unmarshal([]byte(compose), &document); err != nil {
		return false
	}
	services, _ := document["services"].(map[string]any)
	_, ok := services[ChuteServiceName]
	return ok
}

// lastServiceKey 经 YAML 解析（yaml.Node 保留文档顺序）取 services 段最后
// 一个服务名，供插入点定位；解析失败返回 false，由调用方走文本兜底。
func lastServiceKey(compose string) (string, bool) {
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(compose), &document); err != nil {
		return "", false
	}
	if len(document.Content) == 0 {
		return "", false
	}
	root := document.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "services" {
			continue
		}
		services := root.Content[i+1]
		if services.Kind != yaml.MappingNode || len(services.Content) < 2 {
			return "", false
		}
		return services.Content[len(services.Content)-2].Value, true
	}
	return "", false
}

// validateChuteInsertion 本地终检：插入产物必须是合法 YAML，且 chute 服务
// 位于 services 段（插入点错位会把它嵌进别的映射）。诊断片段用 %q 保留
// 每行的真实缩进，避免错误消息丢失空白后无法定位。
func validateChuteInsertion(result string) error {
	var document map[string]any
	if err := yaml.Unmarshal([]byte(result), &document); err != nil {
		return fmt.Errorf("插入后不再是合法 YAML: %w\n插入点上下文:\n%s", err, quotedChuteContext(result))
	}
	services, _ := document["services"].(map[string]any)
	if _, ok := services[ChuteServiceName]; !ok {
		return fmt.Errorf("插入后错分审核服务不在 services 段\n插入点上下文:\n%s", quotedChuteContext(result))
	}
	return nil
}

func quotedChuteContext(compose string) string {
	lines := strings.Split(compose, "\n")
	header := serviceHeaderAt(lines, ChuteServiceName)
	if header < 0 {
		return "(未找到错分审核服务头行)"
	}
	start, end := header-4, header+4
	if start < 0 {
		start = 0
	}
	if end > len(lines) {
		end = len(lines)
	}
	parts := make([]string, 0, end-start)
	for j := start; j < end; j++ {
		parts = append(parts, fmt.Sprintf("L%d: %q", j+1, lines[j]))
	}
	return strings.Join(parts, "\n")
}

// servicesSectionEnd 返回 services 段的插入点与段内首个服务头行的行号：
// 插入点在段内最后一个服务之后、下一个顶级键（networks:/volumes: 等）或
// 文件末尾之前；注释与空行不算段边界。服务键缩进与步长由首个服务头行
// 推算，插入块与段内既有服务对齐。
func servicesSectionEnd(lines []string) (int, int) {
	servicesIdx := -1
	for i, line := range lines {
		if line == "services:" {
			servicesIdx = i
			break
		}
	}
	if servicesIdx < 0 {
		return -1, -1
	}
	first := -1
	for j := servicesIdx + 1; j < len(lines); j++ {
		line := lines[j]
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if lineIndent(line) == 0 {
			return j, first
		}
		if first < 0 {
			first = j
		}
	}
	return len(lines), first
}

var ingressPollEnvLines = []string{
	"      CV_CAMERA_POLL_ENABLED: ${CV_CAMERA_POLL_ENABLED:-0}",
	"      CV_WMS_URL: ${CV_WMS_URL:-}",
	"      CV_MIS_AUTH: ${CV_MIS_AUTH:-}",
	"      CV_ORGAN_CODE: ${CV_ORGAN_CODE:-}",
	"      CV_CAMERA_POLL_INTERVAL_SECONDS: ${CV_CAMERA_POLL_INTERVAL_SECONDS:-300}",
}

const (
	chuteUpstreamMarker     = "upstream chute_backend"
	chuteLocationMarker     = "location /fall-events/"
	chuteUpstreamAnchor     = "    upstream cv_ingress {"
	chuteTaskLocationPrefix = "        location ~ ^/api/v3/"
	chuteServerFallback     = "    server {"
	chuteCatchAllAnchor     = "        location / {"
)

var chuteUpstreamBlock = []string{
	"    # 错分审核服务（chute-verification-service）只监听本机回环 8091，",
	"    # 局域网查询经网关 /fall-events/ 反代访问。",
	"    upstream chute_backend {",
	"        server 127.0.0.1:8091 max_fails=1 fail_timeout=10s;",
	"    }",
	"",
}

var chuteLocationBlock = []string{
	"        # 错分审核事件查询：局域网应用直接访问 /fall-events/**。",
	"        location /fall-events/ {",
	"            proxy_pass http://chute_backend;",
	"            proxy_set_header Host $host;",
	"            proxy_set_header X-Real-IP $remote_addr;",
	"            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;",
	"            proxy_set_header X-Forwarded-Proto $scheme;",
	"        }",
	"",
}

// EnsureFallEventsProxy 幂等保证 gateway nginx 配置反代错分审核事件接口：
// 服务本机仅回环监听（8091），局域网应用经网关 /fall-events/ 访问。
// upstream 首选插在 cv_ingress 之前（兜底：server 块之前）；location 首选插
// 在任务创建 location 之前（兜底：catch-all location / 之前）。两个块独立
// 幂等，模板演进前初始化的存量中心由错分同步自动补齐。输入统一归一为 LF
// 行尾（锚点行精确匹配对 CRLF 容错），写入远端的产物即为 LF。
func EnsureFallEventsProxy(conf string) string {
	conf = strings.ReplaceAll(conf, "\r\n", "\n")
	lines := strings.Split(conf, "\n")
	if !strings.Contains(conf, chuteUpstreamMarker) {
		anchor := firstLineIndex(lines, chuteUpstreamAnchor, chuteServerFallback)
		if anchor >= 0 {
			lines = insertBefore(lines, anchor, chuteUpstreamBlock)
		}
	}
	if !strings.Contains(conf, chuteLocationMarker) {
		// 首选锚是前缀（任务创建的 location 行含随模板演进的正则），
		// 兜底 catch-all location /（任何形态的配置都有）。
		anchor := firstLineIndex(lines, chuteCatchAllAnchor)
		for i, line := range lines {
			if strings.HasPrefix(line, chuteTaskLocationPrefix) {
				anchor = i
				break
			}
		}
		if anchor >= 0 {
			lines = insertBefore(lines, anchor, chuteLocationBlock)
		}
	}
	return strings.Join(lines, "\n")
}

// firstLineIndex 依次尝试各锚点行，返回第一个命中的行号（都不存在返回 -1）。
func firstLineIndex(lines []string, anchors ...string) int {
	for _, anchor := range anchors {
		for i, line := range lines {
			if line == anchor {
				return i
			}
		}
	}
	return -1
}

func insertBefore(lines []string, at int, block []string) []string {
	out := make([]string, 0, len(lines)+len(block))
	out = append(out, lines[:at]...)
	out = append(out, block...)
	out = append(out, lines[at:]...)
	return out
}

// EnsureIngressPollEnv 在 center-cv-ingress 的 environment 段内幂等补齐
// 错分检测轮询插值行（只增不删）。渲染模板已内置这些行，本函数服务于
// 模板更新前已初始化中心的存量 Compose。老版本模板可能没有 ingress 服务
// ——轮询收口在新版 ingress，无服务即无轮询可配，静默跳过、待重新初始化
// 后自然带上，不阻断 WMS 同步；服务存在但缺 environment 段时自动补段。
// environment 行与插值条目的缩进都从文件实际内容推导（既有子条目的缩进
// 优先），手写或重排过的 Compose 不再因缩进差异被静默跳过或补错位。
func EnsureIngressPollEnv(compose string) (string, error) {
	lines := strings.Split(compose, "\n")
	start := serviceHeaderAt(lines, ingressServiceKey)
	if start < 0 {
		return compose, nil
	}
	hdr := lineIndent(lines[start])
	end := serviceBlockEnd(lines, start)
	envStart, entryIndent := -1, hdr+4
	for i := start + 1; i < end; i++ {
		if lineIndent(lines[i]) > hdr && strings.TrimLeft(lines[i], " \t") == "environment:" {
			envStart = i
			break
		}
	}
	present := map[string]bool{}
	if envStart >= 0 {
		envIndent := lineIndent(lines[envStart])
		entryIndent = envIndent + 2
		envEnd := end
		for i := envStart + 1; i < end; i++ {
			if lines[i] != "" && lineIndent(lines[i]) <= envIndent {
				envEnd = i
				break
			}
			if entryIndent == envIndent+2 && lines[i] != "" && lineIndent(lines[i]) > envIndent {
				entryIndent = lineIndent(lines[i])
			}
		}
		for i := envStart + 1; i < envEnd; i++ {
			trimmed := strings.TrimSpace(lines[i])
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if idx := strings.Index(trimmed, ":"); idx > 0 {
				present[trimmed[:idx]] = true
			}
		}
		return ensureEnvEntries(lines, envEnd, entryIndent, present)
	}
	// 服务块内没有 environment 段：在服务头行之后补一段，字段缩进取块内
	// 既有字段的实际缩进（无字段行回落模板步长）。
	fieldIndent := hdr + 2
	for i := start + 1; i < end; i++ {
		if lines[i] != "" && !strings.HasPrefix(strings.TrimSpace(lines[i]), "#") && lineIndent(lines[i]) > hdr {
			fieldIndent = lineIndent(lines[i])
			break
		}
	}
	out := make([]string, 0, len(lines)+len(ingressPollEnvLines)+1)
	out = append(out, lines[:start+1]...)
	out = append(out, strings.Repeat(" ", fieldIndent)+"environment:")
	out = append(out, lines[start+1:]...)
	return ensureEnvEntries(out, start+2, fieldIndent+2, present)
}

// ensureEnvEntries 把缺失的轮询插值行按 entryIndent 插到 at 行之前，
// 已存在的键（present）只增不删。
func ensureEnvEntries(lines []string, at, entryIndent int, present map[string]bool) (string, error) {
	missing := make([]string, 0, len(ingressPollEnvLines))
	for _, want := range ingressPollEnvLines {
		trimmed := strings.TrimSpace(want)
		key := trimmed[:strings.Index(trimmed, ":")]
		if !present[key] {
			missing = append(missing, want)
		}
	}
	if len(missing) == 0 {
		return strings.Join(lines, "\n"), nil
	}
	out := make([]string, 0, len(lines)+len(missing))
	out = append(out, lines[:at]...)
	out = append(out, reindentTo(missing, entryIndent)...)
	out = append(out, lines[at:]...)
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
// 启用时写入三个变量，关闭时移除；用户自行添加的其它行与注释保持不动。
// CV_ORGAN_CODE 已不再配置，但仍按托管变量清理旧版本写入的存量行，
// 让两服务回落 Compose 插值默认值。
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
