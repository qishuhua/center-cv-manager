package templates

import "strings"

// 业务 Debug 模式共享边界：center-cv-service 读 LOG_LEVEL 环境变量（源码默认
// info，支持 trace/debug/info/warn/error/critical），中心「Debug 模式」开关经
// business 模块 module.env 托管该值，Compose Worker 公共环境锚点用插值默认
// 兜底，未写入时业务日志保持 info。

// logLevelEnvMarker 是 module.env 中由中心 Debug 模式开关托管的块标记。
// （标记字符串写入远端文件，改名会破坏存量中心托管块的识别与清理，勿改。）
const logLevelEnvMarker = "# 业务日志等级：由中心调试日志开关维护，修改请回中心编辑"

const workerLogLevelLine = "LOG_LEVEL: ${LOG_LEVEL:-info}"

// ApplyLogLevelModuleEnv 幂等维护 module.env 中的 LOG_LEVEL：开启 Debug 模式
// 时写入 debug，关闭时移除托管值（回落 Compose 插值默认 info）；用户自行
// 添加的其它行与注释（含错分检测托管块）保持不动。
func ApplyLogLevelModuleEnv(env string, debug bool) string {
	lines := strings.Split(env, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if key := envKeyOf(line); key == "LOG_LEVEL" {
			continue
		}
		if strings.TrimSpace(line) == logLevelEnvMarker {
			continue
		}
		kept = append(kept, line)
	}
	result := strings.TrimRight(strings.Join(kept, "\n"), "\n ")
	if debug {
		block := logLevelEnvMarker + "\nLOG_LEVEL=debug"
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

// EnsureWorkerLogLevelEnv 幂等保证 business Compose 的 Worker 公共环境锚点
// 含 LOG_LEVEL 插值行（只增不删）：渲染模板已内置该行，本函数服务于模板
// 更新前已初始化中心的存量 Compose。已存在任意形式的 LOG_LEVEL 条目（含
// 手工硬编码）一律不动；锚点缺失（异常布局）时原样返回，等待整体校验
// 暴露问题。插入缩进取锚点内既有条目的实际缩进（回落模板 2 空格）。
func EnsureWorkerLogLevelEnv(compose string) string {
	lines := strings.Split(strings.ReplaceAll(compose, "\r\n", "\n"), "\n")
	header := -1
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == "x-worker-environment: &worker-environment" {
			header = i
			break
		}
	}
	if header < 0 {
		return strings.Join(lines, "\n")
	}
	end := len(lines)
	for j := header + 1; j < len(lines); j++ {
		if lines[j] != "" && lineIndent(lines[j]) == 0 {
			end = j
			break
		}
	}
	entryIndent, insertAt := -1, -1
	for j := header + 1; j < end; j++ {
		trimmed := strings.TrimSpace(lines[j])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "LOG_LEVEL:") {
			return strings.Join(lines, "\n")
		}
		if entryIndent < 0 {
			entryIndent = lineIndent(lines[j])
		}
		insertAt = j + 1
	}
	if insertAt < 0 {
		// 空锚点块：直接在头行之后补一条目。
		insertAt, entryIndent = header+1, 2
	}
	if entryIndent < 1 {
		entryIndent = 2
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:insertAt]...)
	out = append(out, strings.Repeat(" ", entryIndent)+workerLogLevelLine)
	out = append(out, lines[insertAt:]...)
	return strings.Join(out, "\n")
}
