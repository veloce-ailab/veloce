package service

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
)

const (
	autoUpdateMaxArchive = 512 << 20 // 512 MiB
	autoUpdateMaxBinary  = 256 << 20 // 256 MiB
)

var autoUpdateRepositoryURL = "https://api.github.com/repos/veloce-ailab/veloce/releases/latest"

// BuildVersion is set by release builds with:
// -ldflags "-X github.com/veloce-ailab/veloce/internal/service.BuildVersion=vX.Y.Z"
// Development builds deliberately remain non-updatable.
var BuildVersion = "dev"

var semanticVersionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

type AutoUpdateStatus struct {
	Enabled         bool   `json:"enabled"`
	IntervalHours   string `json:"interval_hours"`
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	Platform        string `json:"platform"`
	Supported       bool   `json:"supported"`
	LastCheckedAt   string `json:"last_checked_at"`
	LastError       string `json:"last_error"`
	InProgress      bool   `json:"in_progress"`
	Phase           string `json:"phase"`
	Progress        int    `json:"progress"`
	DownloadedBytes int64  `json:"downloaded_bytes"`
	TotalBytes      int64  `json:"total_bytes"`
	Message         string `json:"message"`
}

type autoUpdateProgress struct {
	InProgress      bool
	Phase           string
	Progress        int
	DownloadedBytes int64
	TotalBytes      int64
	Message         string
}

type githubRelease struct {
	TagName string               `json:"tag_name"`
	Draft   bool                 `json:"draft"`
	Assets  []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

type releaseCandidate struct {
	Version string
	Asset   githubReleaseAsset
}

// AutoUpdateService checks official releases and hands verified binaries to the
// application lifecycle, which is responsible for shutting down and restarting.
type AutoUpdateService struct {
	client  *http.Client
	apply   func(stagedBinary string) error
	runMu   sync.Mutex
	started sync.Once
}

var autoUpdateRuntime struct {
	sync.Mutex
	service  *AutoUpdateService
	progress autoUpdateProgress
}

func NewAutoUpdateService() *AutoUpdateService {
	return &AutoUpdateService{client: &http.Client{Timeout: 2 * time.Minute}}
}

func (s *AutoUpdateService) Start(apply func(stagedBinary string) error) {
	if s == nil {
		return
	}
	s.apply = apply
	s.started.Do(func() {
		RegisterScheduledJob(ScheduledJob{
			Name:        "auto_update",
			Description: "检查并安装新版本（每个节点独立更新自身）",
			PrimaryOnly: false,
			Interval:    func() time.Duration { return time.Minute },
			Enabled:     func() bool { return AutoUpdateEnabled() },
			Run: func(ctx context.Context) (string, bool, error) {
				if !s.RunDue(ctx) {
					return "", false, nil
				}
				status := CurrentAutoUpdateStatus()
				if status.LastError != "" {
					return "", true, errors.New(status.LastError)
				}
				message := "已检查更新"
				if status.LatestVersion != "" {
					message += "，最新版本 " + status.LatestVersion
				}
				return message, true, nil
			},
		})
	})
}

// RegisterAutoUpdateService makes the application-owned updater available to
// the admin API so manual updates use the same safe restart callback.
func RegisterAutoUpdateService(service *AutoUpdateService) {
	autoUpdateRuntime.Lock()
	defer autoUpdateRuntime.Unlock()
	autoUpdateRuntime.service = service
}

func StartManualAutoUpdate() (AutoUpdateStatus, error) {
	autoUpdateRuntime.Lock()
	updater := autoUpdateRuntime.service
	autoUpdateRuntime.Unlock()
	if updater == nil {
		return CurrentAutoUpdateStatus(), errors.New("automatic restart is unavailable")
	}
	return updater.StartManualUpdate()
}

func (s *AutoUpdateService) StartManualUpdate() (AutoUpdateStatus, error) {
	if s == nil || s.apply == nil {
		return CurrentAutoUpdateStatus(), errors.New("automatic restart is unavailable")
	}
	if !updatableBuild() {
		return CurrentAutoUpdateStatus(), errors.New("this build has no release version; install an official release binary")
	}
	if !beginAutoUpdate("checking", "Checking for updates") {
		return CurrentAutoUpdateStatus(), errors.New("an update is already in progress")
	}
	go s.runUpdate(context.Background())
	return CurrentAutoUpdateStatus(), nil
}

func (s *AutoUpdateService) CheckNow(ctx context.Context) (AutoUpdateStatus, error) {
	if s == nil {
		return CurrentAutoUpdateStatus(), errors.New("auto-update service is unavailable")
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	candidate, err := s.fetchCandidate(ctx)
	if err != nil {
		recordAutoUpdateCheck("", err)
		return CurrentAutoUpdateStatus(), err
	}
	recordAutoUpdateCheck(candidate.Version, nil)
	return CurrentAutoUpdateStatus(), nil
}

// RunDue reports whether an update check actually ran this pass.
func (s *AutoUpdateService) RunDue(ctx context.Context) bool {
	if s == nil || !AutoUpdateEnabled() || !autoUpdateDue() {
		return false
	}
	if !beginAutoUpdate("checking", "Checking for updates") {
		return false
	}
	s.runUpdate(ctx)
	return true
}

func (s *AutoUpdateService) runUpdate(ctx context.Context) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	completed := false
	defer func() {
		if !completed {
			finishAutoUpdate("error", "Update failed")
		}
	}()

	candidate, err := s.fetchCandidate(ctx)
	if err != nil {
		recordAutoUpdateCheck("", err)
		setAutoUpdateProgress("error", 0, 0, 0, err.Error())
		return
	}
	recordAutoUpdateCheck(candidate.Version, nil)
	if !isNewerRelease(candidate.Version, CurrentBuildVersion()) {
		finishAutoUpdate("up_to_date", "Already up to date")
		completed = true
		return
	}

	setAutoUpdateProgress("downloading", 0, 0, 0, "Downloading update")
	staged, err := s.downloadAndStage(ctx, candidate, func(downloaded, total int64) {
		progress := 0
		if total > 0 {
			progress = int(downloaded * 100 / total)
		}
		setAutoUpdateProgress("downloading", progress, downloaded, total, "Downloading update")
	})
	if err != nil {
		recordAutoUpdateError(err)
		setAutoUpdateProgress("error", 0, 0, 0, err.Error())
		return
	}
	setAutoUpdateProgress("restarting", 100, 0, 0, "Restarting with update")
	if err := s.apply(staged); err != nil {
		_ = os.Remove(staged)
		recordAutoUpdateError(err)
		setAutoUpdateProgress("error", 0, 0, 0, err.Error())
		return
	}
	completed = true
}

func CurrentBuildVersion() string {
	return strings.TrimSpace(BuildVersion)
}

func CurrentAutoUpdateStatus() AutoUpdateStatus {
	latest := model.GetSystemSetting("auto_update_latest_version", "")
	current := CurrentBuildVersion()
	progress := currentAutoUpdateProgress()
	return AutoUpdateStatus{
		Enabled:         AutoUpdateEnabled(),
		IntervalHours:   strconv.Itoa(autoUpdateIntervalHours()),
		CurrentVersion:  current,
		LatestVersion:   latest,
		UpdateAvailable: isNewerRelease(latest, current),
		Platform:        runtime.GOOS + "-" + runtime.GOARCH,
		Supported:       updatableBuild(),
		LastCheckedAt:   model.GetSystemSetting("auto_update_last_checked_at", ""),
		LastError:       model.GetSystemSetting("auto_update_last_error", ""),
		InProgress:      progress.InProgress,
		Phase:           progress.Phase,
		Progress:        progress.Progress,
		DownloadedBytes: progress.DownloadedBytes,
		TotalBytes:      progress.TotalBytes,
		Message:         progress.Message,
	}
}

func AutoUpdateEnabled() bool {
	return settingBool("auto_update_enabled", false)
}

func autoUpdateIntervalHours() int {
	return autoUpdateSettingInt("auto_update_interval_hours", 24, 1, 168)
}

func autoUpdateDue() bool {
	last, err := time.Parse(time.RFC3339, strings.TrimSpace(model.GetSystemSetting("auto_update_last_checked_at", "")))
	if err != nil {
		return true
	}
	return time.Since(last) >= time.Duration(autoUpdateIntervalHours())*time.Hour
}

func autoUpdateSettingInt(key string, fallback, min, max int) int {
	value, err := strconv.Atoi(strings.TrimSpace(model.GetSystemSetting(key, strconv.Itoa(fallback))))
	if err != nil || value < min {
		return fallback
	}
	if value > max {
		return max
	}
	return value
}

func (s *AutoUpdateService) fetchCandidate(ctx context.Context) (releaseCandidate, error) {
	if !updatableBuild() {
		return releaseCandidate{}, errors.New("this build has no release version; install an official release binary")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, autoUpdateRepositoryURL, nil)
	if err != nil {
		return releaseCandidate{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "flai-community-auto-updater")
	resp, err := s.client.Do(req)
	if err != nil {
		return releaseCandidate{}, fmt.Errorf("request latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return releaseCandidate{}, fmt.Errorf("latest release returned HTTP %d", resp.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&release); err != nil {
		return releaseCandidate{}, fmt.Errorf("decode latest release: %w", err)
	}
	if release.Draft {
		return releaseCandidate{}, errors.New("latest release is a draft")
	}
	version := normalizeReleaseVersion(release.TagName)
	if !validSemanticVersion(version) {
		return releaseCandidate{}, fmt.Errorf("latest release has invalid tag %q", release.TagName)
	}
	expected := fmt.Sprintf("flai-community-v%s-%s-%s", version, runtime.GOOS, runtime.GOARCH)
	for _, asset := range release.Assets {
		if asset.Name != expected+".zip" && asset.Name != expected+".tar.gz" {
			continue
		}
		if strings.TrimSpace(asset.BrowserDownloadURL) == "" {
			return releaseCandidate{}, fmt.Errorf("release asset %q has no download URL", asset.Name)
		}
		if !validSHA256Digest(asset.Digest) {
			return releaseCandidate{}, fmt.Errorf("release asset %q has no SHA-256 digest", asset.Name)
		}
		return releaseCandidate{Version: "v" + version, Asset: asset}, nil
	}
	return releaseCandidate{}, fmt.Errorf("no release asset for %s-%s", runtime.GOOS, runtime.GOARCH)
}

func (s *AutoUpdateService) downloadAndStage(ctx context.Context, candidate releaseCandidate, onProgress func(downloaded, total int64)) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	directory := filepath.Dir(executable)
	archive, err := os.CreateTemp(directory, ".flai-update-*.download")
	if err != nil {
		return "", fmt.Errorf("create update download: %w", err)
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate.Asset.BrowserDownloadURL, nil)
	if err != nil {
		archive.Close()
		return "", err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "flai-community-auto-updater")
	resp, err := s.client.Do(req)
	if err != nil {
		archive.Close()
		return "", fmt.Errorf("download update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		archive.Close()
		return "", fmt.Errorf("update download returned HTTP %d", resp.StatusCode)
	}
	hash := sha256.New()
	reader := &autoUpdateProgressReader{
		Reader:   io.LimitReader(resp.Body, autoUpdateMaxArchive+1),
		total:    resp.ContentLength,
		onUpdate: onProgress,
	}
	if onProgress != nil {
		onProgress(0, resp.ContentLength)
	}
	written, err := io.Copy(io.MultiWriter(archive, hash), reader)
	if closeErr := archive.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("save update download: %w", err)
	}
	if written > autoUpdateMaxArchive {
		return "", fmt.Errorf("update archive exceeds %d MiB", autoUpdateMaxArchive>>20)
	}
	if !strings.EqualFold("sha256:"+hex.EncodeToString(hash.Sum(nil)), strings.TrimSpace(candidate.Asset.Digest)) {
		return "", errors.New("update checksum does not match the release digest")
	}

	staged := executable + ".update"
	if err := os.Remove(staged); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove previous staged update: %w", err)
	}
	if err := extractReleaseBinary(archivePath, candidate.Asset.Name, staged); err != nil {
		_ = os.Remove(staged)
		return "", err
	}
	return staged, nil
}

type autoUpdateProgressReader struct {
	io.Reader
	read     int64
	total    int64
	onUpdate func(downloaded, total int64)
}

func (r *autoUpdateProgressReader) Read(buffer []byte) (int, error) {
	n, err := r.Reader.Read(buffer)
	if n > 0 {
		r.read += int64(n)
		if r.onUpdate != nil {
			r.onUpdate(r.read, r.total)
		}
	}
	return n, err
}

func extractReleaseBinary(archivePath, assetName, target string) error {
	if strings.HasSuffix(assetName, ".zip") {
		return extractZipBinary(archivePath, target)
	}
	if strings.HasSuffix(assetName, ".tar.gz") {
		return extractTarGzBinary(archivePath, target)
	}
	return errors.New("unsupported update archive format")
}

func extractZipBinary(archivePath, target string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open update zip: %w", err)
	}
	defer reader.Close()
	var file *zip.File
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		if !entry.FileInfo().Mode().IsRegular() || !safeArchiveFileName(entry.Name) || file != nil {
			return errors.New("update archive must contain exactly one regular file at its root")
		}
		file = entry
	}
	if file == nil {
		return errors.New("update archive contains no binary")
	}
	source, err := file.Open()
	if err != nil {
		return fmt.Errorf("open update binary: %w", err)
	}
	defer source.Close()
	return copyReleaseBinary(source, target)
}

func extractTarGzBinary(archivePath, target string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open update tar.gz: %w", err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	found := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read update tar.gz: %w", err)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg || !safeArchiveFileName(header.Name) || found {
			return errors.New("update archive must contain exactly one regular file at its root")
		}
		if err := copyReleaseBinary(reader, target); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return errors.New("update archive contains no binary")
	}
	return nil
}

func copyReleaseBinary(source io.Reader, target string) error {
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err != nil {
		return fmt.Errorf("create extracted binary: %w", err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(source, autoUpdateMaxBinary+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("extract update binary: %w", copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if written == 0 || written > autoUpdateMaxBinary {
		return fmt.Errorf("update binary must be between 1 byte and %d MiB", autoUpdateMaxBinary>>20)
	}
	return os.Chmod(target, 0755)
}

func safeArchiveFileName(name string) bool {
	return name != "" && filepath.Base(name) == name && !strings.ContainsAny(name, `\\/`)
}

// RestartWithStagedUpdate is called only after the HTTP server has drained.
func RestartWithStagedUpdate(staged string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	if filepath.Clean(staged) != filepath.Clean(executable+".update") {
		return errors.New("invalid staged update path")
	}
	if runtime.GOOS == "windows" {
		return scheduleWindowsUpdate(executable, staged)
	}
	backup := executable + ".previous"
	_ = os.Remove(backup)
	if err := os.Rename(executable, backup); err != nil {
		return fmt.Errorf("backup current executable: %w", err)
	}
	if err := os.Rename(staged, executable); err != nil {
		_ = os.Rename(backup, executable)
		return fmt.Errorf("install update: %w", err)
	}
	command := exec.Command(executable, os.Args[1:]...)
	command.Dir = filepath.Dir(executable)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		_ = os.Rename(executable, staged)
		_ = os.Rename(backup, executable)
		return fmt.Errorf("start updated executable: %w", err)
	}
	return nil
}

func scheduleWindowsUpdate(executable, staged string) error {
	script, err := os.CreateTemp(filepath.Dir(executable), ".flai-update-*.ps1")
	if err != nil {
		return fmt.Errorf("create Windows update helper: %w", err)
	}
	scriptPath := script.Name()
	content := "$ErrorActionPreference = 'Stop'\r\n" +
		fmt.Sprintf("Wait-Process -Id %d\r\n", os.Getpid()) +
		"Move-Item -LiteralPath '" + powerShellLiteral(staged) + "' -Destination '" + powerShellLiteral(executable) + "' -Force\r\n" +
		"Start-Process -FilePath '" + powerShellLiteral(executable) + "' -WorkingDirectory '" + powerShellLiteral(filepath.Dir(executable)) + "'\r\n" +
		"Remove-Item -LiteralPath $PSCommandPath -Force\r\n"
	if _, err := io.WriteString(script, content); err != nil {
		script.Close()
		_ = os.Remove(scriptPath)
		return err
	}
	if err := script.Close(); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	if err := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath).Start(); err != nil {
		_ = os.Remove(scriptPath)
		return fmt.Errorf("start Windows update helper: %w", err)
	}
	return nil
}

func powerShellLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func recordAutoUpdateCheck(version string, err error) {
	_ = model.SetSystemSetting("auto_update_last_checked_at", time.Now().UTC().Format(time.RFC3339))
	if version != "" {
		_ = model.SetSystemSetting("auto_update_latest_version", version)
	}
	recordAutoUpdateError(err)
}

func recordAutoUpdateError(err error) {
	message := ""
	if err != nil {
		message = strings.TrimSpace(err.Error())
		if len(message) > 500 {
			message = message[:500]
		}
	}
	_ = model.SetSystemSetting("auto_update_last_error", message)
}

func beginAutoUpdate(phase, message string) bool {
	autoUpdateRuntime.Lock()
	defer autoUpdateRuntime.Unlock()
	if autoUpdateRuntime.progress.InProgress {
		return false
	}
	autoUpdateRuntime.progress = autoUpdateProgress{InProgress: true, Phase: phase, Message: message}
	return true
}

func finishAutoUpdate(phase, message string) {
	autoUpdateRuntime.Lock()
	defer autoUpdateRuntime.Unlock()
	autoUpdateRuntime.progress = autoUpdateProgress{Phase: phase, Progress: 100, Message: message}
}

func setAutoUpdateProgress(phase string, progress int, downloaded, total int64, message string) {
	autoUpdateRuntime.Lock()
	defer autoUpdateRuntime.Unlock()
	autoUpdateRuntime.progress = autoUpdateProgress{
		InProgress:      phase != "error" && phase != "up_to_date",
		Phase:           phase,
		Progress:        progress,
		DownloadedBytes: downloaded,
		TotalBytes:      total,
		Message:         message,
	}
}

func currentAutoUpdateProgress() autoUpdateProgress {
	autoUpdateRuntime.Lock()
	defer autoUpdateRuntime.Unlock()
	return autoUpdateRuntime.progress
}

func updatableBuild() bool {
	if !supportedReleasePlatform() {
		return false
	}
	if !validSemanticVersion(normalizeReleaseVersion(CurrentBuildVersion())) {
		return false
	}
	_, err := os.Executable()
	return err == nil
}

func supportedReleasePlatform() bool {
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		return runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
	default:
		return false
	}
}

func normalizeReleaseVersion(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

func validSHA256Digest(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(strings.ToLower(value), "sha256:") {
		return false
	}
	value = value[len("sha256:"):]
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validSemanticVersion(value string) bool {
	return semanticVersionPattern.MatchString(normalizeReleaseVersion(value))
}

func isNewerRelease(latest, current string) bool {
	latestParts, latestPrerelease, ok := parseSemanticVersion(latest)
	if !ok {
		return false
	}
	currentParts, currentPrerelease, ok := parseSemanticVersion(current)
	if !ok {
		return false
	}
	for index := range latestParts {
		if latestParts[index] != currentParts[index] {
			return latestParts[index] > currentParts[index]
		}
	}
	if latestPrerelease == currentPrerelease {
		return false
	}
	return latestPrerelease == "" // a final release is newer than its prerelease
}

func parseSemanticVersion(value string) ([3]int, string, bool) {
	match := semanticVersionPattern.FindStringSubmatch(normalizeReleaseVersion(value))
	if match == nil {
		return [3]int{}, "", false
	}
	var parts [3]int
	for index := range parts {
		parsed, err := strconv.Atoi(match[index+1])
		if err != nil {
			return [3]int{}, "", false
		}
		parts[index] = parsed
	}
	return parts, match[4], true
}
