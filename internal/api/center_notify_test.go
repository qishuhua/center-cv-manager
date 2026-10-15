package api

import (
	"errors"
	"strings"
	"testing"
)

func TestNotifyCenterOperationStartAndOutcome(t *testing.T) {
	sender := &fakeFeishuSender{}
	finish := notifyCenterOperation(sender, "⚙️", "重置中心", "admin", "重庆中心", "将删除受管 Compose 容器与部署配置。")
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0], "重置中心开始（操作人：admin）") || !strings.Contains(sender.messages[0], "中心：重庆中心") {
		t.Fatalf("start message = %v", sender.messages)
	}
	finish(nil, "")
	if len(sender.messages) != 2 || !strings.Contains(sender.messages[1], "✅ 重置中心完成") {
		t.Fatalf("success message = %v", sender.messages)
	}
	detail := "业务代码：\n- center-cv-service:master@abcd1234\n模型：\n- face:v3[nvidia-A10]@deadbeef1234"
	finish(nil, detail)
	if len(sender.messages) != 3 || !strings.Contains(sender.messages[2], "✅ 重置中心完成") || !strings.Contains(sender.messages[2], "face:v3[nvidia-A10]@deadbeef1234") {
		t.Fatalf("success message with detail = %v", sender.messages)
	}
	finish(errors.New("boom"), detail)
	if len(sender.messages) != 4 || !strings.Contains(sender.messages[3], "❌ 重置中心失败") || !strings.Contains(sender.messages[3], "boom") || strings.Contains(sender.messages[3], "face:v3") {
		t.Fatalf("failure message must include error and skip success detail = %v", sender.messages)
	}
	// sender 为 nil 时必须安静跳过，不能 panic。
	notifyCenterOperation(nil, "⏸️", "暂停中心", "", "x", "")(nil, "")
}

func TestNotifyCenterResultSendsSingleMessage(t *testing.T) {
	sender := &fakeFeishuSender{}
	notifyCenterResult(sender, "🔒", "固定模型版本组", "admin", "重庆中心", "已固定 2 个模型版本：\n- fire:v3[nvidia-A10]@abc123def456\n- smoke:v2[onnx]@deadbeef1234\n固定后仅允许相同 Manifest 的幂等发布，调整任一模型前需整体解绑。", nil)
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0], "🔒 固定模型版本组完成（操作人：admin）") || !strings.Contains(sender.messages[0], "中心：重庆中心") || !strings.Contains(sender.messages[0], "- fire:v3[nvidia-A10]@abc123def456") {
		t.Fatalf("success message = %v", sender.messages)
	}
	notifyCenterResult(sender, "🔓", "解绑模型版本组", "", "重庆中心", "解绑后可新增、删除或切换该中心的模型版本。", errors.New("boom"))
	if len(sender.messages) != 2 || !strings.Contains(sender.messages[1], "❌ 解绑模型版本组失败") || !strings.Contains(sender.messages[1], "boom") {
		t.Fatalf("failure message = %v", sender.messages)
	}
	// 无操作人时不带操作人后缀；sender 为 nil 时必须安静跳过，不能 panic。
	notifyCenterResult(sender, "🔒", "固定模型版本组", "", "重庆中心", "", nil)
	if len(sender.messages) != 3 || strings.Contains(sender.messages[2], "操作人") {
		t.Fatalf("anonymous success message = %v", sender.messages)
	}
	notifyCenterResult(nil, "🔓", "解绑模型版本组", "", "x", "", nil)
}
