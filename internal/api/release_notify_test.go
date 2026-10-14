package api

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

type fakeFeishuSender struct {
	mu       sync.Mutex
	messages []string
}

func (f *fakeFeishuSender) SendText(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, text)
	return nil
}

func TestReleaseNotifierStartAndEndMessages(t *testing.T) {
	sender := &fakeFeishuSender{}
	notifier := newReleaseNotifier(sender, "admin", []string{
		"中心A：center-cv-service:v2.1@abcd1234",
		"中心B：center-cv-service:v2.1@abcd1234、face:v3[nvidia-A10]@deadbeef",
	})
	if len(sender.messages) != 1 {
		t.Fatalf("start message count = %d", len(sender.messages))
	}
	start := sender.messages[0]
	for _, want := range []string{"🚀 版本发布开始", "操作人：admin", "中心A：center-cv-service", "face:v3", "共 2 个中心", "健康检查告警属预期"} {
		if !strings.Contains(start, want) {
			t.Fatalf("start message missing %q: %s", want, start)
		}
	}

	// 部分失败：分组展示成功与失败中心。
	notifier.record("中心A", nil)
	if len(sender.messages) != 1 {
		t.Fatal("end message must wait for all centers")
	}
	notifier.record("中心B", errors.New("发布预检失败:\n镜像缺失"))
	if len(sender.messages) != 2 {
		t.Fatalf("end message count = %d", len(sender.messages))
	}
	end := sender.messages[1]
	for _, want := range []string{"📦 版本发布结束", "✅ 成功：中心A", "❌ 失败：中心B", "发布预检失败: 镜像缺失"} {
		if !strings.Contains(end, want) {
			t.Fatalf("end message missing %q: %s", want, end)
		}
	}
}

func TestReleaseNotifierAllSuccessAndConcurrentRecords(t *testing.T) {
	sender := &fakeFeishuSender{}
	names := []string{"中心A：v1", "中心B：v1", "中心C：v1"}
	notifier := newReleaseNotifier(sender, "", names)
	var wg sync.WaitGroup
	for _, name := range []string{"中心A", "中心B", "中心C"} {
		wg.Add(1)
		go func(center string) {
			defer wg.Done()
			notifier.record(center, nil)
		}(name)
	}
	wg.Wait()
	if len(sender.messages) != 2 {
		t.Fatalf("messages = %d, want start+end", len(sender.messages))
	}
	if !strings.Contains(sender.messages[1], "✅ 全部成功：中心A、中心B、中心C") {
		t.Fatalf("unexpected end message: %s", sender.messages[1])
	}
}

func TestSingleLineTruncates(t *testing.T) {
	long := strings.Repeat("错", 300)
	got := singleLine(long+"\nsecond line", 50)
	if strings.Contains(got, "\n") || strings.Contains(got, "second") {
		t.Fatalf("multi-line content survived: %q", got)
	}
	if runes := len([]rune(got)); runes > 51 {
		t.Fatalf("truncation failed: %d runes", runes)
	}
}
