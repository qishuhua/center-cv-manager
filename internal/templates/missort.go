package templates

import (
	"strconv"
	"strings"
)

// 错分检测（mis-sort）架构：ingress 统一控制开关——CV_CAMERA_POLL_ENABLED=1
// 时启动摄像机配置轮询（创建 9/10 类错分任务）并绑定计划落格 ZeroMQ PUB 端口
// （CV_PLANNED_ZMQ_BIND）；计划落格事件经网关 /fall-events/planned（misAuth 鉴权）
// 进入 ingress 原样广播，Worker（center-cv-service）订阅 CV_PLANNED_ZMQ_ENDPOINTS
// 本地核验。告警上报由 video-transcode-service 直接完成（消费 cv.alarm.transcode，
// 错分核验结果作为 detectResult 字段随同一业务回调上报，地址按消息 env 在
// UAT/PRO 两套间选择）。本文件维护 business 模块的错分托管变量与插值行补齐。

// MisSortOptions 描述中心的错分检测配置，用于维护 business 模块 module.env。
type MisSortOptions struct {
	Enabled bool
	WMSURL  string
	MisAuth string
	// IntervalSeconds 是 ingress 摄像机配置轮询周期（秒）；≤0 时按
	// DefaultCameraPollIntervalSeconds（1 小时）写入。仅启用错分时生效。
	IntervalSeconds int
}

// DefaultCameraPollIntervalSeconds 是摄像机配置轮询周期的默认值（1 小时），
// 与 Compose 插值默认及中心编辑页口径一致。
const DefaultCameraPollIntervalSeconds = 3600

const (
	// module.env 中由中心 WMS 设置托管的变量，启用时按此顺序写入。
	misSortEnvMarker = "# 错分检测（WMS）配置：由中心 WMS 设置维护，修改请回中心编辑"
)

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

// ensureServiceEnv 幂等保证指定服务块的 environment 含全部缺失插值行
//（只增不删，同键已有任意形式的条目一律不动）：渲染模板已内置这些行，
// 本函数在错分设置同步时兜底补齐（防远端手工删改）。environment 行与
// 插值条目的缩进都从文件实际内容推导（既有子条目的缩进优先），手写或
// 重排过的 Compose 不会因缩进差异被静默跳过或补错位；服务存在但缺
// environment 段时自动补段。服务不存在时原样返回。
func ensureServiceEnv(compose, service string, entries []string) string {
	lines := strings.Split(compose, "\n")
	start := serviceHeaderAt(lines, service)
	if start < 0 {
		return compose
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
		return ensureEnvEntries(lines, envEnd, entryIndent, present, entries)
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
	out := make([]string, 0, len(lines)+len(entries)+1)
	out = append(out, lines[:start+1]...)
	out = append(out, strings.Repeat(" ", fieldIndent)+"environment:")
	out = append(out, lines[start+1:]...)
	return ensureEnvEntries(out, start+2, fieldIndent+2, present, entries)
}

// ensureEnvEntries 把缺失的环境插值行按 entryIndent 插到 at 行之前，
// 已存在的键（present）只增不删。
func ensureEnvEntries(lines []string, at, entryIndent int, present map[string]bool, entries []string) string {
	missing := make([]string, 0, len(entries))
	for _, want := range entries {
		trimmed := strings.TrimSpace(want)
		key := trimmed[:strings.Index(trimmed, ":")]
		if !present[key] {
			missing = append(missing, want)
		}
	}
	if len(missing) == 0 {
		return strings.Join(lines, "\n")
	}
	out := make([]string, 0, len(lines)+len(missing))
	out = append(out, lines[:at]...)
	out = append(out, reindentTo(missing, entryIndent)...)
	out = append(out, lines[at:]...)
	return strings.Join(out, "\n")
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

// ApplyMisSortModuleEnv 维护 module.env 的错分托管块：启用时写入 WMS 地址、
// 共享鉴权、ingress 轮询开关与轮询周期（错分检测由 ingress 统一控制；WMS
// 地址与鉴权由 ingress 轮询和 video-transcode 的 9/10 核验直发共用）；关闭时
// 移除托管变量，回落 Compose 插值默认值（轮询 0、WMS/鉴权为空、周期 1 小时，
// 9/10 核验上报随之失效）。历史遗留的机构编码行一并清理。
func ApplyMisSortModuleEnv(env string, opts MisSortOptions) string {
	managed := map[string]bool{
		"CV_WMS_URL":                     true,
		"CV_MIS_AUTH":                    true,
		"CV_ORGAN_CODE":                  true,
		"CV_CAMERA_POLL_ENABLED":         true,
		"CV_CAMERA_POLL_INTERVAL_SECONDS": true,
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
		interval := opts.IntervalSeconds
		if interval <= 0 {
			interval = DefaultCameraPollIntervalSeconds
		}
		block := misSortEnvMarker + "\n" +
			"CV_WMS_URL=" + opts.WMSURL + "\n" +
			"CV_MIS_AUTH=" + opts.MisAuth + "\nCV_CAMERA_POLL_ENABLED=1" +
			"\nCV_CAMERA_POLL_INTERVAL_SECONDS=" + strconv.Itoa(interval)
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

// EnsureIngressPollEnv 幂等保证 ingress 服务块的 environment 含错分检测全部
// 插值行（只增不删）：轮询开关、WMS 地址/鉴权、配置路径与周期，以及计划
// 落格 ZeroMQ 发布端口。存量中心缺少的行由错分同步补齐。
func EnsureIngressPollEnv(compose string) string {
	return ensureServiceEnv(compose, "center-cv-ingress", []string{
		"      CV_CAMERA_POLL_ENABLED: ${CV_CAMERA_POLL_ENABLED:-0}",
		"      CV_WMS_URL: ${CV_WMS_URL:-http://10.21.61.48:31995}",
		"      CV_MIS_AUTH: ${CV_MIS_AUTH:-20260606123a}",
		"      CV_CAMERA_CONFIG_PATH: ${CV_CAMERA_CONFIG_PATH:-/missort/api/cameraConfig/query}",
		"      CV_CAMERA_POLL_INTERVAL_SECONDS: ${CV_CAMERA_POLL_INTERVAL_SECONDS:-3600}",
		"      CV_CAMERA_CONFIG_TIMEOUT_MS: ${CV_CAMERA_CONFIG_TIMEOUT_MS:-5000}",
		"      CV_PLANNED_ZMQ_BIND: ${CV_PLANNED_ZMQ_BIND:-tcp://127.0.0.1:8091}",
	})
}

// EnsureWorkerZMQEnv 幂等保证 Worker 公共环境锚点含计划落格订阅与时区偏移
// 插值行（只增不删）：渲染模板已内置，本函数在错分同步时兜底补齐，订阅
// 地址默认与 ingress 的 CV_PLANNED_ZMQ_BIND 对应。
func EnsureWorkerZMQEnv(compose string) string {
	compose = ensureWorkerEnvEntry(compose, "CV_PLANNED_ZMQ_ENDPOINTS", "CV_PLANNED_ZMQ_ENDPOINTS: ${CV_PLANNED_ZMQ_ENDPOINTS:-tcp://127.0.0.1:8091}")
	return ensureWorkerEnvEntry(compose, "CV_EVENT_TIME_UTC_OFFSET_MINUTES", "CV_EVENT_TIME_UTC_OFFSET_MINUTES: ${CV_EVENT_TIME_UTC_OFFSET_MINUTES:-480}")
}

// EnsureTranscodeAlarmEnv 幂等保证转码服务的 environment 含告警上报地址
// 插值行（只增不删）：普通告警按消息 env 在 UAT/PRO 两套间选择回调地址
// （错分 9/10 核验结果不经此处，直发 CV_WMS_URL 拼接的 missort 接收端，
// 由模板 transcode 侧 CV_WMS_URL/CV_MIS_AUTH 插值行取值）。插值默认与
// 源码内置值一致（getenv 读到空串会覆盖内置地址，不能留空默认）；改地址
// 走 module.env 同名变量覆盖。
func EnsureTranscodeAlarmEnv(compose string) string {
	return ensureServiceEnv(compose, "video-transcode-service", []string{
		"      CV_PRO_POST_API: ${CV_PRO_POST_API:-https://jmsgw.jtexpress.com.cn/businessplan/aiVideoAnalysis/v2/algorithmCallBack}",
		"      CV_UAT_POST_API: ${CV_UAT_POST_API:-https://uat-jmsgw.jtexpress.com.cn/businessplan/aiVideoAnalysis/v2/algorithmCallBack}",
		"      ALARM_PRO_OSS_UPLOAD_URL_API: ${ALARM_PRO_OSS_UPLOAD_URL_API:-https://jmsgw.jtexpress.com.cn/businessplan/fileUpload/getUploadUrl}",
		"      ALARM_UAT_OSS_UPLOAD_URL_API: ${ALARM_UAT_OSS_UPLOAD_URL_API:-https://uat-jmsgw.jtexpress.com.cn/businessplan/fileUpload/getUploadUrl}",
	})
}
