package services

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildOSSObjectKey(t *testing.T) {
	t.Parallel()

	date := time.Date(2026, time.July, 17, 10, 30, 0, 0, time.Local)
	key, err := BuildOSSObjectKey("video_1-2", "local/materials", date)
	if err != nil {
		t.Fatalf("BuildOSSObjectKey() error = %v", err)
	}
	if want := "local/materials/2026-07-17/video_1-2.mp4"; key != want {
		t.Fatalf("BuildOSSObjectKey() = %q, want %q", key, want)
	}

	for _, materialID := range []string{"", "../video", "video/name", "视频"} {
		if _, err := BuildOSSObjectKey(materialID, "local/materials", date); err == nil {
			t.Errorf("BuildOSSObjectKey(%q) unexpectedly succeeded", materialID)
		}
	}
	if _, err := BuildOSSObjectKey("video", "../materials", date); err == nil {
		t.Fatal("BuildOSSObjectKey() accepted an unsafe prefix")
	}
}

func TestOSSSettingsValidation(t *testing.T) {
	t.Parallel()

	settings := DefaultBatchOSSSettings("", "secret")
	if _, err := NewOSSService(settings); err == nil || !strings.Contains(err.Error(), "OSS_ACCESS_KEY_ID") {
		t.Fatalf("NewOSSService() missing AccessKey ID error = %v", err)
	}

	settings = DefaultBatchOSSSettings("access-id", "")
	if _, err := NewOSSService(settings); err == nil || !strings.Contains(err.Error(), "OSS_ACCESS_KEY_SECRET") {
		t.Fatalf("NewOSSService() missing secret error = %v", err)
	}

	settings = DefaultBatchOSSSettings("access-id", "secret")
	settings.Endpoint = "http://example.com"
	if _, err := NewOSSService(settings); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("NewOSSService() insecure endpoint error = %v", err)
	}
}

func TestOSSSigV4MatchesBotocore(t *testing.T) {
	t.Parallel()

	settings := DefaultBatchOSSSettings("test-id", "test-secret")
	settings.Endpoint = "https://example.com"
	service, err := NewOSSService(settings)
	if err != nil {
		t.Fatalf("NewOSSService() error = %v", err)
	}
	body := []byte("decrypted-mp4-video")
	request, err := http.NewRequest(http.MethodPut, "https://example.com/bucket/key", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Content-Type", "video/mp4")
	service.signRequest(
		request,
		sha256Hex(body),
		"video/mp4",
		time.Date(2026, time.July, 17, 5, 20, 33, 0, time.UTC),
	)

	want := "AWS4-HMAC-SHA256 Credential=test-id/20260717/oss-cn-hangzhou/s3/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date, " +
		"Signature=00841cd167162a5e9eed6d091d5ce2ac6cb555b1ad23654e3b03984e5c7a5693"
	if got := request.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}

	presignedURL, err := service.presignGet(
		"local/materials/2026-07-17/video-1.mp4",
		time.Date(2026, time.July, 17, 5, 20, 33, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("presignGet() error = %v", err)
	}
	wantURL := "https://example.com/marketing-video-dashboard/local/materials/2026-07-17/video-1.mp4?" +
		"X-Amz-Algorithm=AWS4-HMAC-SHA256&" +
		"X-Amz-Credential=test-id%2F20260717%2Foss-cn-hangzhou%2Fs3%2Faws4_request&" +
		"X-Amz-Date=20260717T052033Z&X-Amz-Expires=604800&" +
		"X-Amz-Signature=03be2f0f1cc40993971508a5120c26f645577c5eccedec0b991839446c2e38d1&" +
		"X-Amz-SignedHeaders=host"
	if presignedURL != wantURL {
		t.Fatalf("presignGet() = %q, want %q", presignedURL, wantURL)
	}
}

func TestOSSServiceUploadVideo(t *testing.T) {
	t.Parallel()

	video := []byte("decrypted-mp4-video")
	wantPath := "/marketing-video-dashboard/local/materials/2026-07-17/video-1.mp4"
	var mu sync.Mutex
	methods := make([]string, 0, 3)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("request path = %q, want %q", r.URL.Path, wantPath)
		}
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()

		switch r.Method {
		case http.MethodPut:
			if got := r.Header.Get("Content-Type"); got != "video/mp4" {
				t.Errorf("PUT Content-Type = %q", got)
			}
			if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "AWS4-HMAC-SHA256 Credential=test-id/") {
				t.Errorf("PUT Authorization = %q", got)
			}
			if r.Header.Get("X-Amz-Date") == "" || r.Header.Get("X-Amz-Content-Sha256") == "" {
				t.Error("PUT is missing SigV4 headers")
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read PUT body: %v", err)
			}
			if string(body) != string(video) {
				t.Errorf("PUT body = %q, want %q", body, video)
			}
			w.WriteHeader(http.StatusOK)

		case http.MethodHead:
			if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=test-id/") {
				t.Error("HEAD is missing SigV4 Authorization")
			}
			w.Header().Set("Content-Length", "19")
			w.Header().Set("ETag", `"test-etag"`)
			w.WriteHeader(http.StatusOK)

		case http.MethodGet:
			if got := r.Header.Get("Range"); got != "bytes=0-0" {
				t.Errorf("GET Range = %q", got)
			}
			for _, key := range []string{"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-Expires", "X-Amz-Signature", "X-Amz-SignedHeaders"} {
				if r.URL.Query().Get(key) == "" {
					t.Errorf("signed GET is missing %s", key)
				}
			}
			w.Header().Set("Content-Range", "bytes 0-0/19")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(video[:1])

		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	filePath := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(filePath, video, 0o600); err != nil {
		t.Fatalf("write test video: %v", err)
	}

	settings := DefaultBatchOSSSettings("test-id", "test-secret")
	settings.Endpoint = server.URL
	service, err := NewOSSService(settings)
	if err != nil {
		t.Fatalf("NewOSSService() error = %v", err)
	}
	service.client = server.Client()
	service.now = func() time.Time {
		return time.Date(2026, time.July, 17, 5, 20, 33, 0, time.UTC)
	}

	var progressValues [][2]int64
	result, err := service.UploadVideoWithProgress(
		context.Background(),
		filePath,
		"video-1",
		time.Date(2026, time.July, 17, 0, 0, 0, 0, time.Local),
		func(uploaded, total int64) {
			progressValues = append(progressValues, [2]int64{uploaded, total})
		},
	)
	if err != nil {
		t.Fatalf("UploadVideo() error = %v", err)
	}
	if result.ObjectKey != strings.TrimPrefix(wantPath, "/marketing-video-dashboard/") {
		t.Errorf("ObjectKey = %q", result.ObjectKey)
	}
	if result.SizeBytes != int64(len(video)) || result.ETag != "test-etag" {
		t.Errorf("UploadVideo() result = %+v", result)
	}
	if !result.Verified || !result.AccessVerified {
		t.Errorf("UploadVideo() verification flags = %+v", result)
	}
	if !strings.HasPrefix(result.URL, server.URL+wantPath+"?") || !strings.Contains(result.URL, "X-Amz-Signature=") {
		t.Errorf("UploadVideo() URL = %q", result.URL)
	}
	if len(progressValues) < 2 {
		t.Fatalf("upload progress callbacks = %#v, want at least start and completion", progressValues)
	}
	lastProgress := progressValues[len(progressValues)-1]
	if lastProgress[0] != int64(len(video)) || lastProgress[1] != int64(len(video)) {
		t.Fatalf("last upload progress = %#v, want %d/%d", lastProgress, len(video), len(video))
	}

	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(methods, ","); got != "PUT,HEAD,GET" {
		t.Errorf("request methods = %q, want PUT,HEAD,GET", got)
	}
}

func TestOSSServiceRejectsRemoteSizeMismatch(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "1")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	filePath := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(filePath, []byte("more-than-one-byte"), 0o600); err != nil {
		t.Fatalf("write test video: %v", err)
	}
	settings := DefaultBatchOSSSettings("test-id", "test-secret")
	settings.Endpoint = server.URL
	settings.VerifyAccessURL = false
	service, err := NewOSSService(settings)
	if err != nil {
		t.Fatalf("NewOSSService() error = %v", err)
	}
	service.client = server.Client()

	_, err = service.UploadVideo(context.Background(), filePath, "video-1", time.Time{})
	if err == nil || !strings.Contains(err.Error(), "大小校验失败") {
		t.Fatalf("UploadVideo() mismatch error = %v", err)
	}
}
