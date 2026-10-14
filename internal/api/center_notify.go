package api

import (
	"context"
	"time"
)

// notifyCenterOperation 在中心级运维操作（初始化/重置/暂停/恢复）提交时向飞书群
// 发送开始消息，并返回结束回调：任务结束时按结果发送成功或失败消息；成功消息
// 可附带明细（如初始化发布的业务代码与模型版本清单）。
// 与发布通知一致：尽力而为，发送失败不影响操作任务本身。
func notifyCenterOperation(sender textSender, icon, action, operator, centerName, detail string) func(error, string) {
	send := func(text string) {
		if sender == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = sender.SendText(ctx, text)
	}
	operatorText := ""
	if operator != "" {
		operatorText = "（操作人：" + operator + "）"
	}
	send(icon + " " + action + "开始" + operatorText + "\n中心：" + centerName + "\n" + detail)
	return func(err error, successDetail string) {
		if err != nil {
			send("❌ " + action + "失败" + operatorText + "\n中心：" + centerName + "\n错误：" + singleLine(err.Error(), 200))
			return
		}
		message := "✅ " + action + "完成" + operatorText + "\n中心：" + centerName
		if successDetail != "" {
			message += "\n" + successDetail
		}
		send(message)
	}
}
