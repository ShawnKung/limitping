package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIURL = "https://api.github.com/repos/ShawnKung/limitping/releases/latest"
	maxMetadata   = 1 << 20
	maxBinary     = 128 << 20
)

type Client struct {
	HTTPClient     *http.Client
	APIURL         string
	GOOS           string
	GOARCH         string
	ExecutablePath string
}

type Result struct {
	Current    string
	Latest     string
	Path       string
	Comparison int
	Updated    bool
}

type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func NewClient() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: 2 * time.Minute},
		APIURL:     defaultAPIURL,
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
	}
}

func (c *Client) Update(ctx context.Context, currentVersion string) (Result, error) {
	current, err := parseVersion(currentVersion)
	if err != nil {
		return Result{}, fmt.Errorf("当前版本 %q 不是可更新的发布版本: %w", currentVersion, err)
	}
	rel, err := c.latest(ctx, currentVersion)
	if err != nil {
		return Result{}, err
	}
	latest, err := parseVersion(rel.TagName)
	if err != nil {
		return Result{}, fmt.Errorf("最新 Release tag %q 无效: %w", rel.TagName, err)
	}

	result := Result{Current: current.String(), Latest: latest.String()}
	result.Comparison = compareVersions(current, latest)
	if result.Comparison >= 0 {
		return result, nil
	}

	osName, arch := c.GOOS, c.GOARCH
	if !supportedTarget(osName, arch) {
		return result, fmt.Errorf("自动更新不支持 %s/%s", osName, arch)
	}
	binaryName := fmt.Sprintf("limitping_%s_%s_%s", latest.String(), osName, arch)
	binaryAsset, ok := findAsset(rel.Assets, binaryName)
	if !ok {
		return result, fmt.Errorf("Release %s 缺少 %s", rel.TagName, binaryName)
	}
	checksumsAsset, ok := findAsset(rel.Assets, "checksums.txt")
	if !ok {
		return result, fmt.Errorf("Release %s 缺少 checksums.txt", rel.TagName)
	}

	checksums, err := c.downloadMetadata(ctx, checksumsAsset.URL, currentVersion)
	if err != nil {
		return result, fmt.Errorf("下载 checksums.txt: %w", err)
	}
	expected, err := checksumFor(checksums, binaryName)
	if err != nil {
		return result, err
	}

	target, err := c.executablePath()
	if err != nil {
		return result, err
	}
	if err := c.replace(ctx, binaryAsset.URL, target, expected, currentVersion); err != nil {
		return result, err
	}
	result.Path = target
	result.Updated = true
	return result, nil
}

func (c *Client) latest(ctx context.Context, currentVersion string) (release, error) {
	apiURL := c.APIURL
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	body, err := c.downloadMetadata(ctx, apiURL, currentVersion)
	if err != nil {
		return release{}, fmt.Errorf("查询 GitHub Release: %w", err)
	}
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil {
		return release{}, fmt.Errorf("解析 GitHub Release: %w", err)
	}
	if rel.TagName == "" {
		return release{}, fmt.Errorf("GitHub Release 缺少 tag_name")
	}
	return rel, nil
}

func (c *Client) downloadMetadata(ctx context.Context, url, currentVersion string) ([]byte, error) {
	resp, err := c.get(ctx, url, currentVersion)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxMetadata+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > maxMetadata {
		return nil, fmt.Errorf("响应超过 %d 字节", maxMetadata)
	}
	return body, nil
}

func (c *Client) get(ctx context.Context, url, currentVersion string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "limitping/"+strings.TrimPrefix(currentVersion, "v"))
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func (c *Client) executablePath() (string, error) {
	path := c.ExecutablePath
	if path == "" {
		var err error
		path, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("确定当前可执行文件: %w", err)
		}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("解析可执行文件路径 %s: %w", path, err)
	}
	return resolved, nil
}

func (c *Client) replace(ctx context.Context, url, target string, expected [sha256.Size]byte, currentVersion string) error {
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("检查当前可执行文件 %s: %w", target, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("当前可执行文件不是普通文件: %s", target)
	}

	resp, err := c.get(ctx, url, currentVersion)
	if err != nil {
		return fmt.Errorf("下载更新: %w", err)
	}
	defer resp.Body.Close()

	tmp, err := os.CreateTemp(filepath.Dir(target), ".limitping-update-*")
	if err != nil {
		return fmt.Errorf("在 %s 创建更新文件: %w", filepath.Dir(target), err)
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		tmp.Close()
		if !keep {
			os.Remove(tmpPath)
		}
	}()

	hash := sha256.New()
	limited := &io.LimitedReader{R: resp.Body, N: maxBinary + 1}
	written, err := io.Copy(io.MultiWriter(tmp, hash), limited)
	if err != nil {
		return fmt.Errorf("写入更新文件: %w", err)
	}
	if written > maxBinary {
		return fmt.Errorf("更新文件超过 %d 字节", maxBinary)
	}
	if actual := hash.Sum(nil); !equalChecksum(actual, expected[:]) {
		return fmt.Errorf("更新文件 SHA-256 校验失败")
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		return fmt.Errorf("设置更新文件权限: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("同步更新文件: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭更新文件: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("替换 %s: %w", target, err)
	}
	keep = true
	return nil
}

func supportedTarget(osName, arch string) bool {
	return (osName == "darwin" || osName == "linux") && (arch == "amd64" || arch == "arm64")
}

func findAsset(assets []asset, name string) (asset, bool) {
	for _, candidate := range assets {
		if candidate.Name == name && candidate.URL != "" {
			return candidate, true
		}
	}
	return asset{}, false
}

func checksumFor(contents []byte, name string) ([sha256.Size]byte, error) {
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size {
			return [sha256.Size]byte{}, fmt.Errorf("%s 的 SHA-256 无效", name)
		}
		var checksum [sha256.Size]byte
		copy(checksum[:], decoded)
		return checksum, nil
	}
	return [sha256.Size]byte{}, fmt.Errorf("checksums.txt 中没有 %s", name)
}

func equalChecksum(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for i := range left {
		different |= left[i] ^ right[i]
	}
	return different == 0
}

type semanticVersion struct {
	major      uint64
	minor      uint64
	patch      uint64
	prerelease []string
}

func parseVersion(value string) (semanticVersion, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(value), "v")
	if build := strings.IndexByte(raw, '+'); build >= 0 {
		raw = raw[:build]
	}
	core, prerelease, hasPrerelease := strings.Cut(raw, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semanticVersion{}, fmt.Errorf("需要 MAJOR.MINOR.PATCH")
	}
	numbers := make([]uint64, 3)
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semanticVersion{}, fmt.Errorf("版本数字 %q 无效", part)
		}
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return semanticVersion{}, fmt.Errorf("版本数字 %q 无效", part)
		}
		numbers[i] = number
	}
	parsed := semanticVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}
	if hasPrerelease {
		if prerelease == "" {
			return semanticVersion{}, fmt.Errorf("预发布版本不能为空")
		}
		parsed.prerelease = strings.Split(prerelease, ".")
		for _, identifier := range parsed.prerelease {
			if !validPrereleaseIdentifier(identifier) {
				return semanticVersion{}, fmt.Errorf("预发布标识 %q 无效", identifier)
			}
		}
	}
	return parsed, nil
}

func validPrereleaseIdentifier(identifier string) bool {
	if identifier == "" || (len(identifier) > 1 && identifier[0] == '0' && numeric(identifier)) {
		return false
	}
	for _, char := range identifier {
		if !(char >= '0' && char <= '9' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char == '-') {
			return false
		}
	}
	return true
}

func numeric(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func compareVersions(left, right semanticVersion) int {
	for _, pair := range [][2]uint64{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(left.prerelease) == 0 && len(right.prerelease) == 0 {
		return 0
	}
	if len(left.prerelease) == 0 {
		return 1
	}
	if len(right.prerelease) == 0 {
		return -1
	}
	for i := 0; i < len(left.prerelease) && i < len(right.prerelease); i++ {
		leftID, rightID := left.prerelease[i], right.prerelease[i]
		if leftID == rightID {
			continue
		}
		leftNumeric, rightNumeric := numeric(leftID), numeric(rightID)
		switch {
		case leftNumeric && rightNumeric:
			if len(leftID) < len(rightID) || len(leftID) == len(rightID) && leftID < rightID {
				return -1
			}
			return 1
		case leftNumeric:
			return -1
		case rightNumeric:
			return 1
		case leftID < rightID:
			return -1
		default:
			return 1
		}
	}
	if len(left.prerelease) < len(right.prerelease) {
		return -1
	}
	if len(left.prerelease) > len(right.prerelease) {
		return 1
	}
	return 0
}

func (v semanticVersion) String() string {
	value := fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
	if len(v.prerelease) > 0 {
		value += "-" + strings.Join(v.prerelease, ".")
	}
	return value
}
