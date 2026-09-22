package modelasset

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

type quietReporter struct{}

func (quietReporter) Log(string)          {}
func (quietReporter) Logf(string, ...any) {}
func (quietReporter) Message(string)      {}
func (quietReporter) Progress(float64)    {}

// A real SSH transport checks that sudo credentials are delivered via stdin,
// including subsequent file operations on a fresh session.
func permissionSSH(t *testing.T, mode string) *ssh.Client {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		server, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			conn.Close()
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		for channel := range channels {
			stream, requests, err := channel.Accept()
			if err != nil {
				continue
			}
			go func() {
				defer stream.Close()
				for req := range requests {
					if req.Type != "exec" {
						req.Reply(false, nil)
						continue
					}
					var payload struct{ Command string }
					ssh.Unmarshal(req.Payload, &payload)
					req.Reply(true, nil)
					allowed := mode == "direct" && !strings.HasPrefix(payload.Command, "sudo ") || mode == "nopass" && strings.HasPrefix(payload.Command, "sudo -n ")
					if strings.HasPrefix(payload.Command, "sudo -k -S ") {
						input, _ := io.ReadAll(stream)
						allowed = mode == "password" && string(input) == "secret-pass\n" && !strings.Contains(payload.Command, "secret-pass")
					}
					status := uint32(1)
					if allowed {
						status = 0
						if strings.Contains(payload.Command, "stat -c") {
							io.WriteString(stream, "123\n")
						}
					}
					stream.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
					return
				}
			}()
		}
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestPermissionFallbackAndSubsequentOperations(t *testing.T) {
	for _, mode := range []string{"direct", "nopass", "password", "denied"} {
		t.Run(mode, func(t *testing.T) {
			client := permissionSSH(t, mode)
			files, err := prepareRemoteFiles(context.Background(), client, "secret-pass", "/home/test/.local/share/center-manager/data/models", quietReporter{})
			if mode == "denied" {
				if err == nil {
					t.Fatal("must reject insufficient sudo rights")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if files.sudo != (mode != "direct") {
				t.Fatal("incorrect execution identity")
			}
			if size := files.fileSize(context.Background(), "/data/models/a"); size != 123 {
				t.Fatalf("stat lost execution identity: %d", size)
			}
			if err := runDownload(context.Background(), files, "download", "signed-url", "/data/models/a", "a", 0, 123, quietReporter{}); err != nil {
				t.Fatalf("download lost execution identity: %v", err)
			}
		})
	}
}

func TestMissingFileSizeIsNotAnEmptyFile(t *testing.T) {
	files := remoteFiles{client: permissionSSH(t, "denied")}
	if size := files.fileSize(context.Background(), "/data/models/absent"); size != -1 {
		t.Fatalf("missing file reported as %d", size)
	}
}

func TestModelPathsRejectReservedAndAmbiguousNames(t *testing.T) {
	for _, name := range []string{".center-manager/a", " ../outside", "dir/../a", "a\x00b", "a\nb", " a"} {
		if safeRelativePath(name) {
			t.Fatalf("accepted unsafe key %q", name)
		}
	}
	guard := modelPathGuard("/data/models", "v1/a.bin")
	for _, directory := range []string{"/data/models", "/data/models/v1", "/data/models/.center-manager/downloads/v1/a.bin.part"} {
		if !strings.Contains(guard, "test ! -L "+shellQuote(directory)) {
			t.Fatalf("missing symlink guard for %s", directory)
		}
	}
}
