package source

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"center-cv-manager/internal/domain"
)

type checkoutRepository struct{ checkout domain.Checkout }

func (r *checkoutRepository) ListSources() ([]domain.Source, error) { return nil, nil }
func (r *checkoutRepository) GetSource(string) (domain.Source, error) {
	return domain.Source{}, errors.New("not implemented")
}
func (r *checkoutRepository) SaveSource(domain.Source) error { return nil }
func (r *checkoutRepository) DeleteSource(string) error      { return nil }
func (r *checkoutRepository) GetGitCredential() (domain.GitCredential, error) {
	return domain.GitCredential{}, nil
}
func (r *checkoutRepository) SaveGitCredential(domain.GitCredential) error { return nil }
func (r *checkoutRepository) SaveCheckout(checkout domain.Checkout) error {
	r.checkout = checkout
	return nil
}
func (r *checkoutRepository) GetCheckout(sourceID, ref string) (domain.Checkout, error) {
	if r.checkout.SourceID == sourceID && r.checkout.Ref == ref {
		return r.checkout, nil
	}
	return domain.Checkout{}, errors.New("not found")
}

type silentReporter struct{}

func (silentReporter) Log(string)       {}
func (silentReporter) Progress(float64) {}
func (silentReporter) Message(string)   {}

func TestValidateRef(t *testing.T) {
	for _, v := range []string{"v1.2.3", "release/2026-09", "feature_x"} {
		if err := ValidateRef(v); err != nil {
			t.Errorf("%s should be valid: %v", v, err)
		}
	}
	for _, v := range []string{"", "--upload-pack=bad", "../main", "main branch"} {
		if ValidateRef(v) == nil {
			t.Errorf("%s should be invalid", v)
		}
	}
}

func TestValidateCommit(t *testing.T) {
	if err := ValidateCommit(strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "main", strings.Repeat("g", 40), strings.Repeat("a", 39)} {
		if ValidateCommit(value) == nil {
			t.Errorf("%q should be invalid", value)
		}
	}
}

func TestEnsureCheckoutPinsCommitAndReusesCache(t *testing.T) {
	repositoryPath := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repositoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repositoryPath}, args...)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	runGit("init")
	runGit("config", "user.name", "Source Test")
	runGit("config", "user.email", "source-test@example.invalid")
	if err := os.WriteFile(filepath.Join(repositoryPath, "README.md"), []byte("pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial")
	runGit("branch", "-M", "main")
	commit := runGit("rev-parse", "HEAD")

	stored := &checkoutRepository{}
	service := New(t.TempDir(), stored, nil)
	src := domain.Source{ID: "source-1", Name: "service", Repository: repositoryPath, CreatedAt: time.Now()}
	checkout, err := service.EnsureCheckout(context.Background(), src, "main", commit, silentReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Commit != commit || filepath.Base(checkout.Path) != commit {
		t.Fatalf("checkout = %#v", checkout)
	}
	if _, err = os.Stat(filepath.Join(checkout.Path, ".git")); !os.IsNotExist(err) {
		t.Fatalf("checkout should not retain .git, err=%v", err)
	}

	// Once the exact commit is cached, release preparation must not depend on
	// the branch still pointing at the same commit.
	if err = os.WriteFile(filepath.Join(repositoryPath, "README.md"), []byte("moved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "move branch")
	reused, err := service.EnsureCheckout(context.Background(), src, "main", commit, silentReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if reused.Path != checkout.Path || reused.Commit != commit {
		t.Fatalf("cache was not reused: %#v", reused)
	}

	// Without that immutable cache, a moved branch must fail instead of
	// silently building a different commit under the selected version.
	fresh := New(t.TempDir(), &checkoutRepository{}, nil)
	if _, err = fresh.EnsureCheckout(context.Background(), src, "main", commit, silentReporter{}); err == nil || !strings.Contains(err.Error(), "在选择后已从") {
		t.Fatalf("expected branch drift error, got %v", err)
	}
}

func TestRepositoryForTokenConvertsSSHURL(t *testing.T) {
	credential := domain.GitCredential{BaseURL: "https://code.jms.com", Token: "secret", Configured: true}
	got, authenticated, err := repositoryForToken("ssh://git@code.jms.com:2222/project/team/repo.git", credential)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://code.jms.com/project/team/repo.git" || !authenticated {
		t.Fatalf("unexpected conversion: %q %v", got, authenticated)
	}
}

func TestRepositoryForTokenRejectsOtherHost(t *testing.T) {
	credential := domain.GitCredential{BaseURL: "https://code.jms.com", Token: "secret", Configured: true}
	if _, _, err := repositoryForToken("https://other.example/repo.git", credential); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repositoryForToken("ssh://git@other.example/repo.git", credential); err == nil {
		t.Fatal("SSH repository on another host should be rejected")
	}
}

func TestParseGitProgress(t *testing.T) {
	tests := []struct {
		line     string
		progress float64
	}{
		{"remote: Counting objects: 100% (20/20), done.", 3},
		{"Receiving objects: 40% (40/100), 12.00 MiB | 2.00 MiB/s", 40},
		{"Resolving deltas: 50% (10/20)", 91},
		{"Updating files: 100% (100/100), done.", 99},
	}
	for _, test := range tests {
		got, _, ok := parseGitProgress(test.line)
		if !ok || got != test.progress {
			t.Errorf("parseGitProgress(%q) = %v, %v; want %v", test.line, got, ok, test.progress)
		}
	}
}

func TestSplitGitOutputHandlesCarriageReturns(t *testing.T) {
	data := []byte("Receiving objects: 1%\rReceiving objects: 2%\r\n")
	advance, token, err := splitGitOutput(data, false)
	if err != nil || advance == 0 || string(token) != "Receiving objects: 1%" {
		t.Fatalf("unexpected split: advance=%d token=%q err=%v", advance, token, err)
	}
}

func TestValidateRepository(t *testing.T) {
	for _, v := range []string{"https://git.example/a.git", "ssh://git@git.example/a.git", "git@git.example:a.git"} {
		if err := ValidateRepository(v); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
	if ValidateRepository("relative/path") == nil {
		t.Fatal("relative repository should be rejected")
	}
}
