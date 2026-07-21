package database

import "testing"

func exportRecordTestItems() []ExportRecordItem {
	return []ExportRecordItem{
		{
			VideoID:          "video-1",
			Title:            "视频 1",
			OriginalVideoURL: "https://finder.video.qq.com/video-1.mp4",
			CapturedAt:       "2026-07-17 15:23:25",
		},
		{
			VideoID:          "video-2",
			Title:            "视频 2",
			OriginalVideoURL: "https://finder.video.qq.com/video-2.mp4",
			CapturedAt:       "2026-07-17 15:23:25",
		},
	}
}

func TestExportRecordRepositoryReadinessAndQueue(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewExportRecordRepository()
	originalRecord := &ExportRecord{ID: "original-export", FileName: "original.csv"}
	if err := repo.Create(originalRecord, exportRecordTestItems()); err != nil {
		t.Fatalf("create original export: %v", err)
	}
	if originalRecord.Status != ExportStatusReady || !originalRecord.DownloadReady || originalRecord.CompletedCount != 2 {
		t.Fatalf("original export should be immediately ready: %#v", originalRecord)
	}
	if originalRecord.CreativeRadarSyncStatus != CreativeRadarSyncNotSynced {
		t.Fatalf("new export should be unsynchronized: %#v", originalRecord)
	}

	ossRecord := &ExportRecord{ID: "oss-export", FileName: "oss.csv", OSSUploadEnabled: true}
	if err := repo.Create(ossRecord, exportRecordTestItems()); err != nil {
		t.Fatalf("create OSS export: %v", err)
	}
	if ossRecord.Status != ExportStatusProcessing || ossRecord.DownloadReady {
		t.Fatalf("OSS export should start in processing state: %#v", ossRecord)
	}

	if err := repo.UpdateItemProgress("oss-export", "video-1", ExportItemProgressUpdate{
		DownloadStatus:   "done",
		DownloadProgress: 100,
		FileSize:         1024,
		OSSStatus:        "done",
		OSSProgress:      100,
		OSSUploadedBytes: 1024,
		OSSTotalBytes:    1024,
		OSSObjectKey:     "wechat_channel/video-1.mp4",
		OSSVideoURL:      "https://bucket.oss/video-1.mp4",
	}); err != nil {
		t.Fatalf("complete first OSS item: %v", err)
	}
	stored, err := repo.GetByID("oss-export")
	if err != nil {
		t.Fatalf("read OSS export: %v", err)
	}
	if stored.Status != ExportStatusProcessing || stored.DownloadReady || stored.CompletedCount != 1 {
		t.Fatalf("OSS export became ready before every item completed: %#v", stored)
	}

	if err := repo.UpdateItemProgress("oss-export", "video-2", ExportItemProgressUpdate{
		DownloadStatus:   "done",
		DownloadProgress: 100,
		FileSize:         2048,
		OSSStatus:        "done",
		OSSProgress:      100,
		OSSUploadedBytes: 2048,
		OSSTotalBytes:    2048,
		OSSObjectKey:     "wechat_channel/video-2.mp4",
		OSSVideoURL:      "https://bucket.oss/video-2.mp4",
	}); err != nil {
		t.Fatalf("complete second OSS item: %v", err)
	}
	stored, err = repo.GetByID("oss-export")
	if err != nil {
		t.Fatalf("read ready OSS export: %v", err)
	}
	if stored.Status != ExportStatusReady || !stored.DownloadReady || stored.CompletedCount != 2 || stored.ReadyAt == nil {
		t.Fatalf("OSS export should be ready after every item uploads: %#v", stored)
	}

	queue, err := repo.ListOSSUploadQueue(10)
	if err != nil {
		t.Fatalf("list OSS upload queue: %v", err)
	}
	if len(queue) != 2 {
		t.Fatalf("OSS queue length = %d, want 2", len(queue))
	}
	for _, item := range queue {
		if item.ExportRecordID != "oss-export" || item.ExportFileName != "oss.csv" || item.OSSStatus != "done" || item.OSSVideoURL == "" {
			t.Fatalf("unexpected OSS queue item: %#v", item)
		}
	}

	stats, err := repo.Stats()
	if err != nil {
		t.Fatalf("read export stats: %v", err)
	}
	if stats.Total != 2 || stats.Ready != 2 || stats.Processing != 0 || stats.Failed != 0 {
		t.Fatalf("unexpected export stats: %#v", stats)
	}
}

func TestExportRecordRepositoryCreativeRadarSyncLifecycle(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewExportRecordRepository()
	record := &ExportRecord{ID: "radar-export", FileName: "radar.csv"}
	if err := repo.Create(record, exportRecordTestItems()); err != nil {
		t.Fatalf("create export: %v", err)
	}
	candidates, err := repo.ListCreativeRadarSyncCandidates()
	if err != nil || len(candidates) != 1 || candidates[0].ID != record.ID {
		t.Fatalf("unexpected creative radar candidates: %#v, error: %v", candidates, err)
	}
	if err := repo.PrepareCreativeRadarSync(candidates); err != nil {
		t.Fatalf("prepare creative radar sync: %v", err)
	}
	stored, _ := repo.GetByID(record.ID)
	if stored.CreativeRadarSyncStatus != CreativeRadarSyncPending || stored.CreativeRadarSyncTotal != 2 {
		t.Fatalf("unexpected queued sync state: %#v", stored)
	}
	if err := repo.BeginCreativeRadarSync(record.ID, 2); err != nil {
		t.Fatalf("begin creative radar sync: %v", err)
	}
	if err := repo.UpdateCreativeRadarSyncProgress(record.ID, 1, 0, 1, 0, ""); err != nil {
		t.Fatalf("update creative radar progress: %v", err)
	}
	if err := repo.FinishCreativeRadarSync(record.ID, true, 2, 0, 1, 1, ""); err != nil {
		t.Fatalf("finish creative radar sync: %v", err)
	}
	stored, err = repo.GetByID(record.ID)
	if err != nil {
		t.Fatalf("read synchronized export: %v", err)
	}
	if stored.CreativeRadarSyncStatus != CreativeRadarSyncSuccess ||
		stored.CreativeRadarSyncCompleted != 2 || stored.CreativeRadarInserted != 1 ||
		stored.CreativeRadarUpdated != 1 || stored.CreativeRadarSyncedAt == nil {
		t.Fatalf("unexpected successful sync state: %#v", stored)
	}
	candidates, err = repo.ListCreativeRadarSyncCandidates()
	if err != nil || len(candidates) != 0 {
		t.Fatalf("successful export should not be selected again: %#v, error: %v", candidates, err)
	}
}

func TestExportRecordRepositoryPersistsAutomaticCreativeRadarQueue(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewExportRecordRepository()
	record := &ExportRecord{
		ID:                    "auto-radar-export",
		FileName:              "auto-radar.csv",
		OSSUploadEnabled:      true,
		CreativeRadarAutoSync: true,
	}
	if err := repo.Create(record, exportRecordTestItems()); err != nil {
		t.Fatalf("create automatic Creative Radar export: %v", err)
	}
	if record.CreativeRadarSyncStatus != CreativeRadarSyncPending || !record.CreativeRadarAutoSync {
		t.Fatalf("automatic sync request was not persisted: %#v", record)
	}
	candidates, err := repo.ListCreativeRadarAutoSyncCandidates()
	if err != nil || len(candidates) != 1 || candidates[0].ID != record.ID {
		t.Fatalf("unexpected automatic sync candidates: %#v, error: %v", candidates, err)
	}
	if err := repo.RecoverInterruptedCreativeRadarSync(); err != nil {
		t.Fatalf("recover interrupted sync: %v", err)
	}
	stored, err := repo.GetByID(record.ID)
	if err != nil {
		t.Fatalf("read automatic sync record: %v", err)
	}
	if stored.CreativeRadarSyncStatus != CreativeRadarSyncPending {
		t.Fatalf("pending automatic queue entry should survive restart: %#v", stored)
	}
}

func TestExportRecordRepositoryRecoversInterruptedCreativeRadarSync(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewExportRecordRepository()
	record := &ExportRecord{ID: "interrupted-radar-export", FileName: "interrupted.csv"}
	if err := repo.Create(record, exportRecordTestItems()); err != nil {
		t.Fatalf("create export: %v", err)
	}
	if err := repo.BeginCreativeRadarSync(record.ID, 2); err != nil {
		t.Fatalf("begin creative radar sync: %v", err)
	}
	if err := repo.RecoverInterruptedCreativeRadarSync(); err != nil {
		t.Fatalf("recover interrupted creative radar sync: %v", err)
	}
	stored, err := repo.GetByID(record.ID)
	if err != nil {
		t.Fatalf("read recovered export: %v", err)
	}
	if stored.CreativeRadarSyncStatus != CreativeRadarSyncFailed || stored.CreativeRadarSyncError == "" {
		t.Fatalf("unexpected recovered sync state: %#v", stored)
	}
}

func TestExportRecordRepositoryMarkFailedTerminatesPendingItems(t *testing.T) {
	cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewExportRecordRepository()
	record := &ExportRecord{ID: "failed-export", FileName: "failed.csv", OSSUploadEnabled: true}
	if err := repo.Create(record, exportRecordTestItems()); err != nil {
		t.Fatalf("create OSS export: %v", err)
	}
	if err := repo.MarkFailed(record.ID, "OSS 配置无效"); err != nil {
		t.Fatalf("mark OSS export failed: %v", err)
	}

	stored, err := repo.GetByID(record.ID)
	if err != nil {
		t.Fatalf("read failed export: %v", err)
	}
	if stored.Status != ExportStatusFailed || stored.DownloadReady || stored.FailedCount != 2 {
		t.Fatalf("unexpected failed export state: %#v", stored)
	}
	items, err := repo.ListItems(record.ID)
	if err != nil {
		t.Fatalf("list failed export items: %v", err)
	}
	for _, item := range items {
		if item.DownloadStatus != "failed" || item.OSSStatus != "failed" || item.ErrorMessage != "OSS 配置无效" {
			t.Fatalf("pending item was not terminated: %#v", item)
		}
	}
}
