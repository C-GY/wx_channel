package api

import (
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"wx_channel/internal/database"
)

func TestBuildExportRecordCSVUsesRequestedOrderAndDynamicVideoLink(t *testing.T) {
	item := database.ExportRecordItem{
		VideoID:          "video-1",
		Title:            "=formula title",
		Author:           "作者",
		PublishTime:      "2026-07-17 14:30:00",
		OriginalVideoURL: "https://finder.video.qq.com/video-1.mp4?token=original",
		OSSVideoURL:      "https://bucket.oss-cn-hangzhou.aliyuncs.com/wechat_channel/video-1.mp4",
		CoverURL:         "https://finder.video.qq.com/cover.jpg",
		DurationMs:       211000,
		FileSize:         204063129,
		LikeCount:        12,
		CommentCount:     34,
		FavCount:         56,
		ForwardCount:     78,
		CapturedAt:       "2026-07-17 15:23:25",
	}

	tests := []struct {
		name       string
		ossEnabled bool
		header     string
		videoURL   string
	}{
		{
			name:     "original address",
			header:   "视频链接（原始地址）",
			videoURL: item.OriginalVideoURL,
		},
		{
			name:       "OSS address",
			ossEnabled: true,
			header:     "视频链接（OSS地址）",
			videoURL:   item.OSSVideoURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := buildExportRecordCSV(&database.ExportRecord{OSSUploadEnabled: tt.ossEnabled}, []database.ExportRecordItem{item})
			if err != nil {
				t.Fatalf("buildExportRecordCSV() error = %v", err)
			}
			if !strings.HasPrefix(string(data), "\xEF\xBB\xBF") {
				t.Fatal("CSV should contain a UTF-8 BOM for Excel")
			}
			if !strings.Contains(string(data), "\r\n") {
				t.Fatal("CSV should use CRLF line endings")
			}

			reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\xEF\xBB\xBF")))
			rows, err := reader.ReadAll()
			if err != nil {
				t.Fatalf("read generated CSV: %v", err)
			}
			if len(rows) != 2 {
				t.Fatalf("got %d CSV rows, want 2", len(rows))
			}
			wantHeaders := []string{
				"视频ID", "视频标题", "作者昵称", "发布时间", tt.header,
				"视频封面链接", "视频时长", "文件大小", "点赞数", "评论数",
				"收藏数", "转发数", "数据采集时间",
			}
			if strings.Join(rows[0], "|") != strings.Join(wantHeaders, "|") {
				t.Fatalf("headers = %#v, want %#v", rows[0], wantHeaders)
			}
			if len(rows[1]) != 13 {
				t.Fatalf("got %d data fields, want 13", len(rows[1]))
			}
			if rows[1][1] != "'=formula title" {
				t.Fatalf("formula title was not neutralized: %q", rows[1][1])
			}
			if rows[1][4] != tt.videoURL {
				t.Fatalf("video URL = %q, want %q", rows[1][4], tt.videoURL)
			}
			if rows[1][6] != "3:31" {
				t.Fatalf("duration = %q, want 3:31", rows[1][6])
			}
			if rows[1][7] != "194.61 MB" {
				t.Fatalf("file size = %q, want 194.61 MB", rows[1][7])
			}
			if rows[1][8] != "56" || rows[1][9] != "34" || rows[1][10] != "12" || rows[1][11] != "78" {
				t.Fatalf("interaction fields are out of order: %#v", rows[1][8:12])
			}
			if rows[1][12] != "2026-07-17 15:23:25" {
				t.Fatalf("capturedAt = %q, want exact local date-time", rows[1][12])
			}
		})
	}
}

func TestNormalizeExportDateTimeDoesNotShiftLocalInput(t *testing.T) {
	previousLocation := time.Local
	time.Local = time.FixedZone("CST", 8*60*60)
	defer func() { time.Local = previousLocation }()

	if got := normalizeExportDateTime("2026-07-17 15:23:25", "fallback"); got != "2026-07-17 15:23:25" {
		t.Fatalf("local date-time shifted: got %q", got)
	}
	if got := normalizeExportDateTime("2026-07-17T07:23:25Z", "fallback"); got != "2026-07-17 15:23:25" {
		t.Fatalf("RFC3339 date-time was not converted to local time: got %q", got)
	}
}
