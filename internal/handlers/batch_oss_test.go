package handlers

import (
	"context"
	"testing"
	"time"

	"wx_channel/internal/services"
)

type batchOSSUploaderStub struct {
	calls       int
	filePath    string
	materialID  string
	scrapedDate time.Time
}

func (s *batchOSSUploaderStub) UploadVideo(
	_ context.Context,
	filePath string,
	materialID string,
	scrapedDate time.Time,
) (services.OSSUploadResult, error) {
	s.calls++
	s.filePath = filePath
	s.materialID = materialID
	s.scrapedDate = scrapedDate
	return services.OSSUploadResult{
		ObjectKey:      "local/materials/2026-07-17/video-1.mp4",
		URL:            "https://signed.example/video-1",
		SizeBytes:      1024,
		ETag:           "etag",
		Verified:       true,
		AccessVerified: true,
	}, nil
}

func (s *batchOSSUploaderStub) UploadVideoWithProgress(
	ctx context.Context,
	filePath string,
	materialID string,
	scrapedDate time.Time,
	onProgress func(uploaded, total int64),
) (services.OSSUploadResult, error) {
	if onProgress != nil {
		onProgress(0, 1024)
		onProgress(512, 1024)
		onProgress(1024, 1024)
	}
	return s.UploadVideo(ctx, filePath, materialID, scrapedDate)
}

func TestUploadBatchTaskToOSS(t *testing.T) {
	uploader := &batchOSSUploaderStub{}
	handler := &BatchHandler{ossUploader: uploader}
	task := &BatchTask{
		ID:               "video-1",
		Title:            "测试视频",
		CreateTime:       "2025-03-21 13:15:08",
		CapturedAt:       "2026-07-17 05:20:33",
		OSSUploadEnabled: true,
		OSSStatus:        "pending",
	}

	if err := handler.uploadBatchTaskToOSS(context.Background(), task, `D:\downloads\video-1.mp4`); err != nil {
		t.Fatalf("uploadBatchTaskToOSS() error = %v", err)
	}
	if uploader.calls != 1 {
		t.Fatalf("UploadVideo() calls = %d, want 1", uploader.calls)
	}
	if uploader.filePath != `D:\downloads\video-1.mp4` || uploader.materialID != "video-1" {
		t.Fatalf("UploadVideo() arguments = path %q, ID %q", uploader.filePath, uploader.materialID)
	}
	if got := uploader.scrapedDate.Format("2006-01-02"); got != "2026-07-17" {
		t.Fatalf("UploadVideo() scraped date = %q", got)
	}
	if task.OSSStatus != "done" || task.OSSError != "" {
		t.Fatalf("task OSS status = %q, error = %q", task.OSSStatus, task.OSSError)
	}
	if task.OSSObjectKey != "local/materials/2026-07-17/video-1.mp4" {
		t.Fatalf("task OSS object key = %q", task.OSSObjectKey)
	}
	if task.OSSURL != "https://signed.example/video-1" {
		t.Fatalf("task OSS URL = %q", task.OSSURL)
	}
	if task.OSSProgress != 100 || task.OSSUploadedBytes != 1024 || task.OSSTotalBytes != 1024 {
		t.Fatalf("task OSS progress = %.1f%%, bytes = %d/%d", task.OSSProgress, task.OSSUploadedBytes, task.OSSTotalBytes)
	}
}

func TestUploadBatchTaskToOSSDisabled(t *testing.T) {
	uploader := &batchOSSUploaderStub{}
	handler := &BatchHandler{ossUploader: uploader}
	task := &BatchTask{ID: "video-1", OSSUploadEnabled: false}

	if err := handler.uploadBatchTaskToOSS(context.Background(), task, "video.mp4"); err != nil {
		t.Fatalf("uploadBatchTaskToOSS() disabled error = %v", err)
	}
	if uploader.calls != 0 {
		t.Fatalf("UploadVideo() calls = %d, want 0", uploader.calls)
	}
}
