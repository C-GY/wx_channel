package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"wx_channel/internal/database"
)

func TestBuildCreativeRadarVideosMapsExportCSVFields(t *testing.T) {
	record := database.ExportRecord{OSSUploadEnabled: true}
	items := []database.ExportRecordItem{{
		VideoID:          "export/video-1",
		Title:            "测试素材.mp4",
		Author:           "测试账号",
		PublishTime:      "2026-07-21 12:30:00",
		OriginalVideoURL: "https://finder.example/video.mp4",
		OSSVideoURL:      "https://oss.example/video.mp4",
		CoverURL:         "https://oss.example/cover.jpg",
		DurationMs:       125000,
		LikeCount:        7,
		CommentCount:     8,
		FavCount:         9,
		ForwardCount:     10,
	}}

	videos := buildCreativeRadarVideos(record, items)
	if len(videos) != 1 {
		t.Fatalf("video count = %d, want 1", len(videos))
	}
	video := videos[0]
	if video.Platform != "wechat_channel" || video.ExportID != items[0].VideoID {
		t.Fatalf("unexpected identity mapping: %#v", video)
	}
	if video.VideoURL != items[0].OSSVideoURL || video.AccountName != items[0].Author {
		t.Fatalf("unexpected resource mapping: %#v", video)
	}
	if video.CoverURL != items[0].CoverURL {
		t.Fatalf("cover URL was not mapped: %#v", video)
	}
	// The source page exposes like/favorite values in reverse semantic order;
	// this mirrors the already-corrected CSV columns.
	if video.LikeCount != 9 || video.FavoriteCount != 7 || video.CommentCount != 8 || video.ForwardCount != 10 {
		t.Fatalf("unexpected interaction mapping: %#v", video)
	}
	if video.Duration != "02:05" || video.PublishTime != items[0].PublishTime {
		t.Fatalf("unexpected time mapping: %#v", video)
	}
}

func TestCreativeRadarSyncContinuesAfterAnIndependentCSVFails(t *testing.T) {
	if err := database.Initialize(&database.Config{DBPath: filepath.Join(t.TempDir(), "creative-radar.db")}); err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	defer database.Close()

	repo := database.NewExportRecordRepository()
	for _, fixture := range []struct {
		id    string
		title string
	}{
		{id: "failed-csv", title: "will-fail"},
		{id: "successful-csv", title: "will-succeed"},
	} {
		record := &database.ExportRecord{ID: fixture.id, FileName: fixture.id + ".csv"}
		items := []database.ExportRecordItem{{VideoID: fixture.id + "-video", Title: fixture.title, Author: "author"}}
		if err := repo.Create(record, items); err != nil {
			t.Fatalf("create %s: %v", fixture.id, err)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Videos []creativeRadarVideo `json:"videos"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Videos) != 1 {
			t.Fatalf("unexpected request body: %#v, error: %v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		if body.Videos[0].Title == "will-fail" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":500,"msg":"temporary error"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"msg":"ok","data":{"total":1,"inserted":[101],"updated":[],"errors":[]}}`))
	}))
	defer server.Close()

	api := NewExportRecordAPI()
	api.creativeRadar.client.endpoint = server.URL
	api.creativeRadar.client.apiKey = "test-key"
	records, err := repo.ListCreativeRadarSyncCandidates()
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if err := repo.PrepareCreativeRadarSync(records); err != nil {
		t.Fatalf("prepare candidates: %v", err)
	}
	now := time.Now()
	api.creativeRadar.job = CreativeRadarSyncJob{Status: "running", TotalRecords: len(records), StartedAt: &now}
	api.runCreativeRadarSync(records)

	failedRecord, _ := repo.GetByID("failed-csv")
	successfulRecord, _ := repo.GetByID("successful-csv")
	if failedRecord.CreativeRadarSyncStatus != database.CreativeRadarSyncFailed || failedRecord.CreativeRadarSyncError == "" {
		t.Fatalf("first CSV should preserve its error: %#v", failedRecord)
	}
	if successfulRecord.CreativeRadarSyncStatus != database.CreativeRadarSyncSuccess || successfulRecord.CreativeRadarInserted != 1 {
		t.Fatalf("later CSV should still synchronize: %#v", successfulRecord)
	}
	job := api.creativeRadar.snapshot()
	if job.Status != "completed" || job.CompletedRecords != 2 || job.SuccessRecords != 1 || job.FailedRecords != 1 {
		t.Fatalf("unexpected final job: %#v", job)
	}
}

func TestCreativeRadarClientUploadsExpectedEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected request: %s, content-type=%q", r.Method, r.Header.Get("Content-Type"))
		}
		var body struct {
			APIKey string               `json:"api_key"`
			Source string               `json:"source"`
			Videos []creativeRadarVideo `json:"videos"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upload request: %v", err)
		}
		if body.APIKey != "test-key" || body.Source != creativeRadarSource || len(body.Videos) != 2 {
			t.Fatalf("unexpected upload body: %#v", body)
		}
		if body.Videos[0].CoverURL != "https://example.com/cover.jpg" {
			t.Fatalf("cover_url missing from upload body: %#v", body.Videos[0])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"msg":"ok","data":{"total":2,"inserted":[101],"updated":[],"errors":[{"index":1,"reason":"title必填"}]}}`))
	}))
	defer server.Close()

	client := &creativeRadarClient{
		endpoint:   server.URL,
		apiKey:     "test-key",
		httpClient: &http.Client{Timeout: time.Second},
	}
	result, err := client.upload(context.Background(), []creativeRadarVideo{
		{Platform: creativeRadarSource, Title: "one", AccountName: "author", CoverURL: "https://example.com/cover.jpg"},
		{Platform: creativeRadarSource, Title: "", AccountName: "author"},
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if result.Inserted != 1 || result.Updated != 0 || len(result.Errors) != 1 || result.Errors[0].Index != 1 {
		t.Fatalf("unexpected upload result: %#v", result)
	}
}
