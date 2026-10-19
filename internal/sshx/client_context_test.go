package sshx

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestDialContextCancelsStalledHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		client, _, err := DialContext(ctx, Config{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, Username: "test", Password: "test"})
		if client != nil {
			client.Close()
		}
		finished <- err
	}()
	select {
	case conn := <-accepted:
		defer conn.Close()
	case <-time.After(time.Second):
		t.Fatal("TCP connection not established")
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("stalled SSH handshake should fail")
		}
	case <-time.After(time.Second):
		t.Fatal("SSH handshake ignored context deadline")
	}
}
