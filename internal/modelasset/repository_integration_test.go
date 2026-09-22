package modelasset

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	aliyunoss "github.com/aliyun/aliyun-oss-go-sdk/oss"
	"golang.org/x/crypto/ssh"
)

// Execute the real remote shell commands over SSH in a test-only directory.
// curl is stubbed, so tests never use OSS or the network
// outside the local SSH transport.
func repositoryTestSSH(t *testing.T, root string) *ssh.Client {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
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
	bin := filepath.Join(root, "test-bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	stub := `#!/bin/sh
out=''; last=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = '-o' ]; then shift; out="$1"; fi
  last="$1"; shift
done
case "$last" in
  */model.engine) printf weight > "$out" ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(stub), 0755); err != nil {
		t.Fatal(err)
	}
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
		for incoming := range channels {
			stream, requests, err := incoming.Accept()
			if err != nil {
				continue
			}
			go func() {
				defer stream.Close()
				for request := range requests {
					if request.Type != "exec" {
						request.Reply(false, nil)
						continue
					}
					var payload struct{ Command string }
					ssh.Unmarshal(request.Payload, &payload)
					request.Reply(true, nil)
					command := exec.Command("sh", "-c", payload.Command)
					command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
					command.Stdout, command.Stderr = stream, stream.Stderr()
					status := uint32(0)
					if command.Run() != nil {
						status = 1
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

type testArtifactSigner struct{ calls int }

func (s *testArtifactSigner) SignURL(key string, _ aliyunoss.HTTPMethod, _ int64, _ ...aliyunoss.Option) (string, error) {
	s.calls++
	return "https://test.invalid/" + key, nil
}

func TestRepositoryReleaseDownloadCacheAndFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux remote commands tested in build container")
	}
	root := t.TempDir()
	files := remoteFiles{client: repositoryTestSSH(t, root), root: root}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	digest := func(content string) string { sum := sha256.Sum256([]byte(content)); return hex.EncodeToString(sum[:]) }
	item := ReleaseItem{SchemaVersion: manifestSchemaVersion, Model: "denglin", Version: "v1", FormatID: "denglin", Platform: "denglin", ManifestKey: "center-models/denglin/v1/manifest.json", ManifestDigest: strings.Repeat("a", 64), RepositoryPath: "denglin/denglin", Artifacts: []ManifestArtifact{
		{Path: "artifact/denglin/model.engine", TargetPath: "denglin/denglin/model.engine", Size: 6, SHA256: digest("weight")},
	}}
	signer := &testArtifactSigner{}
	release := func(current CurrentVersion) error {
		return releaseRepository(ctx, files, signer, item, current, 0, 6, quietReporter{})
	}
	if err := release(CurrentVersion{}); err != nil {
		t.Fatal(err)
	}
	if signer.calls != 1 {
		t.Fatalf("expected 1 download, got %d", signer.calls)
	}
	alias := filepath.Join(root, "denglin", "denglin.engine")
	if _, err := os.Stat(alias); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, item.RepositoryPath)); !os.IsNotExist(err) {
		t.Fatalf("public nested model directory still exists: %v", err)
	}
	statePath := filepath.Join(root, ".center-manager/releases/denglin.json")
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var current CurrentVersion
	if err = json.Unmarshal(stateData, &current); err != nil {
		t.Fatal(err)
	}
	if current.TargetPath != "denglin/denglin.engine" || current.StoragePath == "" {
		t.Fatalf("unexpected flat release state: %#v", current)
	}
	if err := release(current); err != nil {
		t.Fatal(err)
	}
	if signer.calls != 1 {
		t.Fatal("unchanged repository was downloaded again")
	}
	item.Version, item.ManifestDigest = "v2", strings.Repeat("b", 64)
	if err := release(current); err != nil {
		t.Fatal(err)
	}
	if signer.calls != 1 {
		t.Fatal("new release with identical files did not reuse cache")
	}
	oldLink, err := os.Readlink(alias)
	if err != nil {
		t.Fatal(err)
	}
	oldState, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	item.Version, item.ManifestDigest = "bad", strings.Repeat("c", 64)
	item.Artifacts[0].SHA256 = digest("broken") // Same length, invalid content from download.
	if err := release(CurrentVersion{}); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("expected checksum failure, got %v", err)
	}
	newLink, _ := os.Readlink(alias)
	newState, _ := os.ReadFile(statePath)
	if oldLink != newLink || string(oldState) != string(newState) {
		t.Fatal("failed download changed active repository or state")
	}
	var active CurrentVersion
	if err = json.Unmarshal(oldState, &active); err != nil {
		t.Fatal(err)
	}
	activeItem := releaseItemFromCurrent(active)
	if !currentVersionFilesReady(ctx, files, active, activeItem, false) {
		t.Fatal("flat model health check failed")
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(active.StoragePath))); err != nil {
		t.Fatal(err)
	}
	if currentVersionFilesReady(ctx, files, active, activeItem, false) {
		t.Fatal("missing model not detected")
	}
	t.Log(fmt.Sprintf("verified %d downloads, cache reuse, hash failure protection and health checks", signer.calls))
}
