package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultOSSEndpoint         = "https://oss.fandow.com/"
	DefaultOSSRegion           = "oss-cn-hangzhou"
	DefaultOSSBucket           = "marketing-video-dashboard"
	DefaultOSSObjectPrefix     = "wechat_channel"
	DefaultOSSAccessURLTimeout = 15 * time.Second
	maxOSSSinglePutSize        = int64(5 * 1024 * 1024 * 1024)
)

var safeOSSIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// OSSSettings describes the fixed S3-compatible OSS target and user credentials.
// The defaults mirror dataBridgeRPA's local environment upload configuration.
type OSSSettings struct {
	Endpoint         string
	Region           string
	Bucket           string
	AccessKeyID      string
	AccessKeySecret  string
	ObjectPrefix     string
	AccessURLTimeout time.Duration
	VerifyAccessURL  bool
}

// DefaultBatchOSSSettings builds the OSS configuration used by batch downloads.
func DefaultBatchOSSSettings(accessKeyID, accessKeySecret string) OSSSettings {
	return OSSSettings{
		Endpoint:         DefaultOSSEndpoint,
		Region:           DefaultOSSRegion,
		Bucket:           DefaultOSSBucket,
		AccessKeyID:      strings.TrimSpace(accessKeyID),
		AccessKeySecret:  strings.TrimSpace(accessKeySecret),
		ObjectPrefix:     DefaultOSSObjectPrefix,
		AccessURLTimeout: DefaultOSSAccessURLTimeout,
		VerifyAccessURL:  true,
	}
}

func (s OSSSettings) validate() error {
	parsed, err := url.Parse(strings.TrimSpace(s.Endpoint))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("OSS_ENDPOINT 必须是有效的 HTTPS 地址")
	}
	if strings.TrimSpace(s.Region) == "" {
		return fmt.Errorf("OSS_REGION 不能为空")
	}
	if strings.TrimSpace(s.Bucket) == "" {
		return fmt.Errorf("OSS_BUCKET 不能为空")
	}
	accessKeyID := strings.TrimSpace(s.AccessKeyID)
	accessKeySecret := strings.TrimSpace(s.AccessKeySecret)
	if accessKeyID == "" {
		return fmt.Errorf("OSS_ACCESS_KEY_ID 不能为空")
	}
	if accessKeySecret == "" {
		return fmt.Errorf("OSS_ACCESS_KEY_SECRET 不能为空")
	}
	if len(accessKeyID) > 256 || strings.ContainsAny(accessKeyID, "\r\n\x00") {
		return fmt.Errorf("OSS_ACCESS_KEY_ID 格式不合法")
	}
	if len(accessKeySecret) > 1024 || strings.ContainsAny(accessKeySecret, "\r\n\x00") {
		return fmt.Errorf("OSS_ACCESS_KEY_SECRET 格式不合法")
	}
	if _, err := normalizeOSSObjectPrefix(s.ObjectPrefix); err != nil {
		return err
	}
	if s.AccessURLTimeout <= 0 || s.AccessURLTimeout > 2*time.Minute {
		return fmt.Errorf("OSS 访问地址校验超时必须在 1 秒到 2 分钟之间")
	}
	return nil
}

// OSSUploadResult contains stable object identity and its permanent public URL.
type OSSUploadResult struct {
	ObjectKey      string
	URL            string
	SizeBytes      int64
	ETag           string
	Verified       bool
	AccessVerified bool
}

// OSSService streams decrypted MP4 files to an S3-compatible, path-style OSS API.
type OSSService struct {
	settings OSSSettings
	endpoint *url.URL
	client   *http.Client
	now      func() time.Time
}

// NewOSSService validates settings without making a network request.
func NewOSSService(settings OSSSettings) (*OSSService, error) {
	if err := settings.validate(); err != nil {
		return nil, err
	}
	endpoint, _ := url.Parse(strings.TrimRight(strings.TrimSpace(settings.Endpoint), "/"))
	var transport http.RoundTripper = http.DefaultTransport
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		clonedTransport := defaultTransport.Clone()
		clonedTransport.TLSHandshakeTimeout = 10 * time.Second
		clonedTransport.ResponseHeaderTimeout = 2 * time.Minute
		clonedTransport.ExpectContinueTimeout = time.Second
		transport = clonedTransport
	}
	return &OSSService{
		settings: settings,
		endpoint: endpoint,
		client: &http.Client{
			Transport: transport,
			Timeout:   0,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Scheme != "https" {
					return fmt.Errorf("OSS 跳转目标必须使用 HTTPS")
				}
				if len(via) >= 10 {
					return fmt.Errorf("OSS 跳转次数过多")
				}
				return nil
			},
		},
		now: time.Now,
	}, nil
}

func normalizeOSSObjectPrefix(prefix string) (string, error) {
	normalized := strings.Trim(strings.TrimSpace(prefix), "/")
	if normalized == "" {
		normalized = "materials"
	}
	if strings.Contains(normalized, `\`) {
		return "", fmt.Errorf("OSS object_prefix 不合法")
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == "" || segment == "." || segment == ".." || !safeOSSIdentifier.MatchString(segment) {
			return "", fmt.Errorf("OSS object_prefix 不合法")
		}
	}
	return normalized, nil
}

// BuildOSSObjectKey follows dataBridgeRPA's deterministic
// <prefix>/<YYYY-MM-DD>/<material-id>.mp4 convention.
func BuildOSSObjectKey(materialID, prefix string, scrapedDate time.Time) (string, error) {
	materialID = strings.TrimSpace(materialID)
	if !safeOSSIdentifier.MatchString(materialID) {
		return "", fmt.Errorf("视频 ID 只能包含 ASCII 字母、数字、下划线和连字符")
	}
	normalizedPrefix, err := normalizeOSSObjectPrefix(prefix)
	if err != nil {
		return "", err
	}
	if scrapedDate.IsZero() {
		return normalizedPrefix + "/" + materialID + ".mp4", nil
	}
	return normalizedPrefix + "/" + scrapedDate.Format("2006-01-02") + "/" + materialID + ".mp4", nil
}

// UploadVideo hashes and streams one local MP4 with a signed PUT, then verifies
// object size with HEAD and validates the permanent public URL with a range GET.
func (s *OSSService) UploadVideo(ctx context.Context, filePath, materialID string, scrapedDate time.Time) (OSSUploadResult, error) {
	return s.uploadVideo(ctx, filePath, materialID, scrapedDate, nil)
}

// UploadVideoWithProgress performs the same upload while reporting bytes sent
// during the PUT request. Hashing and remote verification are represented by
// the surrounding OSS status rather than fake byte progress.
func (s *OSSService) UploadVideoWithProgress(
	ctx context.Context,
	filePath, materialID string,
	scrapedDate time.Time,
	onProgress func(uploaded, total int64),
) (OSSUploadResult, error) {
	return s.uploadVideo(ctx, filePath, materialID, scrapedDate, onProgress)
}

func (s *OSSService) uploadVideo(
	ctx context.Context,
	filePath, materialID string,
	scrapedDate time.Time,
	onProgress func(uploaded, total int64),
) (OSSUploadResult, error) {
	var result OSSUploadResult
	if s == nil {
		return result, fmt.Errorf("OSS 服务未初始化")
	}

	file, err := os.Open(filePath)
	if err != nil {
		return result, fmt.Errorf("打开待上传视频失败: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return result, fmt.Errorf("读取待上传视频信息失败: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return result, fmt.Errorf("待上传视频不存在或为空")
	}
	if info.Size() > maxOSSSinglePutSize {
		return result, fmt.Errorf("待上传视频超过 OSS 单次 PUT 的 5 GiB 上限")
	}

	objectKey, err := BuildOSSObjectKey(materialID, s.settings.ObjectPrefix, scrapedDate)
	if err != nil {
		return result, err
	}
	payloadHash, err := hashOSSFile(file)
	if err != nil {
		return result, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return result, fmt.Errorf("重置待上传视频读取位置失败: %w", err)
	}

	var requestBody io.Reader = file
	if onProgress != nil {
		onProgress(0, info.Size())
		requestBody = &ossProgressReader{
			reader:     file,
			total:      info.Size(),
			onProgress: onProgress,
		}
	}

	target := s.objectURL(objectKey)
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), requestBody)
	if err != nil {
		return result, fmt.Errorf("创建 OSS 上传请求失败: %w", err)
	}
	request.ContentLength = info.Size()
	request.Header.Set("Content-Type", "video/mp4")
	s.signRequest(request, payloadHash, "video/mp4", s.now().UTC())

	response, err := s.client.Do(request)
	if err != nil {
		return result, fmt.Errorf("OSS 上传失败（bucket=%s, key=%s）: %w", s.settings.Bucket, objectKey, err)
	}
	if err := checkOSSResponse(response, "OSS 上传"); err != nil {
		return result, fmt.Errorf("%w（bucket=%s, key=%s）", err, s.settings.Bucket, objectKey)
	}

	remoteSize, etag, err := s.headObject(ctx, objectKey)
	if err != nil {
		return result, err
	}
	if remoteSize != info.Size() {
		return result, fmt.Errorf("OSS 上传后大小校验失败：本地 %d bytes，远端 %d bytes", info.Size(), remoteSize)
	}

	accessURL := s.objectURL(objectKey).String()
	accessVerified := false
	if s.settings.VerifyAccessURL {
		if err := s.verifyAccessURL(ctx, accessURL); err != nil {
			return result, err
		}
		accessVerified = true
	}

	return OSSUploadResult{
		ObjectKey:      objectKey,
		URL:            accessURL,
		SizeBytes:      info.Size(),
		ETag:           etag,
		Verified:       true,
		AccessVerified: accessVerified,
	}, nil
}

type ossProgressReader struct {
	reader     io.Reader
	uploaded   int64
	total      int64
	onProgress func(uploaded, total int64)
}

func (r *ossProgressReader) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	if count > 0 {
		r.uploaded += int64(count)
		if r.onProgress != nil {
			r.onProgress(r.uploaded, r.total)
		}
	}
	return count, err
}

func hashOSSFile(file *os.File) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("计算待上传视频哈希失败: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *OSSService) objectURL(objectKey string) *url.URL {
	target := *s.endpoint
	target.RawQuery = ""
	target.Fragment = ""
	target.Path = strings.TrimRight(target.Path, "/") + "/" + s.settings.Bucket + "/" + objectKey
	return &target
}

func (s *OSSService) headObject(ctx context.Context, objectKey string) (int64, string, error) {
	target := s.objectURL(objectKey)
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, target.String(), nil)
	if err != nil {
		return 0, "", fmt.Errorf("创建 OSS 校验请求失败: %w", err)
	}
	s.signRequest(request, emptyOSSPayloadHash(), "", s.now().UTC())
	response, err := s.client.Do(request)
	if err != nil {
		return 0, "", fmt.Errorf("OSS 上传后校验失败: %w", err)
	}
	if err := checkOSSResponse(response, "OSS 上传后校验"); err != nil {
		return 0, "", err
	}

	remoteSize := response.ContentLength
	if remoteSize < 0 {
		remoteSize, err = strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
		if err != nil {
			return 0, "", fmt.Errorf("OSS 上传后校验缺少有效的 Content-Length")
		}
	}
	return remoteSize, strings.Trim(response.Header.Get("ETag"), `"`), nil
}

func checkOSSResponse(response *http.Response, operation string) error {
	if response == nil {
		return fmt.Errorf("%s未返回响应", operation)
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = response.Status
	}
	return fmt.Errorf("%s失败: HTTP %d: %s", operation, response.StatusCode, message)
}

func emptyOSSPayloadHash() string {
	sum := sha256.Sum256(nil)
	return hex.EncodeToString(sum[:])
}

func canonicalOSSQuery(values url.Values) string {
	return strings.ReplaceAll(values.Encode(), "+", "%20")
}

func canonicalOSSURI(target *url.URL) string {
	uri := target.EscapedPath()
	if uri == "" {
		return "/"
	}
	return uri
}

func (s *OSSService) signRequest(request *http.Request, payloadHash, contentType string, now time.Time) {
	amzDate := now.Format("20060102T150405Z")
	shortDate := now.Format("20060102")
	request.Header.Set("X-Amz-Date", amzDate)
	request.Header.Set("X-Amz-Content-Sha256", payloadHash)

	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "host:" + request.URL.Host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	if contentType != "" {
		signedHeaders = "content-type;" + signedHeaders
		canonicalHeaders = "content-type:" + contentType + "\n" + canonicalHeaders
	}

	canonicalRequest := request.Method + "\n" +
		canonicalOSSURI(request.URL) + "\n" +
		canonicalOSSQuery(request.URL.Query()) + "\n" +
		canonicalHeaders + "\n" +
		signedHeaders + "\n" + payloadHash
	credentialScope := shortDate + "/" + s.settings.Region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + credentialScope + "\n" + sha256Hex([]byte(canonicalRequest))
	signature := hex.EncodeToString(s.signatureKey(shortDate, stringToSign))
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.settings.AccessKeyID+"/"+credentialScope+
		", SignedHeaders="+signedHeaders+", Signature="+signature)
}

func (s *OSSService) signatureKey(shortDate, stringToSign string) []byte {
	dateKey := hmacSHA256([]byte("AWS4"+s.settings.AccessKeySecret), shortDate)
	regionKey := hmacSHA256(dateKey, s.settings.Region)
	serviceKey := hmacSHA256(regionKey, "s3")
	signingKey := hmacSHA256(serviceKey, "aws4_request")
	return hmacSHA256(signingKey, stringToSign)
}

func hmacSHA256(key []byte, value string) []byte {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte(value))
	return hash.Sum(nil)
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func (s *OSSService) verifyAccessURL(ctx context.Context, accessURL string) error {
	verifyContext, cancel := context.WithTimeout(ctx, s.settings.AccessURLTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(verifyContext, http.MethodGet, accessURL, nil)
	if err != nil {
		return fmt.Errorf("创建 OSS 访问地址校验请求失败: %w", err)
	}
	request.Header.Set("Range", "bytes=0-0")
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("OSS 访问链接验证失败: %w", err)
	}
	defer response.Body.Close()
	if response.Request != nil && response.Request.URL != nil && response.Request.URL.Scheme != "https" {
		return fmt.Errorf("OSS 访问链接发生非 HTTPS 重定向")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("OSS 访问链接验证失败: HTTP %d", response.StatusCode)
	}
	buffer := make([]byte, 1)
	read, readErr := io.ReadFull(response.Body, buffer)
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		return fmt.Errorf("OSS 访问链接验证失败: %w", readErr)
	}
	if read == 0 {
		return fmt.Errorf("OSS 访问链接未返回视频内容")
	}
	return nil
}
