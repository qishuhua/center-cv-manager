package modelasset

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
)

func setCurrentVersionHealth(current *CurrentVersion, checkErr error) {
	// Persisted status is historical; always replace both fields together.
	current.Healthy = checkErr == nil
	current.HealthError = ""
	if checkErr != nil {
		current.HealthError = checkErr.Error()
	}
}

// Health checks are read-only. Report the failing path instead of treating
// every remote command failure as a missing or damaged model.
func currentVersionHealth(ctx context.Context, files remoteFiles, current CurrentVersion, item ReleaseItem) error {
	alias, _, artifact, err := runtimeAlias(item)
	if err != nil {
		return err
	}
	stored := current.StoragePath
	label := "内部版本文件"
	if stored == "" {
		stored = artifact.TargetPath
		label = "旧版模型文件"
	}
	if err := checkHealthFile(ctx, files, path.Join(files.root, stored), label, artifact.Size); err != nil {
		return err
	}
	if current.StoragePath == "" && !repositoryLinkPortable(ctx, files, item) {
		return fmt.Errorf("旧版模型目录 %s 的链接可移植性检查失败；需修复运行时路径，模型版本文件已通过大小校验", path.Join(files.root, current.RepositoryPath))
	}
	if err := checkHealthFile(ctx, files, path.Join(files.root, alias), "运行时模型路径", artifact.Size); err != nil {
		return fmt.Errorf("%w；版本文件已通过大小校验，可重新发布同一版本以修复运行时路径", err)
	}
	return nil
}

func healthFileCommand(name string) string {
	return "file=" + shellQuote(name) + `; if [ -f "$file" ]; then stat -L -c %s -- "$file"; elif [ -L "$file" ]; then printf 'broken-link'; elif [ -e "$file" ]; then printf 'not-file'; else stat -L -c %s -- "$file"; fi`
}

func checkHealthFile(ctx context.Context, files remoteFiles, name, label string, expected int64) error {
	output, err := files.run(ctx, healthFileCommand(name))
	return healthFileResult(name, label, expected, string(output), err)
}

func healthFileResult(name, label string, expected int64, output string, checkErr error) error {
	value := strings.TrimSpace(output)
	if checkErr != nil {
		return fmt.Errorf("%s %s 检查失败（路径不存在、权限不足或 SSH 异常）: %v %s", label, name, checkErr, value)
	}
	switch value {
	case "broken-link":
		return fmt.Errorf("%s %s 的符号链接无法访问目标文件（目标缺失或权限不足）", label, name)
	case "not-file":
		return fmt.Errorf("%s %s 不是普通文件", label, name)
	}
	size, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fmt.Errorf("%s %s 的大小检查返回无效结果: %s", label, name, value)
	}
	if size <= 0 {
		return fmt.Errorf("%s %s 为空", label, name)
	}
	if size != expected {
		return fmt.Errorf("%s %s 大小不一致：预期 %d 字节，实际 %d 字节", label, name, expected, size)
	}
	return nil
}
