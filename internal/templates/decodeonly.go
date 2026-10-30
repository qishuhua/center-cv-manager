package templates

import "strings"

// 业务仅解码共享边界：center-cv-service 读 CV_DECODE_ONLY_MODE 环境变量
// （仅进程启动时读取，源码默认 0；置 1 时只拉流、解码、按任务既有采样策略
// 抽帧，不加载模型、不执行推理/追踪，也不产生事件、录片与告警消息），中心
//「仅解码模式」开关经 business 模块 module.env 托管该值，Compose Worker
// 公共环境锚点用插值默认兜底，未写入时业务正常推理。

// decodeOnlyEnvMarker 是 module.env 中由中心仅解码开关托管的块标记。
// （标记字符串写入远端文件，改名会破坏存量中心托管块的识别与清理，勿改。）
const decodeOnlyEnvMarker = "# 业务仅解码：由中心仅解码开关维护，修改请回中心编辑"

const workerDecodeOnlyLine = "CV_DECODE_ONLY_MODE: ${CV_DECODE_ONLY_MODE:-0}"

// ApplyDecodeOnlyModuleEnv 幂等维护 module.env 中的 CV_DECODE_ONLY_MODE：
// 开启仅解码时写入 1，关闭时移除托管值（回落 Compose 插值默认 0）；用户自行
// 添加的其它行与注释（含错分检测、Debug 模式、存图托管块）保持不动。
func ApplyDecodeOnlyModuleEnv(env string, enabled bool) string {
	lines := strings.Split(env, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if key := envKeyOf(line); key == "CV_DECODE_ONLY_MODE" {
			continue
		}
		if strings.TrimSpace(line) == decodeOnlyEnvMarker {
			continue
		}
		kept = append(kept, line)
	}
	result := strings.TrimRight(strings.Join(kept, "\n"), "\n ")
	if enabled {
		block := decodeOnlyEnvMarker + "\nCV_DECODE_ONLY_MODE=1"
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

// EnsureWorkerDecodeOnlyEnv 幂等保证 business Compose 的 Worker 公共环境锚点
// 含 CV_DECODE_ONLY_MODE 插值行（只增不删）：渲染模板已内置该行，本函数
// 服务于此开关上线前已初始化中心的存量 Compose。
func EnsureWorkerDecodeOnlyEnv(compose string) string {
	return ensureWorkerEnvEntry(compose, "CV_DECODE_ONLY_MODE", workerDecodeOnlyLine)
}
