package modelasset

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Use the same identity for preparation, transfer, size checks and publication.
// Creating the root with sudo alone leaves later writes failing as the SSH user.
type remoteFiles struct {
	client   *ssh.Client
	root     string
	sudo     bool
	password string
}

func (f remoteFiles) invocation(command string) (string, io.Reader) {
	if !f.sudo {
		return command, nil
	}
	if f.password != "" {
		return "sudo -k -S -p '' -- sh -c " + shellQuote(command), strings.NewReader(f.password + "\n")
	}
	return "sudo -n -- sh -c " + shellQuote(command), nil
}

func (f remoteFiles) run(ctx context.Context, command string) ([]byte, error) {
	command, input := f.invocation(command)
	return runSSH(ctx, f.client, command, input)
}

func (f remoteFiles) fileSize(ctx context.Context, name string) int64 {
	output, err := f.run(ctx, "stat -c %s "+shellQuote(name))
	if err != nil {
		return -1
	}
	var size int64
	if _, err = fmt.Sscan(strings.TrimSpace(string(output)), &size); err != nil {
		return -1
	}
	return size
}

func prepareRemoteFiles(ctx context.Context, client *ssh.Client, password, root string, r Reporter) (remoteFiles, error) {
	f := remoteFiles{client: client, root: root}
	command := modelPathGuard(root, "permission-probe") + " && mkdir -p " + shellQuote(root) + " " + shellQuote(root+"/.center-manager") + " && test -w " + shellQuote(root) + " && test -w " + shellQuote(root+"/.center-manager")
	if _, err := f.run(ctx, command); err == nil {
		return f, nil
	}
	if ctx.Err() != nil {
		return f, ctx.Err()
	}
	r.Logf("SSH 用户无法写入 %s，正在检查 sudo 权限", root)
	f.sudo = true
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := f.run(probeCtx, "true"); err != nil {
		if password == "" || strings.ContainsAny(password, "\r\n") {
			return f, fmt.Errorf("SSH 用户没有 %s 写权限，也不能免密 sudo；请为该用户配置模型目录写权限或 sudo 权限后重试", root)
		}
		f.password = password
		if _, err = f.run(probeCtx, "true"); err != nil {
			return f, fmt.Errorf("SSH 用户没有 %s 写权限，使用已保存的 SSH 密码验证 sudo 也失败；请检查该用户的 sudo 权限或模型目录权限", root)
		}
	}
	if _, err := f.run(ctx, command); err != nil {
		return f, fmt.Errorf("sudo 已验证，但创建或写入模型目录 %s 失败（请检查只读挂载或磁盘权限）: %w", root, err)
	}
	r.Logf("已使用 sudo 准备 %s，本次模型文件操作将使用相同权限执行", root)
	return f, nil
}

func modelPathGuard(root, name string) string {
	if !safeRelativePath(name) {
		return "exit 1"
	}
	paths := []string{
		path.Join(root, name),
		path.Join(root, ".center-manager", name+".oss-etag"),
		path.Join(root, ".center-manager", name+".oss-etag.part"),
		path.Join(root, ".center-manager", "downloads", name+".part"),
	}
	seen := map[string]bool{}
	var checks []string
	for _, current := range paths {
		for current != "/" {
			if !seen[current] {
				checks = append(checks, "test ! -L "+shellQuote(current))
				seen[current] = true
			}
			current = path.Dir(current)
		}
	}
	return strings.Join(checks, " && ")
}
