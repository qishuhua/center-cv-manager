package baseimage

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
	"time"

	"center-cv-manager/internal/build"
	"center-cv-manager/internal/center"
	"center-cv-manager/internal/centerlock"
	"center-cv-manager/internal/domain"
	"center-cv-manager/internal/modular"
	"center-cv-manager/internal/secret"
	"center-cv-manager/internal/sshx"
	aliyunoss "github.com/aliyun/aliyun-oss-go-sdk/oss"
	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"golang.org/x/crypto/ssh"
)

var ErrNotConfigured = errors.New("尚未配置 OSS 基础镜像仓库")

type Repository interface {
	GetOSSCredential() (domain.OSSCredential, error)
	SaveOSSCredential(domain.OSSCredential) error
}

type Reporter interface {
	Log(string)
	Logf(string, ...any)
	Progress(float64)
	Message(string)
}

type ImageArchive struct {
	Key          string    `json:"key"`
	Name         string    `json:"name"`
	Platform     string    `json:"platform"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag"`
	LastModified time.Time `json:"last_modified"`
}

type Service struct {
	store   Repository
	cipher  *secret.Cipher
	centers *center.Service
}

func New(store Repository, cipher *secret.Cipher, centers *center.Service) *Service {
	return &Service{store: store, cipher: cipher, centers: centers}
}

func (s *Service) Credential() (domain.OSSCredential, error) {
	v, err := s.store.GetOSSCredential()
	if err == nil && v.AccessKeySecret != "" {
		v.AccessKeySecret = center.Mask
		v.Configured = true
	}
	return v, err
}

func (s *Service) SaveCredential(input domain.OSSCredential) (domain.OSSCredential, error) {
	input.Endpoint = strings.TrimSpace(input.Endpoint)
	input.Bucket = strings.TrimSpace(input.Bucket)
	input.AccessKeyID = strings.TrimSpace(input.AccessKeyID)
	input.ImagesPrefix = strings.TrimSpace(input.ImagesPrefix)
	input.ModelsPrefix = strings.TrimSpace(input.ModelsPrefix)
	if input.ImagesPrefix == "" {
		input.ImagesPrefix = "center-images/"
	}
	if input.ModelsPrefix == "" {
		input.ModelsPrefix = "center-models/"
	}
	if !strings.HasSuffix(input.ImagesPrefix, "/") || strings.HasPrefix(input.ImagesPrefix, "/") || strings.Contains(input.ImagesPrefix, "..") {
		return domain.OSSCredential{}, fmt.Errorf("OSS 镜像前缀必须是以 / 结尾的相对路径")
	}
	if !strings.HasSuffix(input.ModelsPrefix, "/") || strings.HasPrefix(input.ModelsPrefix, "/") || strings.Contains(input.ModelsPrefix, "..") {
		return domain.OSSCredential{}, fmt.Errorf("OSS 模型前缀必须是以 / 结尾的相对路径")
	}
	if input.Endpoint == "" || input.Bucket == "" || input.AccessKeyID == "" {
		return domain.OSSCredential{}, fmt.Errorf("Endpoint、Bucket 和 AccessKey ID 不能为空")
	}
	old, err := s.store.GetOSSCredential()
	if err != nil {
		return domain.OSSCredential{}, err
	}
	if input.AccessKeySecret == "" || input.AccessKeySecret == center.Mask {
		input.AccessKeySecret = old.AccessKeySecret
	} else {
		input.AccessKeySecret, err = s.cipher.Encrypt(input.AccessKeySecret)
		if err != nil {
			return domain.OSSCredential{}, err
		}
	}
	if input.AccessKeySecret == "" {
		return domain.OSSCredential{}, fmt.Errorf("AccessKey Secret 不能为空")
	}
	input.UpdatedAt = time.Now()
	input.Configured = true
	if err = s.store.SaveOSSCredential(input); err != nil {
		return domain.OSSCredential{}, err
	}
	input.AccessKeySecret = center.Mask
	return input, nil
}

func (s *Service) Catalog() ([]ImageArchive, error) {
	_, bucket, config, err := s.client()
	if err != nil {
		return nil, err
	}
	archives := []ImageArchive{}
	marker := ""
	for {
		result, listErr := bucket.ListObjects(aliyunoss.Prefix(config.ImagesPrefix), aliyunoss.Marker(marker), aliyunoss.MaxKeys(1000))
		if listErr != nil {
			return nil, fmt.Errorf("列举 OSS 基础镜像失败: %w", listErr)
		}
		for _, object := range result.Objects {
			relative := strings.TrimPrefix(object.Key, config.ImagesPrefix)
			if relative == "" || strings.HasSuffix(relative, "/") || !isArchive(relative) {
				continue
			}
			archives = append(archives, ImageArchive{Key: object.Key, Name: relative, Platform: archivePlatform(relative), Size: object.Size, ETag: normalizeETag(object.ETag), LastModified: object.LastModified})
		}
		if !result.IsTruncated {
			break
		}
		marker = result.NextMarker
	}
	sort.Slice(archives, func(i, j int) bool {
		if archives[i].Platform != archives[j].Platform {
			return archives[i].Platform < archives[j].Platform
		}
		return archives[i].Name < archives[j].Name
	})
	return archives, nil
}

func (s *Service) UpgradeCenter(ctx context.Context, centerID string, restartStack bool, r Reporter) error {
	release, cancelErr := centerlock.AcquireContext(ctx, centerID)
	if cancelErr != nil {
		return cancelErr
	}
	defer release()
	_, bucket, _, err := s.client()
	if err != nil {
		return err
	}
	archives, err := s.Catalog()
	if err != nil {
		return err
	}
	cfg, socket, stored, err := s.centers.Connection(centerID)
	if err != nil {
		return err
	}
	remote, fingerprint, err := sshx.NewDocker(cfg, socket)
	if err != nil {
		return err
	}
	defer remote.Close()
	_ = s.centers.RememberHostKey(stored, fingerprint)
	accelerator, err := build.ResolveAccelerator(ctx, remote, stored)
	if err != nil {
		return err
	}
	selected := make([]ImageArchive, 0, len(archives))
	var totalBytes int64
	for _, archive := range archives {
		if archive.Platform == "common" || archive.Platform == accelerator {
			selected = append(selected, archive)
			totalBytes += archive.Size
		}
	}
	if len(selected) == 0 {
		return fmt.Errorf("OSS 中没有适用于 %s 的基础镜像包", build.AcceleratorName(accelerator))
	}
	r.Message("正在同步基础镜像")
	r.Logf("中心平台：%s；候选 %d 个镜像包（合计 %s，仅用于版本比对，Docker 已存在的不会下载）", build.AcceleratorName(accelerator), len(selected), humanSize(totalBytes))
	cacheRoot, cacheFallback, err := resolveCacheRoot(ctx, remote.SSH, stored.ComposeFile)
	if err != nil {
		return err
	}
	if cacheFallback {
		r.Logf("Compose 目录不可写，已自动改用 SSH 用户缓存目录：%s", cacheRoot)
	} else {
		r.Logf("基础镜像缓存目录：%s", cacheRoot)
	}
	dockerImages, err := remote.Client.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return fmt.Errorf("读取中心 Docker 镜像列表失败: %w", err)
	}
	localTags := collectImageTags(dockerImages)
	localImageIDs := collectImageIDs(dockerImages)
	var completedBytes int64
	loadedCount := 0
	applicableRefs := map[string]bool{}
	for index, archive := range selected {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		localPath := path.Join(cacheRoot, archive.Name)
		etagPath := localPath + ".oss-etag"
		loadedPath := localPath + ".loaded-etag"
		loadedRefsPath := localPath + ".loaded-images"
		loadedIDsPath := localPath + ".loaded-image-ids"
		loadedETagOutput, _ := runSSH(ctx, remote.SSH, "cat "+shellQuote(loadedPath)+" 2>/dev/null || true", nil)
		loadedETag := strings.TrimSpace(string(loadedETagOutput))
		loadedRefsOutput, _ := runSSH(ctx, remote.SSH, "cat "+shellQuote(loadedRefsPath)+" 2>/dev/null || true", nil)
		loadedRefs := splitLines(string(loadedRefsOutput))
		for _, ref := range loadedRefs {
			applicableRefs[ref] = true
		}
		loadedIDsOutput, _ := runSSH(ctx, remote.SSH, "cat "+shellQuote(loadedIDsPath)+" 2>/dev/null || true", nil)
		loadedIDs := parseLoadedImageIDs(string(loadedIDsOutput))
		stateConflict := loadedETag == archive.ETag && len(loadedIDs) > 0 && !allImageIDsMatch(loadedRefs, loadedIDs, localImageIDs)
		if loadedETag == archive.ETag && allImageRefsExist(loadedRefs, localTags) && !stateConflict {
			if len(loadedIDs) == 0 {
				if err = writeLoadedState(ctx, remote.SSH, loadedPath, loadedRefsPath, loadedIDsPath, archive.ETag, loadedRefs, localImageIDs); err != nil {
					return fmt.Errorf("补充已加载镜像 ID 状态失败: %w", err)
				}
			}
			r.Logf("[%d/%d] Docker 已存在归档内镜像，跳过下载：%s（%s）", index+1, len(selected), archive.Name, strings.Join(loadedRefs, "、"))
			completedBytes += archive.Size
			if totalBytes > 0 {
				r.Progress(float64(completedBytes) * 90 / float64(totalBytes))
			}
			continue
		}
		if stateConflict {
			r.Logf("[%d/%d] Docker 中同名 tag 的 Image ID 已变化，需要恢复 OSS 归档内容：%s", index+1, len(selected), archive.Name)
		}
		if !stateConflict && (loadedETag == "" || loadedETag == archive.ETag) {
			if matchedRef, matched := archiveMatchesLocalImage(archive.Name, localTags); matched {
				applicableRefs[matchedRef] = true
				if err = writeLoadedState(ctx, remote.SSH, loadedPath, loadedRefsPath, loadedIDsPath, archive.ETag, []string{matchedRef}, localImageIDs); err != nil {
					return fmt.Errorf("记录已存在镜像状态失败: %w", err)
				}
				r.Logf("[%d/%d] Docker 已存在对应版本 %s，镜像 tar 无需下载：%s", index+1, len(selected), matchedRef, archive.Name)
				completedBytes += archive.Size
				if totalBytes > 0 {
					r.Progress(float64(completedBytes) * 90 / float64(totalBytes))
				}
				continue
			}
		}
		if loadedETag != "" && loadedETag != archive.ETag {
			r.Logf("[%d/%d] OSS 文件 ETag 已变化，即使镜像 tag 不变也需要下载并更新：%s", index+1, len(selected), archive.Name)
		}
		cachedCommand := "[ -f " + shellQuote(localPath) + " ] && [ \"$(stat -c %s " + shellQuote(localPath) + " 2>/dev/null)\" = " + shellQuote(strconv.FormatInt(archive.Size, 10)) + " ] && [ \"$(cat " + shellQuote(etagPath) + " 2>/dev/null)\" = " + shellQuote(archive.ETag) + " ]"
		_, cachedErr := runSSH(ctx, remote.SSH, cachedCommand, nil)
		if cachedErr != nil {
			signedURL, signErr := bucket.SignURL(archive.Key, aliyunoss.HTTPGet, 2*60*60)
			if signErr != nil {
				return fmt.Errorf("生成 %s 下载地址失败: %w", archive.Name, signErr)
			}
			partPath := localPath + ".part"
			partETagPath := partPath + ".oss-etag"
			prepareCommand := "mkdir -p " + shellQuote(path.Dir(localPath)) + " && if [ \"$(cat " + shellQuote(partETagPath) + " 2>/dev/null)\" != " + shellQuote(archive.ETag) + " ]; then rm -f " + shellQuote(partPath) + "; fi && printf %s " + shellQuote(archive.ETag) + " > " + shellQuote(partETagPath)
			if output, prepareErr := runSSH(ctx, remote.SSH, prepareCommand, nil); prepareErr != nil {
				return fmt.Errorf("准备 %s 断点文件失败: %s %w", archive.Name, sanitizeOutput(string(output), signedURL), prepareErr)
			}
			currentSize := remoteFileSize(ctx, remote.SSH, partPath)
			if currentSize > archive.Size {
				if _, removeErr := runSSH(ctx, remote.SSH, "rm -f "+shellQuote(partPath), nil); removeErr != nil {
					return fmt.Errorf("清理尺寸异常的 %s 断点文件失败: %w", archive.Name, removeErr)
				}
				currentSize = 0
			}
			r.Logf("[%d/%d] 下载 %s（%s）", index+1, len(selected), archive.Name, humanSize(archive.Size))
			if currentSize < archive.Size {
				command := downloadCommand(partPath, signedURL)
				if err = runDownload(ctx, remote.SSH, command, signedURL, partPath, completedBytes, totalBytes, r); err != nil {
					actualSize := remoteFileSize(ctx, remote.SSH, partPath)
					return fmt.Errorf("下载 %s 失败（OSS %s，临时文件 %s）: %w", archive.Name, humanSize(archive.Size), humanSize(actualSize), err)
				}
			} else {
				r.Logf("发现完整断点文件，跳过重复传输：%s", archive.Name)
			}
			actualSize := remoteFileSize(ctx, remote.SSH, partPath)
			if actualSize != archive.Size {
				return fmt.Errorf("下载 %s 后尺寸不一致：OSS %d 字节，中心临时文件 %d 字节；断点文件已保留供下次续传", archive.Name, archive.Size, actualSize)
			}
			finalizeCommand := "mv " + shellQuote(partPath) + " " + shellQuote(localPath) + " && printf %s " + shellQuote(archive.ETag) + " > " + shellQuote(etagPath) + " && rm -f " + shellQuote(partETagPath)
			if output, finalizeErr := runSSH(ctx, remote.SSH, finalizeCommand, nil); finalizeErr != nil {
				return fmt.Errorf("保存 %s 下载结果失败: %s %w", archive.Name, sanitizeOutput(string(output), signedURL), finalizeErr)
			}
		} else {
			r.Logf("[%d/%d] OSS 内容未变化，复用缓存：%s", index+1, len(selected), archive.Name)
		}
		r.Message("正在加载基础镜像 " + archive.Name)
		output, loadErr := runSSH(ctx, remote.SSH, "docker load -i "+shellQuote(localPath), nil)
		if strings.TrimSpace(string(output)) != "" {
			r.Log(strings.TrimSpace(string(output)))
		}
		if loadErr != nil {
			return fmt.Errorf("加载 %s 失败: %w", archive.Name, loadErr)
		}
		loadedCount++
		loadedRefs = parseLoadedImageRefs(string(output))
		if refreshed, listErr := remote.Client.ImageList(ctx, image.ListOptions{}); listErr == nil {
			localTags = collectImageTags(refreshed)
			localImageIDs = collectImageIDs(refreshed)
			if len(loadedRefs) == 0 {
				if matchedRef, matched := archiveMatchesLocalImage(archive.Name, localTags); matched {
					loadedRefs = []string{matchedRef}
				}
			}
		} else if len(loadedRefs) > 0 {
			for _, ref := range loadedRefs {
				localTags[strings.ToLower(ref)] = ref
			}
		}
		for _, ref := range loadedRefs {
			applicableRefs[ref] = true
		}
		if err = writeLoadedState(ctx, remote.SSH, loadedPath, loadedRefsPath, loadedIDsPath, archive.ETag, loadedRefs, localImageIDs); err != nil {
			return fmt.Errorf("记录 %s 已加载状态失败: %w", archive.Name, err)
		}
		completedBytes += archive.Size
		if totalBytes > 0 {
			r.Progress(float64(completedBytes) * 90 / float64(totalBytes))
		}
	}
	if restartStack {
		r.Message("正在应用变化镜像的使用服务")
		after, e := remote.Client.ImageList(ctx, image.ListOptions{})
		if e != nil {
			return e
		}
		desiredIDs := collectImageIDs(after)
		containers, e := remote.Client.ContainerList(ctx, containertypes.ListOptions{})
		if e != nil {
			return e
		}
		for _, m := range modular.Modules(stored) {
			if !m.Enabled {
				continue
			}
			raw, e := modular.Run(ctx, remote.SSH, modular.Command(m)+" config", "")
			if e != nil {
				return e
			}
			parsed, e := modular.Parse(raw)
			if e != nil {
				return e
			}
			project := m.Project
			if project == "" {
				project = parsed.Name
			}
			targets := []string{}
			for name, spec := range parsed.Services {
				if !applicableRefs[spec.Image] {
					continue
				}
				for _, v := range containers {
					if v.Labels["com.docker.compose.project"] == project && v.Labels["com.docker.compose.service"] == name && imageNeedsApply(spec.Image, v.ImageID, applicableRefs, desiredIDs) {
						targets = append(targets, name)
						break
					}
				}
			}
			if len(targets) == 0 {
				continue
			}
			sort.Strings(targets)
			command := modular.Command(m) + " up -d --no-deps --force-recreate"
			for _, n := range targets {
				command += " " + shellQuote(n)
			}
			r.Log("$ " + command)
			if output, e := modular.Run(ctx, remote.SSH, command, ""); e != nil {
				return fmt.Errorf("镜像已加载，模块 %s 应用失败: %s %w", m.ID, output, e)
			}
			m.Project = project
			if e = modular.Wait(ctx, remote, m, targets, nil); e != nil {
				return e
			}
		}
	} else if loadedCount > 0 {
		r.Logf("已加载 %d 个基础镜像归档；现有容器保持运行，尚未使用新镜像重建", loadedCount)
	} else {
		r.Log("基础镜像检查完成；Docker 中全部镜像内容状态一致，没有下载或执行 docker load")
	}
	r.Progress(100)
	r.Message("基础镜像更新完成")
	return nil
}

func (s *Service) client() (*aliyunoss.Client, *aliyunoss.Bucket, domain.OSSCredential, error) {
	config, err := s.store.GetOSSCredential()
	if err != nil {
		return nil, nil, domain.OSSCredential{}, err
	}
	if !config.Configured && (config.Endpoint == "" || config.Bucket == "" || config.AccessKeyID == "" || config.AccessKeySecret == "") {
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

func runDownload(ctx context.Context, client *ssh.Client, command, secretURL, partPath string, baseBytes, totalBytes int64, reporter Reporter) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout, session.Stderr = &stdout, &stderr
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
			if totalBytes <= 0 {
				continue
			}
			current := remoteFileSize(ctx, client, partPath)
			progress := float64(baseBytes+current) * 90 / float64(totalBytes)
			reporter.Progress(progress)
			reporter.Message(fmt.Sprintf("正在下载基础镜像 %s / %s", humanSize(baseBytes+current), humanSize(totalBytes)))
		}
	}
}

func remoteFileSize(ctx context.Context, client *ssh.Client, filePath string) int64 {
	output, err := runSSH(ctx, client, "stat -c %s "+shellQuote(filePath)+" 2>/dev/null || echo 0", nil)
	if err != nil {
		return 0
	}
	value, _ := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	return value
}

func resolveCacheRoot(ctx context.Context, client *ssh.Client, composeFile string) (string, bool, error) {
	composeCache := path.Join(path.Dir(composeFile), ".center-manager", "images")
	if _, err := runSSH(ctx, client, writableDirectoryCommand(composeCache), nil); err == nil {
		return composeCache, false, nil
	} else if ctx.Err() != nil {
		return "", false, ctx.Err()
	}
	homeOutput, err := runSSH(ctx, client, `printf '%s' "$HOME"`, nil)
	if err != nil {
		return "", false, fmt.Errorf("Compose 目录不可写，且无法读取 SSH 用户主目录: %w", err)
	}
	home := strings.TrimSpace(string(homeOutput))
	if !path.IsAbs(home) || home == "/" {
		return "", false, fmt.Errorf("Compose 目录不可写，SSH 用户主目录无效: %q", home)
	}
	userCache := path.Join(home, ".cache", "center-manager", "images")
	if output, err := runSSH(ctx, client, writableDirectoryCommand(userCache), nil); err != nil {
		message := sanitizeOutput(string(output), "")
		if message != "" {
			return "", false, fmt.Errorf("Compose 目录和 SSH 用户缓存目录均不可写: %s: %w", message, err)
		}
		return "", false, fmt.Errorf("Compose 目录和 SSH 用户缓存目录均不可写: %w", err)
	}
	return userCache, true, nil
}

func writableDirectoryCommand(directory string) string {
	quoted := shellQuote(directory)
	return "mkdir -p " + quoted + " && test -d " + quoted + " && test -w " + quoted
}

func collectImageTags(images []image.Summary) map[string]string {
	tags := make(map[string]string)
	for _, item := range images {
		for _, ref := range item.RepoTags {
			ref = strings.TrimSpace(ref)
			if ref == "" || ref == "<none>:<none>" {
				continue
			}
			tags[strings.ToLower(ref)] = ref
		}
	}
	return tags
}

func collectImageIDs(images []image.Summary) map[string]string {
	ids := make(map[string]string)
	for _, item := range images {
		for _, ref := range item.RepoTags {
			ref = strings.TrimSpace(ref)
			if ref != "" && ref != "<none>:<none>" {
				ids[strings.ToLower(ref)] = item.ID
			}
		}
	}
	return ids
}

func allImageRefsExist(refs []string, localTags map[string]string) bool {
	if len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		if _, found := localTags[strings.ToLower(ref)]; !found {
			return false
		}
	}
	return true
}

func archiveMatchesLocalImage(archiveName string, localTags map[string]string) (string, bool) {
	stem := strings.ToLower(path.Base(archiveName))
	for _, suffix := range []string{".tar.gz", ".tgz", ".tar"} {
		if strings.HasSuffix(stem, suffix) {
			stem = strings.TrimSuffix(stem, suffix)
			break
		}
	}
	for normalizedRef, originalRef := range localTags {
		repository, tag, ok := splitImageRef(normalizedRef)
		if !ok {
			continue
		}
		baseName := path.Base(repository)
		archiveVersion, matchedName := archiveVersionForImage(stem, baseName)
		if !matchedName {
			continue
		}
		archiveVersion = strings.TrimPrefix(archiveVersion, "v")
		imageVersion := strings.TrimPrefix(tag, "v")
		if archiveVersion == imageVersion {
			return originalRef, true
		}
	}
	return "", false
}

func archiveVersionForImage(stem, imageBaseName string) (string, bool) {
	for _, separator := range []string{"-", "_"} {
		prefix := imageBaseName + separator
		if strings.HasPrefix(stem, prefix) && len(stem) > len(prefix) {
			return stem[len(prefix):], true
		}
	}
	return "", false
}

func splitImageRef(ref string) (string, string, bool) {
	if strings.Contains(ref, "@") {
		return "", "", false
	}
	lastSlash, lastColon := strings.LastIndex(ref, "/"), strings.LastIndex(ref, ":")
	if lastColon <= lastSlash || lastColon == len(ref)-1 {
		return "", "", false
	}
	return ref[:lastColon], ref[lastColon+1:], true
}

func splitLines(value string) []string {
	result, seen := make([]string, 0), make(map[string]bool)
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !seen[line] {
			seen[line] = true
			result = append(result, line)
		}
	}
	return result
}

func parseLoadedImageRefs(output string) []string {
	refs := make([]string, 0)
	for _, line := range splitLines(output) {
		const prefix = "Loaded image:"
		if index := strings.Index(line, prefix); index >= 0 {
			ref := strings.TrimSpace(line[index+len(prefix):])
			if ref != "" {
				refs = append(refs, ref)
			}
		}
	}
	return splitLines(strings.Join(refs, "\n"))
}

func parseLoadedImageIDs(value string) map[string]string {
	ids := make(map[string]string)
	for _, line := range splitLines(value) {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
			ids[strings.ToLower(strings.TrimSpace(parts[0]))] = strings.TrimSpace(parts[1])
		}
	}
	return ids
}

func allImageIDsMatch(refs []string, recordedIDs, currentIDs map[string]string) bool {
	if len(refs) == 0 || len(recordedIDs) == 0 {
		return false
	}
	for _, ref := range refs {
		key := strings.ToLower(ref)
		if recordedIDs[key] == "" || currentIDs[key] == "" || recordedIDs[key] != currentIDs[key] {
			return false
		}
	}
	return true
}

func writeLoadedState(ctx context.Context, client *ssh.Client, etagPath, refsPath, idsPath, etag string, refs []string, currentIDs map[string]string) error {
	command := "mkdir -p " + shellQuote(path.Dir(etagPath)) + " && printf %s " + shellQuote(etag) + " > " + shellQuote(etagPath)
	if len(refs) == 0 {
		command += " && rm -f " + shellQuote(refsPath) + " " + shellQuote(idsPath)
	} else {
		command += " && printf %s " + shellQuote(strings.Join(refs, "\n")) + " > " + shellQuote(refsPath)
		idLines := make([]string, 0, len(refs))
		for _, ref := range refs {
			if id := currentIDs[strings.ToLower(ref)]; id != "" {
				idLines = append(idLines, ref+"\t"+id)
			}
		}
		if len(idLines) == 0 {
			command += " && rm -f " + shellQuote(idsPath)
		} else {
			command += " && printf %s " + shellQuote(strings.Join(idLines, "\n")) + " > " + shellQuote(idsPath)
		}
	}
	_, err := runSSH(ctx, client, command, nil)
	return err
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
	combined = append(combined, stderr...)
	return combined
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

func archivePlatform(relative string) string {
	normalized := strings.ToLower(strings.TrimLeft(strings.ReplaceAll(relative, "\\", "/"), "/"))
	if strings.HasPrefix(normalized, "nvidia/") {
		return "nvidia"
	}
	if strings.HasPrefix(normalized, "denglin/") {
		return "denglin"
	}
	return "common"
}

func isArchive(value string) bool {
	lower := strings.ToLower(value)
	return strings.HasSuffix(lower, ".tar") || strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz")
}

func normalizeETag(value string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), `"`))
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
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

func imageNeedsApply(ref, currentID string, applicable map[string]bool, desired map[string]string) bool {
	return applicable[ref] && desired[strings.ToLower(ref)] != "" && desired[strings.ToLower(ref)] != currentID
}
