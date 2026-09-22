package modelasset

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCurrentVersionHealthReplacesPersistedWarning(t *testing.T) {
	current := CurrentVersion{Healthy: false, HealthError: "历史路径错误"}
	setCurrentVersionHealth(&current, nil)
	if !current.Healthy || current.HealthError != "" {
		t.Fatalf("successful recheck retained historical warning: %+v", current)
	}
	setCurrentVersionHealth(&current, errors.New("当前文件大小不符"))
	if current.Healthy || current.HealthError != "当前文件大小不符" {
		t.Fatalf("failed recheck did not replace health status: %+v", current)
	}
}

func TestHealthFileResult(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
		err                error
	}{
		{name: "ready", output: "123\n"},
		{name: "empty", output: "0", want: "为空"},
		{name: "size mismatch", output: "99", want: "预期 123 字节，实际 99 字节"},
		{name: "broken link", output: "broken-link", want: "符号链接"},
		{name: "directory", output: "not-file", want: "不是普通文件"},
		{name: "invalid output", output: "unexpected", want: "无效结果"},
		{name: "permission", output: "Permission denied", err: context.Canceled, want: "Permission denied"},
		{name: "timeout", err: context.DeadlineExceeded, want: "context deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := healthFileResult("/models/example.plan", "运行时模型路径", 123, tc.output, tc.err)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "/models/example.plan") {
				t.Fatalf("expected path and %q, got %v", tc.want, err)
			}
		})
	}
}
