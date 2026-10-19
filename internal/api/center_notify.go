package api

import (
	"context"
	"time"
)

// notifyCenterResult 在同步完成的中心级即时操作（固定/解绑模型版本组）结束时
// 发送一条结果消息：成功发操作图标 + 完成消息（可附明细），失败发 ❌ 失败消息。
// 即时操作不拆开始/结束两条，避免刷屏；与发布通知一致：尽力而为，不影响操作。
func notifyCenterResult(sender textSender, icon, action, operator, centerName, successDetail string, err error) {
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
	if err != nil {
		send("❌ " + action + "失败" + operatorText + "\n中心：" + centerName + "\n错误：" + singleLine(err.Error(), 200))
		return
	}
	message := icon + " " + action + "完成" + operatorText + "\n中心：" + centerName
	if successDetail != "" {
		message += "\n" + successDetail
	}
	send(message)
}

// notifyCenterOperation 在中心级运维操作（暂停/恢复）提交时向飞书群
// 发送开始消息，并返回结束回调：任务结束时按结果发送成功或失败消息；成功消息
// 可附带操作明细。初始化和重置中心不发送通知。
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
