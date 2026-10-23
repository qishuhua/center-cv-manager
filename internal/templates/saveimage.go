package templates

import "strings"

// 业务存图共享边界：center-cv-service 读 CV_SAVE_DEBUG_IMAGE 环境变量
// （源码默认 0，即不存图；置 1 时每轮推理保存调试图片——原始帧与带检测框
// 的结果帧），中心「存图」开关经 business 模块 module.env 托管该值，Compose
// Worker 公共环境锚点用插值默认兜底，未写入时业务不存图。

// saveImageEnvMarker 是 module.env 中由中心存图开关托管的块标记。
// （标记字符串写入远端文件，改名会破坏存量中心托管块的识别与清理，勿改。）
const saveImageEnvMarker = "# 业务存图：由中心存图开关维护，修改请回中心编辑"

const workerSaveImageLine = "CV_SAVE_DEBUG_IMAGE: ${CV_SAVE_DEBUG_IMAGE:-0}"

// ApplySaveImageModuleEnv 幂等维护 module.env 中的 CV_SAVE_DEBUG_IMAGE：
// 开启存图时写入 1，关闭时移除托管值（回落 Compose 插值默认 0）；用户自行
// 添加的其它行与注释（含错分检测、Debug 模式托管块）保持不动。
func ApplySaveImageModuleEnv(env string, enabled bool) string {
	lines := strings.Split(env, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if key := envKeyOf(line); key == "CV_SAVE_DEBUG_IMAGE" {
			continue
		}
		if strings.TrimSpace(line) == saveImageEnvMarker {
			continue
		}
		kept = append(kept, line)
	}
	result := strings.TrimRight(strings.Join(kept, "\n"), "\n ")
	if enabled {
		block := saveImageEnvMarker + "\nCV_SAVE_DEBUG_IMAGE=1"
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

// EnsureWorkerSaveImageEnv 幂等保证 business Compose 的 Worker 公共环境锚点
// 含 CV_SAVE_DEBUG_IMAGE 插值行（只增不删）：渲染模板已内置该行，本函数
// 服务于模板更新前已初始化中心的存量 Compose。
func EnsureWorkerSaveImageEnv(compose string) string {
	return ensureWorkerEnvEntry(compose, "CV_SAVE_DEBUG_IMAGE", workerSaveImageLine)
}
