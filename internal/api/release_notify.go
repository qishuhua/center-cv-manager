package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/task"
)

// textSender 抽象飞书文本发送，便于测试。
type textSender interface {
	SendText(ctx context.Context, text string) error
}

type releaseOutcome struct {
	center string
	order  int
	err    error
}

// releaseNotifier 在统一版本发布开始时和全部中心任务结束后，向飞书群发送
// 一条汇总消息，告知哪些中心正在发版（发布期间健康告警属预期）。
// 所有发送均为尽力而为：失败不影响发布任务本身。
type releaseNotifier struct {
	sender   textSender
	operator string
	names    []string
	order    map[string]int
	mu       sync.Mutex
	outcomes []releaseOutcome
	seen     map[string]bool
}

func newReleaseNotifier(sender textSender, operator string, centerLines []string) *releaseNotifier {
	names := make([]string, len(centerLines))
	order := make(map[string]int, len(centerLines))
	for i, line := range centerLines {
		if center, _, found := strings.Cut(line, "："); found {
			names[i] = center
			order[center] = i
		}
	}
	n := &releaseNotifier{sender: sender, operator: operator, names: names, order: order, seen: map[string]bool{}}
	var message strings.Builder
	message.WriteString("🚀 版本发布开始")
	if operator != "" {
		message.WriteString("（操作人：" + operator + "）")
	}
	message.WriteString("\n")
	message.WriteString(strings.Join(centerLines, "\n"))
	if len(names) > 1 {
		message.WriteString(fmt.Sprintf("\n共 %d 个中心；", len(names)))
	} else {
		message.WriteString("\n")
	}
	message.WriteString("发布期间以上中心的健康检查告警属预期，发布结束后恢复。")
	n.send(message.String())
	return n
}

// record 由每个中心的发布任务在结束时调用；最后一个记录触发结束消息。
// 幂等：同一中心的重复记录（executor 与兜底轮询并发到达）只保留首条。
func (n *releaseNotifier) record(center string, err error) {
	n.mu.Lock()
	if n.seen[center] {
		n.mu.Unlock()
		return
	}
	n.seen[center] = true
	n.outcomes = append(n.outcomes, releaseOutcome{center: center, order: n.order[center], err: err})
	complete := len(n.outcomes) >= len(n.names)
	n.mu.Unlock()
	if complete {
		n.sendEnd()
	}
}

// watchReleaseNotificationsAsync 按任务参数里的中心名启动兜底轮询。
func (s *Server) watchReleaseNotificationsAsync(notifier *releaseNotifier, created []*domain.Task) {
	ids := make([]string, 0, len(created))
	names := make([]string, 0, len(created))
	for _, item := range created {
		if item == nil {
			continue
		}
		ids = append(ids, item.ID)
		names = append(names, item.Params["center_name"])
	}
	go s.watchReleaseNotifications(notifier, ids, names)
}

// watchReleaseNotifications 兜底记录：任务管理器对已取消的上下文直接跳过
// executor，其中的 record 不会触发（如提交发布后立即"停止所有任务"），群内
// 会只剩"发布开始"没有下文。这里轮询任务终态补记，直到全部结束。
func (s *Server) watchReleaseNotifications(notifier *releaseNotifier, taskIDs, centerNames []string) {
	deadline := time.Now().Add(time.Hour)
	for time.Now().Before(deadline) {
		pending := false
		for index, id := range taskIDs {
			if index >= len(centerNames) {
				break
			}
			current, found := s.tasks.Get(id)
			if !found {
				continue
			}
			switch current.Status {
			case task.Pending, task.Running:
				pending = true
			case task.Cancelled:
				notifier.record(centerNames[index], fmt.Errorf("任务已停止"))
			case task.Failed:
				// 正常路径 executor 已记录真实错误；这里兜底进程重启等场景。
				notifier.record(centerNames[index], errors.New(current.Error))
			default:
				notifier.record(centerNames[index], nil)
			}
		}
		if !pending {
			return
		}
		time.Sleep(3 * time.Second)
	}
}

func (n *releaseNotifier) sendEnd() {
	outcomes := n.snapshot()
	// 按发布开始时的中心顺序展示，避免并发完成顺序打乱消息。
	for i := range outcomes {
		for j := i + 1; j < len(outcomes); j++ {
			if outcomes[j].order < outcomes[i].order {
				outcomes[i], outcomes[j] = outcomes[j], outcomes[i]
			}
		}
	}
	var succeeded, failed []string
	for _, outcome := range outcomes {
		if outcome.err == nil {
			succeeded = append(succeeded, outcome.center)
		} else {
			failed = append(failed, outcome.center+"（"+singleLine(outcome.err.Error(), 120)+"）")
		}
	}
	var message strings.Builder
	message.WriteString("📦 版本发布结束\n")
	switch {
	case len(failed) == 0:
		message.WriteString("✅ 全部成功：" + strings.Join(succeeded, "、"))
	case len(succeeded) == 0:
		message.WriteString("❌ 全部失败：" + strings.Join(failed, "、"))
	default:
		message.WriteString("✅ 成功：" + strings.Join(succeeded, "、") + "\n")
		message.WriteString("❌ 失败：" + strings.Join(failed, "、"))
	}
	n.send(message.String())
}

func (n *releaseNotifier) snapshot() []releaseOutcome {
	n.mu.Lock()
	defer n.mu.Unlock()
	outcomes := make([]releaseOutcome, len(n.outcomes))
	copy(outcomes, n.outcomes)
	return outcomes
}

func (n *releaseNotifier) send(text string) {
	if n.sender == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 通知失败只忽略：发版本身不受影响。
	_ = n.sender.SendText(ctx, text)
}

// singleLine 把错误压缩成一行并截断，避免刷屏。
func singleLine(text string, limit int) string {
	cleaned := strings.Join(strings.Fields(text), " ")
	if len([]rune(cleaned)) > limit {
		return string([]rune(cleaned)[:limit]) + "…"
	}
	return cleaned
}
