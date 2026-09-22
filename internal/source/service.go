package source

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/secret"
	"github.com/google/uuid"
)

type Version struct {
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
	Kind   string `json:"kind"`
}
type Reporter interface {
	Log(string)
	Progress(float64)
	Message(string)
}

const Mask = "********"

type Repository interface {
	ListSources() ([]domain.Source, error)
	GetSource(string) (domain.Source, error)
	SaveSource(domain.Source) error
	DeleteSource(string) error
	GetGitCredential() (domain.GitCredential, error)
	SaveGitCredential(domain.GitCredential) error
	SaveCheckout(domain.Checkout) error
	GetCheckout(string, string) (domain.Checkout, error)
}
type Service struct {
	root        string
	store       Repository
	cipher      *secret.Cipher
	locksMu     sync.Mutex
	sourceLocks map[string]*sync.Mutex
}

func New(root string, store Repository, cipher *secret.Cipher) *Service {
	return &Service{root: root, store: store, cipher: cipher, sourceLocks: map[string]*sync.Mutex{}}
}

var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@+-]{0,199}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)
var gitPercentPattern = regexp.MustCompile(`(?i)(Compressing objects|Receiving objects|Resolving deltas|Updating files|Checking out files):\s*(\d+)%`)

func ValidateRepository(v string) error {
	if strings.ContainsAny(v, "\r\n\x00") {
		return fmt.Errorf("仓库地址无效")
	}
	if strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "ssh://") || strings.HasPrefix(v, "git@") || filepath.IsAbs(v) {
		return nil
	}
	return fmt.Errorf("仓库地址必须是 http(s)、ssh、git@ 或绝对本地路径")
}
func ValidateRef(v string) error {
	if !refPattern.MatchString(v) || strings.HasPrefix(v, "-") {
		return fmt.Errorf("版本名称无效")
	}
	return nil
}

func ValidateCommit(v string) error {
	if !commitPattern.MatchString(v) {
		return fmt.Errorf("commit 无效")
	}
	return nil
}

func (s *Service) List() ([]domain.Source, error) {
	return s.store.ListSources()
}

func (s *Service) Get(id string) (domain.Source, error) {
	return s.store.GetSource(id)
}

func (s *Service) Save(input domain.Source) (domain.Source, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Repository = strings.TrimSpace(input.Repository)
	if input.Name == "" {
		return domain.Source{}, fmt.Errorf("名称不能为空")
	}
	if err := ValidateRepository(input.Repository); err != nil {
		return domain.Source{}, err
	}
	now := time.Now()
	if input.ID == "" {
		input.ID = uuid.NewString()
		input.CreatedAt = now
	} else if stored, err := s.store.GetSource(input.ID); err == nil {
		input.CreatedAt = stored.CreatedAt
	} else {
		return domain.Source{}, err
	}
	input.UpdatedAt = now
	if err := s.store.SaveSource(input); err != nil {
		return domain.Source{}, err
	}
	return input, nil
}

func (s *Service) Delete(id string) error { return s.store.DeleteSource(id) }

func (s *Service) Credential() (domain.GitCredential, error) {
	v, err := s.credential()
	if err == nil && v.Token != "" {
		v.Token = Mask
		v.Configured = true
	}
	return v, err
}

func (s *Service) SaveCredential(input domain.GitCredential) (domain.GitCredential, error) {
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	input.Username = strings.TrimSpace(input.Username)
	parsed, err := url.Parse(input.BaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return domain.GitCredential{}, fmt.Errorf("Git 服务地址必须是完整的 http(s) 地址")
	}
	if input.Username == "" {
		input.Username = "oauth2"
	}
	old, err := s.store.GetGitCredential()
	if err != nil {
		return domain.GitCredential{}, err
	}
	if input.Token == "" || input.Token == Mask {
		input.Token = old.Token
	} else {
		input.Token, err = s.cipher.Encrypt(input.Token)
		if err != nil {
			return domain.GitCredential{}, err
		}
	}
	if input.Token == "" {
		return domain.GitCredential{}, fmt.Errorf("访问令牌不能为空")
	}
	input.Configured = true
	input.UpdatedAt = time.Now()
	if err = s.store.SaveGitCredential(input); err != nil {
		return domain.GitCredential{}, err
	}
	input.Token = Mask
	return input, nil
}

func (s *Service) credential() (domain.GitCredential, error) {
	v, err := s.store.GetGitCredential()
	if err != nil || v.Token == "" {
		return v, err
	}
	v.Token, err = s.cipher.Decrypt(v.Token)
	if err != nil {
		return domain.GitCredential{}, fmt.Errorf("解密 Git 访问令牌失败: %w", err)
	}
	v.Configured = true
	return v, nil
}

func (s *Service) Versions(ctx context.Context, src domain.Source) ([]Version, error) {
	if err := ValidateRepository(src.Repository); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", "--tags", src.Repository)
	cleanup, err := s.prepareGit(cmd, src)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, gitFailure("读取 Git 版本失败", out, err)
	}
	seen := map[string]Version{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		ref := f[1]
		kind := "branch"
		if strings.HasPrefix(ref, "refs/heads/") {
			ref = strings.TrimPrefix(ref, "refs/heads/")
		} else if strings.HasPrefix(ref, "refs/tags/") {
			kind = "tag"
			ref = strings.TrimPrefix(ref, "refs/tags/")
			ref = strings.TrimSuffix(ref, "^{}")
		} else {
			continue
		}
		seen[kind+":"+ref] = Version{Ref: ref, Commit: f[0], Kind: kind}
	}
	versions := make([]Version, 0, len(seen))
	for _, v := range seen {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].Kind != versions[j].Kind {
			return versions[i].Kind < versions[j].Kind
		}
		return versions[i].Ref > versions[j].Ref
	})
	return versions, nil
}

func (s *Service) Download(ctx context.Context, src domain.Source, ref string, r Reporter) (domain.Checkout, error) {
	return s.download(ctx, src, ref, "", r)
}

// EnsureCheckout returns an exact, immutable source snapshot for a release.
// The expected commit comes from ls-remote and prevents a moving branch from
// silently changing between version selection and the actual build.
func (s *Service) EnsureCheckout(ctx context.Context, src domain.Source, ref, expectedCommit string, r Reporter) (domain.Checkout, error) {
	if err := ValidateCommit(expectedCommit); err != nil {
		return domain.Checkout{}, err
	}
	return s.download(ctx, src, ref, strings.ToLower(expectedCommit), r)
}

func (s *Service) sourceLock(sourceID string) *sync.Mutex {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	lock := s.sourceLocks[sourceID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.sourceLocks[sourceID] = lock
	}
	return lock
}

func (s *Service) download(ctx context.Context, src domain.Source, ref, expectedCommit string, r Reporter) (domain.Checkout, error) {
	if err := ValidateRepository(src.Repository); err != nil {
		return domain.Checkout{}, err
	}
	if err := ValidateRef(ref); err != nil {
		return domain.Checkout{}, err
	}
	lock := s.sourceLock(src.ID)
	lock.Lock()
	defer lock.Unlock()

	base := filepath.Join(s.root, src.ID)
	if err := os.MkdirAll(base, 0700); err != nil {
		return domain.Checkout{}, err
	}
	if expectedCommit != "" {
		if checkout, err := s.store.GetCheckout(src.ID, ref); err == nil && strings.EqualFold(checkout.Commit, expectedCommit) {
			if info, statErr := os.Stat(checkout.Path); statErr == nil && info.IsDir() {
				r.Message("源码缓存已就绪")
				r.Log("复用源码缓存，提交 " + checkout.Commit)
				r.Progress(100)
				return checkout, nil
			}
		}
		target := filepath.Join(base, expectedCommit)
		if info, err := os.Stat(target); err == nil && info.IsDir() {
			checkout := domain.Checkout{SourceID: src.ID, Ref: ref, Commit: expectedCommit, Path: target, CreatedAt: time.Now()}
			if err = s.store.SaveCheckout(checkout); err != nil {
				return domain.Checkout{}, err
			}
			r.Message("源码缓存已就绪")
			r.Log("复用源码缓存，提交 " + checkout.Commit)
			r.Progress(100)
			return checkout, nil
		}
	}
	tmp, err := os.MkdirTemp(base, "download-")
	if err != nil {
		return domain.Checkout{}, err
	}
	defer os.RemoveAll(tmp)
	r.Message("正在下载源码")
	r.Progress(1)
	r.Log("从 " + src.Repository + " 下载 " + ref)
	cmd := exec.CommandContext(ctx, "git", "clone", "--progress", "--depth", "1", "--branch", ref, "--single-branch", src.Repository, tmp)
	cleanup, err := s.prepareGit(cmd, src)
	if err != nil {
		return domain.Checkout{}, err
	}
	defer cleanup()
	pipe, err := cmd.StderrPipe()
	if err != nil {
		return domain.Checkout{}, err
	}
	if err = cmd.Start(); err != nil {
		return domain.Checkout{}, err
	}
	var gitOutput strings.Builder
	scanner := bufio.NewScanner(pipe)
	scanner.Split(splitGitOutput)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lastProgress := float64(1)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		gitOutput.WriteString(line + "\n")
		if progress, message, ok := parseGitProgress(line); ok {
			if progress > lastProgress {
				lastProgress = progress
				r.Progress(progress)
				r.Message(message)
				r.Log(message)
			}
		} else {
			r.Log(line)
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return domain.Checkout{}, fmt.Errorf("读取 Git 下载进度失败: %w", scanErr)
	}
	if err = cmd.Wait(); err != nil {
		return domain.Checkout{}, gitFailure("git clone 失败", []byte(gitOutput.String()), err)
	}
	commitOut, err := exec.CommandContext(ctx, "git", "-C", tmp, "rev-parse", "HEAD").Output()
	if err != nil {
		return domain.Checkout{}, fmt.Errorf("读取提交号失败: %w", err)
	}
	commit := strings.ToLower(strings.TrimSpace(string(commitOut)))
	if expectedCommit != "" && commit != expectedCommit {
		return domain.Checkout{}, fmt.Errorf("%s 在选择后已从 %s 变为 %s，请刷新版本后重新发布", ref, expectedCommit, commit)
	}
	if err = ValidateCommit(commit); err != nil {
		return domain.Checkout{}, fmt.Errorf("Git 返回了无效的 commit: %w", err)
	}
	_ = os.RemoveAll(filepath.Join(tmp, ".git"))
	target := filepath.Join(base, commit)
	if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
		if err = os.RemoveAll(tmp); err != nil {
			return domain.Checkout{}, err
		}
	} else if err = os.Rename(tmp, target); err != nil {
		return domain.Checkout{}, err
	}
	checkout := domain.Checkout{SourceID: src.ID, Ref: ref, Commit: commit, Path: target, CreatedAt: time.Now()}
	if err = s.store.SaveCheckout(checkout); err != nil {
		return domain.Checkout{}, err
	}
	r.Progress(100)
	r.Message("源码下载完成")
	r.Log("源码已保存，提交 " + checkout.Commit)
	return checkout, nil
}

func splitGitOutput(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\r' || b == '\n' {
			if i == 0 {
				return 1, nil, nil
			}
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func parseGitProgress(line string) (float64, string, bool) {
	match := gitPercentPattern.FindStringSubmatch(line)
	if len(match) == 3 {
		percent, err := strconv.Atoi(match[2])
		if err != nil {
			return 0, "", false
		}
		stage := strings.ToLower(match[1])
		switch stage {
		case "compressing objects":
			return 5 + float64(percent)*0.05, fmt.Sprintf("远端正在压缩源码 %d%%", percent), true
		case "receiving objects":
			return 10 + float64(percent)*0.75, fmt.Sprintf("正在接收源码 %d%%", percent), true
		case "resolving deltas":
			return 85 + float64(percent)*0.12, fmt.Sprintf("正在处理增量 %d%%", percent), true
		default:
			return 97 + float64(percent)*0.02, fmt.Sprintf("正在写入工作区 %d%%", percent), true
		}
	}
	lower := strings.ToLower(line)
	if strings.Contains(lower, "enumerating objects") || strings.Contains(lower, "counting objects") {
		return 3, "远端正在准备源码", true
	}
	if strings.Contains(lower, "cloning into") {
		return 2, "正在连接 Git 仓库", true
	}
	return 0, "", false
}

func (s *Service) prepareGit(cmd *exec.Cmd, src domain.Source) (func(), error) {
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	credential, err := s.credential()
	if err != nil {
		return nil, err
	}
	repository, authenticated, err := repositoryForToken(src.Repository, credential)
	if err != nil {
		return nil, err
	}
	for i := range cmd.Args {
		if cmd.Args[i] == src.Repository {
			cmd.Args[i] = repository
		}
	}
	if !authenticated {
		return func() {}, nil
	}
	if err = os.MkdirAll(s.root, 0700); err != nil {
		return nil, fmt.Errorf("创建 Git 凭据目录失败: %w", err)
	}
	tempDir, err := os.MkdirTemp(s.root, ".git-token-")
	if err != nil {
		return nil, fmt.Errorf("创建 Git 临时凭据失败: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tempDir) }
	askpass := filepath.Join(tempDir, "askpass.sh")
	script := "#!/bin/sh\ncase \"$1\" in\n  *\"$GIT_HTTP_HOST\"*) case \"$1\" in\n    *sername*) printf '%s\\n' \"$GIT_HTTP_USERNAME\" ;;\n    *) printf '%s\\n' \"$GIT_HTTP_TOKEN\" ;;\n  esac ;;\n  *) exit 1 ;;\nesac\n"
	if err = os.WriteFile(askpass, []byte(script), 0700); err != nil {
		cleanup()
		return nil, fmt.Errorf("创建 Git 令牌助手失败: %w", err)
	}
	base, _ := url.Parse(credential.BaseURL)
	cmd.Env = append(cmd.Env, "GIT_ASKPASS="+askpass, "GIT_HTTP_HOST="+base.Hostname(), "GIT_HTTP_USERNAME="+credential.Username, "GIT_HTTP_TOKEN="+credential.Token)
	return cleanup, nil
}

func repositoryForToken(repository string, credential domain.GitCredential) (string, bool, error) {
	if !credential.Configured || credential.Token == "" {
		if strings.HasPrefix(repository, "ssh://") || strings.HasPrefix(repository, "git@") {
			return "", false, fmt.Errorf("SSH 仓库需要先配置全局 Git 服务地址和访问令牌")
		}
		return repository, false, nil
	}
	base, err := url.Parse(credential.BaseURL)
	if err != nil || base.Hostname() == "" {
		return "", false, fmt.Errorf("全局 Git 服务地址无效")
	}
	if strings.HasPrefix(repository, "http://") || strings.HasPrefix(repository, "https://") {
		target, parseErr := url.Parse(repository)
		if parseErr != nil {
			return "", false, fmt.Errorf("仓库地址无效: %w", parseErr)
		}
		return repository, strings.EqualFold(target.Hostname(), base.Hostname()), nil
	}
	var host, repoPath string
	if strings.HasPrefix(repository, "ssh://") {
		sshURL, parseErr := url.Parse(repository)
		if parseErr != nil {
			return "", false, fmt.Errorf("SSH 仓库地址无效: %w", parseErr)
		}
		host, repoPath = sshURL.Hostname(), sshURL.Path
	} else if strings.HasPrefix(repository, "git@") {
		parts := strings.SplitN(strings.TrimPrefix(repository, "git@"), ":", 2)
		if len(parts) != 2 {
			return "", false, fmt.Errorf("SSH 仓库地址无效")
		}
		host, repoPath = parts[0], parts[1]
	} else {
		return repository, false, nil
	}
	if !strings.EqualFold(host, base.Hostname()) {
		return "", false, fmt.Errorf("仓库主机 %s 与全局 Git 服务 %s 不一致", host, base.Hostname())
	}
	converted := strings.TrimRight(credential.BaseURL, "/") + "/" + strings.TrimLeft(repoPath, "/")
	return converted, true, nil
}

func gitFailure(action string, output []byte, err error) error {
	detail := strings.TrimSpace(string(output))
	if len(detail) > 2000 {
		detail = detail[len(detail)-2000:]
	}
	if detail == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %s", action, detail)
}
