package sshx

import (
	"fmt"
	"io"
	"net"
	"strconv"

	"golang.org/x/crypto/ssh"
)

// RemoteForward 在 SSH 连接的远端（中心服务器）回环地址上开一个临时监听端口，
// 接入的连接全部转发回本机 target。镜像分发用它让中心经管理端发起的 SSH 出站
// 连接访问开发机侧的 registry：会话结束即撤销，中心不落任何持久配置，也不需要
// 防火墙放行或修改 dockerd。
type RemoteForward struct {
	listener net.Listener
	target   string
	closed   chan struct{}
}

// RequestRemoteForward 请求远端监听 listenPort（0 表示由远端自动分配），target
// 是从管理端进程可达的地址（不含 scheme，例如 127.0.0.1:5000）。
func RequestRemoteForward(sshClient *ssh.Client, listenPort int, target string) (*RemoteForward, error) {
	if target == "" {
		return nil, fmt.Errorf("转发目标地址不能为空")
	}
	listener, listenErr := sshClient.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(listenPort)))
	if listenErr != nil && listenPort != 0 {
		// 指定端口被占用或被 sshd 拒绝时退回远端自动分配，避免影响中心已有服务。
		listener, listenErr = sshClient.Listen("tcp", "127.0.0.1:0")
	}
	if listenErr != nil {
		return nil, fmt.Errorf("建立反向隧道失败（确认中心 sshd 开启 AllowTcpForwarding）: %w", listenErr)
	}
	forward := &RemoteForward{listener: listener, target: target, closed: make(chan struct{})}
	go forward.serve()
	return forward, nil
}

// Addr 返回远端监听地址（127.0.0.1:端口），可直接作为镜像引用的 registry 主机部分；
// 127.0.0.0/8 是 dockerd 默认豁免 TLS 的网段，中心侧无需任何 daemon 配置。
func (f *RemoteForward) Addr() string { return f.listener.Addr().String() }

func (f *RemoteForward) serve() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go forwardPipe(conn, f.target)
	}
}

func forwardPipe(conn net.Conn, target string) {
	defer conn.Close()
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer upstream.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, conn); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, upstream); done <- struct{}{} }()
	<-done
}

func (f *RemoteForward) Close() error {
	select {
	case <-f.closed:
		return nil
	default:
		close(f.closed)
	}
	return f.listener.Close()
}
