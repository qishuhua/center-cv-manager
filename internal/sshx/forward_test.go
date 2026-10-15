package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestRemoteForwardRelaysTraffic 用进程内 SSH 服务器模拟中心 sshd：接受
// tcpip-forward 请求后主动打开 forwarded-tcpip 通道，验证数据经反向隧道到达
// 本机目标地址并原样返回。
func TestRemoteForwardRelaysTraffic(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, acceptErr := echo.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); _, _ = io.Copy(c, c) }(conn)
		}
	}()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)

	sshListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sshListener.Close()
	relayed := make(chan string, 1)
	go func() {
		conn, acceptErr := sshListener.Accept()
		if acceptErr != nil {
			return
		}
		server, _, requests, connErr := ssh.NewServerConn(conn, serverConfig)
		if connErr != nil {
			return
		}
		defer server.Close()
		for request := range requests {
			if request.Type != "tcpip-forward" || !request.WantReply {
				if request.WantReply {
					request.Reply(false, nil)
				}
				continue
			}
			// 选一个空闲端口回给客户端（远端监听地址仅作元数据传递）。
			probe, probeErr := net.Listen("tcp", "127.0.0.1:0")
			if probeErr != nil {
				request.Reply(false, nil)
				continue
			}
			port := probe.Addr().(*net.TCPAddr).Port
			probe.Close()
			request.Reply(true, ssh.Marshal(struct{ Port uint32 }{uint32(port)}))
			channel, channelRequests, openErr := server.OpenChannel("forwarded-tcpip", ssh.Marshal(struct {
				Addr              string
				Port              uint32
				OriginatorAddress string
				OriginatorPort    uint32
			}{"127.0.0.1", uint32(port), "127.0.0.1", 40000}))
			if openErr != nil {
				return
			}
			go ssh.DiscardRequests(channelRequests)
			if _, writeErr := channel.Write([]byte("ping-through-forward")); writeErr != nil {
				return
			}
			go func() {
				buffer := make([]byte, 64)
				n, _ := channel.Read(buffer)
				relayed <- string(buffer[:n])
				channel.Close()
			}()
		}
	}()

	client, err := ssh.Dial("tcp", sshListener.Addr().String(), &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password("test")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	forward, err := RequestRemoteForward(client, 0, echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer forward.Close()
	if forward.Addr() == "" || forward.Addr() == "127.0.0.1:0" {
		t.Fatalf("反向转发地址无效: %q", forward.Addr())
	}

	select {
	case message := <-relayed:
		if message != "ping-through-forward" {
			t.Fatalf("回声内容 = %q", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("反向转发回声超时")
	}
}
