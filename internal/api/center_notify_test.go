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
