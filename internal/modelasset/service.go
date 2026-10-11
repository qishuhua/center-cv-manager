package modelasset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"center-cv-manager/internal/center"
	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/centerpath"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/secret"
	"center-cv-manager/internal/sshx"
	aliyunoss "github.com/aliyun/aliyun-oss-go-sdk/oss"
	"golang.org/x/crypto/ssh"
)

var ErrNotConfigured = errors.New("尚未配置 OSS 模型仓库")

type Repository interface {
	GetOSSCredential() (domain.OSSCredential, error)
}

type Reporter interface {
	Log(string)
	Logf(string, ...any)
	Progress(float64)
	Message(string)
}

type Object struct {
	Key          string    `json:"key"`
	Name         string    `json:"name"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag"`
	LastModified time.Time `json:"last_modified"`
}

type Service struct {
	store    Repository
	cipher   *secret.Cipher
	centers  *center.Service
	uploadMu sync.Mutex
}

func New(store Repository, cipher *secret.Cipher, centers *center.Service) *Service {
	return &Service{store: store, cipher: cipher, centers: centers}
}

func (s *Service) Catalog() ([]Object, error) {
	_, bucket, config, err := s.client()
	if err != nil {
		return nil, err
	}
	objects := []Object{}
	marker := ""
	for {
		result, listErr := bucket.ListObjects(aliyunoss.Prefix(config.ModelsPrefix), aliyunoss.Marker(marker), aliyunoss.MaxKeys(1000))
		if listErr != nil {
			return nil, fmt.Errorf("列举 OSS 模型失败: %w", listErr)
		}
		for _, item := range result.Objects {
			relative := strings.TrimPrefix(item.Key, config.ModelsPrefix)
			if !safeRelativePath(relative) || strings.HasSuffix(relative, "/") {
				continue
			}
			objects = append(objects, Object{
				Key:          item.Key,
				Name:         relative,
				Size:         item.Size,
				ETag:         normalizeETag(item.ETag),
				LastModified: item.LastModified,
			})
		}
		if !result.IsTruncated {
			break
		}
		marker = result.NextMarker
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Name < objects[j].Name })
	return objects, nil
}

func (s *Service) SyncCenter(ctx context.Context, centerID string, keys []string, r Reporter) error {
	release, cancelErr := centerlock.AcquireContext(ctx, centerID)
	if cancelErr != nil {
		return cancelErr
	}
	defer release()
	_, bucket, _, err := s.client()
	if err != nil {
		return err
	}
	catalog, err := s.Catalog()
	if err != nil {
		return err
	}
	selected, err := selectObjects(catalog, keys)
	if err != nil {
		return err
	}
	cfg, _, stored, err := s.centers.Connection(centerID)
	if err != nil {
		return err
	}
	client, fingerprint, err := sshx.Dial(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	_ = s.centers.RememberHostKey(stored, fingerprint)
	targetRoot := modular.ModelsRoot(stored)
	files, err := prepareRemoteFiles(ctx, client, cfg.Password, targetRoot, r)
	if err != nil {
		return err
	}
	var totalBytes int64
	for _, object := range selected {
		totalBytes += object.Size
	}
	r.Message("正在同步模型")
	r.Logf("目标目录：%s；已选择 %d 个模型文件，合计 %s", targetRoot, len(selected), humanSize(totalBytes))
	var completedBytes int64
	downloaded := 0
	for index, object := range selected {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		localPath := path.Join(targetRoot, object.Name)
		statePath := path.Join(targetRoot, ".center-manager", object.Name+".oss-etag")
		// Reject symlinks before privileged operations can follow them outside the model tree.
		if _, err = files.run(ctx, modelPathGuard(targetRoot, object.Name)); err != nil {
			return fmt.Errorf("模型路径 %s 包含符号链接或无法安全访问: %w", object.Name, err)
		}
		cachedCommand := "[ -f " + shellQuote(localPath) + " ] && [ \"$(stat -c %s " + shellQuote(localPath) + " 2>/dev/null)\" = " + shellQuote(strconv.FormatInt(object.Size, 10)) + " ] && [ \"$(cat " + shellQuote(statePath) + " 2>/dev/null)\" = " + shellQuote(object.ETag) + " ]"
		if _, cachedErr := files.run(ctx, cachedCommand); cachedErr == nil {
			r.Logf("[%d/%d] 内容未变化，跳过：%s", index+1, len(selected), object.Name)
			completedBytes += object.Size
			progress(r, completedBytes, totalBytes)
			continue
		}
		signedURL, signErr := bucket.SignURL(object.Key, aliyunoss.HTTPGet, 2*60*60)
		if signErr != nil {
			return fmt.Errorf("生成 %s 下载地址失败: %w", object.Name, signErr)
		}
		partPath := path.Join(targetRoot, ".center-manager", "downloads", object.Name+".part")
		partStatePath := statePath + ".part"
		prepareCommand := "umask 022; mkdir -p " + shellQuote(path.Dir(localPath)) + " " + shellQuote(path.Dir(statePath)) + " " + shellQuote(path.Dir(partPath)) + " && if [ \"$(cat " + shellQuote(partStatePath) + " 2>/dev/null)\" != " + shellQuote(object.ETag) + " ]; then rm -f " + shellQuote(partPath) + "; fi && printf %s " + shellQuote(object.ETag) + " > " + shellQuote(partStatePath)
		if output, prepareErr := files.run(ctx, prepareCommand); prepareErr != nil {
			return fmt.Errorf("准备 %s 断点文件失败: %s %w", object.Name, sanitizeOutput(string(output), signedURL), prepareErr)
		}
		currentSize := files.fileSize(ctx, partPath)
		if currentSize > object.Size {
			if _, removeErr := files.run(ctx, "rm -f "+shellQuote(partPath)); removeErr != nil {
				return fmt.Errorf("清理尺寸异常的 %s 断点文件失败: %w", object.Name, removeErr)
			}
			currentSize = -1
		}
		r.Logf("[%d/%d] 下载 %s（%s）", index+1, len(selected), object.Name, humanSize(object.Size))
		if currentSize < object.Size {
			if err = runDownload(ctx, files, downloadCommand(partPath, signedURL), signedURL, partPath, object.Name, completedBytes, totalBytes, r); err != nil {
				actualSize := files.fileSize(ctx, partPath)
				return fmt.Errorf("下载 %s 失败（OSS %s，临时文件 %s）: %w", object.Name, humanSize(object.Size), humanSize(actualSize), err)
			}
		}
		actualSize := files.fileSize(ctx, partPath)
		if actualSize != object.Size {
			return fmt.Errorf("下载 %s 后尺寸不一致：OSS %d 字节，中心临时文件 %d 字节；断点文件已保留", object.Name, object.Size, actualSize)
		}
		finalizeCommand := "chmod 0644 " + shellQuote(partPath) + " && mv -T " + shellQuote(partPath) + " " + shellQuote(localPath) + " && printf %s " + shellQuote(object.ETag) + " > " + shellQuote(statePath) + " && rm -f " + shellQuote(partStatePath)
		if output, finalizeErr := files.run(ctx, finalizeCommand); finalizeErr != nil {
			return fmt.Errorf("保存 %s 下载结果失败: %s %w", object.Name, sanitizeOutput(string(output), signedURL), finalizeErr)
		}
		downloaded++
		completedBytes += object.Size
		progress(r, completedBytes, totalBytes)
	}
	if downloaded == 0 {
		r.Log("所选模型在中心均为最新版本，没有重复下载")
	} else {
		r.Logf("已下载或更新 %d 个模型文件", downloaded)
	}
	r.Progress(100)
	r.Message("模型同步完成")
	return nil
}

func (s *Service) client() (*aliyunoss.Client, *aliyunoss.Bucket, domain.OSSCredential, error) {
	config, err := s.store.GetOSSCredential()
	if err != nil {
		return nil, nil, domain.OSSCredential{}, err
	}
	if config.ModelsPrefix == "" {
		config.ModelsPrefix = "center-models/"
	}
	if config.Endpoint == "" || config.Bucket == "" || config.AccessKeyID == "" || config.AccessKeySecret == "" {
		return nil, nil, domain.OSSCredential{}, ErrNotConfigured
	}
	secretValue, err := s.cipher.Decrypt(config.AccessKeySecret)
	if err != nil {
		return nil, nil, domain.OSSCredential{}, fmt.Errorf("解密 OSS 凭据失败: %w", err)
	}
	client, err := aliyunoss.New(config.Endpoint, config.AccessKeyID, secretValue)
	if err != nil {
		return nil, nil, domain.OSSCredential{}, err
	}
	bucket, err := client.Bucket(config.Bucket)
	return client, bucket, config, err
}

func selectObjects(catalog []Object, keys []string) ([]Object, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("请至少选择一个模型文件")
	}
	byKey := make(map[string]Object, len(catalog))
	for _, object := range catalog {
		byKey[object.Key] = object
	}
	selected := make([]Object, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if seen[key] {
			continue
		}
		object, found := byKey[key]
		if !found {
			return nil, fmt.Errorf("模型文件不存在或已从 OSS 移除: %s", key)
		}
		seen[key] = true
		selected = append(selected, object)
	}
	return selected, nil
}

func safeRelativePath(value string) bool {
	if value != strings.TrimSpace(value) || strings.ContainsAny(value, "\\\r\n\x00") || value == ".center-manager" || strings.HasPrefix(value, ".center-manager/") {
		return false
	}
	return value != "" && !strings.HasPrefix(value, "/") && value != "." && value != ".." && !strings.HasPrefix(value, "../") && path.Clean(value) == value
}

func progress(r Reporter, completed, total int64) {
	if total > 0 {
		r.Progress(float64(completed) * 95 / float64(total))
	}
}

func runDownload(ctx context.Context, files remoteFiles, command, secretURL, partPath, name string, baseBytes, totalBytes int64, reporter Reporter) error {
	session, err := files.client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout, session.Stderr = &stdout, &stderr
	command, session.Stdin = files.invocation("umask 022; " + command)
	if err = session.Start(command); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = session.Close()
			return ctx.Err()
		case err = <-done:
			if err != nil {
				message := sanitizeOutput(tail(stdout.String()+"\n"+stderr.String(), 2000), secretURL)
				if message == "" {
					return fmt.Errorf("远端下载命令异常退出: %w", err)
				}
				return fmt.Errorf("%s: %w", message, err)
			}
			return nil
		case <-ticker.C:
			current := files.fileSize(ctx, partPath)
			if current < 0 {
				continue
			}
			progress(reporter, baseBytes+current, totalBytes)
			reporter.Message(fmt.Sprintf("正在下载模型 %s（%s / %s）", name, humanSize(baseBytes+current), humanSize(totalBytes)))
		}
	}
}

func runSSH(ctx context.Context, client *ssh.Client, command string, stdin io.Reader) ([]byte, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout, session.Stderr = &stdout, &stderr
	if stdin != nil {
		session.Stdin = stdin
	}
	if err = session.Start(command); err != nil {
		return combineOutput(stdout.Bytes(), stderr.Bytes()), err
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case <-ctx.Done():
		_ = session.Close()
		return combineOutput(stdout.Bytes(), stderr.Bytes()), ctx.Err()
	case err = <-done:
		return combineOutput(stdout.Bytes(), stderr.Bytes()), err
	}
}

func downloadCommand(partPath, signedURL string) string {
	part, url := shellQuote(partPath), shellQuote(signedURL)
	return "if command -v curl >/dev/null 2>&1; then " +
		"if [ -s " + part + " ]; then curl -fsSL --retry 3 -C - -o " + part + " " + url + " || { echo '断点续传失败，改为完整下载' >&2; rm -f " + part + "; curl -fsSL --retry 3 -o " + part + " " + url + "; }; else curl -fsSL --retry 3 -o " + part + " " + url + "; fi; " +
		"elif command -v wget >/dev/null 2>&1; then if [ -s " + part + " ]; then wget -c -O " + part + " " + url + " || { echo '断点续传失败，改为完整下载' >&2; rm -f " + part + "; wget -O " + part + " " + url + "; }; else wget -O " + part + " " + url + "; fi; " +
		"else echo '中心缺少 curl/wget' >&2; exit 127; fi"
}

func combineOutput(stdout, stderr []byte) []byte {
	combined := make([]byte, 0, len(stdout)+len(stderr)+1)
	combined = append(combined, stdout...)
	if len(stdout) > 0 && len(stderr) > 0 {
		combined = append(combined, '\n')
	}
	return append(combined, stderr...)
}

func sanitizeOutput(value, secretURL string) string {
	if secretURL != "" {
		value = strings.ReplaceAll(value, secretURL, "[OSS_SIGNED_URL]")
	}
	value = strings.Map(func(r rune) rune {
		if r == 0 {
			return -1
		}
		return r
	}, value)
	return strings.TrimSpace(value)
}

func normalizeETag(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), `"`))
}
func shellQuote(value string) string { return centerpath.ShellQuote(value) }
func tail(value string, limit int) string {
	if len(value) > limit {
		return value[len(value)-limit:]
	}
	return value
}
func humanSize(value int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	size, unit := float64(value), 0
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", size, units[unit])
}
