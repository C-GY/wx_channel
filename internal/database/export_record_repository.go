package database

import (
	"database/sql"
	"fmt"
	"time"
)

// ExportRecordRepository 持久化 CSV 导出记录及其 OSS 上传状态。
type ExportRecordRepository struct {
	db *sql.DB
}

func NewExportRecordRepository() *ExportRecordRepository {
	return &ExportRecordRepository{db: GetDB()}
}

func (r *ExportRecordRepository) available() error {
	if r == nil || r.db == nil {
		return fmt.Errorf("导出记录数据库未初始化")
	}
	return nil
}

func (r *ExportRecordRepository) Create(record *ExportRecord, items []ExportRecordItem) error {
	if err := r.available(); err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("导出记录不能为空")
	}
	if len(items) == 0 {
		return fmt.Errorf("导出明细不能为空")
	}

	now := time.Now()
	record.TotalCount = len(items)
	record.CreativeRadarSyncStatus = CreativeRadarSyncNotSynced
	record.CreatedAt = now
	record.UpdatedAt = now
	if record.OSSUploadEnabled {
		record.Status = ExportStatusProcessing
		record.CompletedCount = 0
		record.DownloadReady = false
		record.ReadyAt = nil
	} else {
		record.Status = ExportStatusReady
		record.CompletedCount = len(items)
		record.DownloadReady = true
		readyAt := now
		record.ReadyAt = &readyAt
	}

	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("开始创建导出记录事务失败: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		INSERT INTO export_records (
			id, file_name, status, oss_upload_enabled, total_count, completed_count,
			failed_count, error_message, created_at, updated_at, ready_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.FileName, record.Status, record.OSSUploadEnabled,
		record.TotalCount, record.CompletedCount, record.FailedCount, record.ErrorMessage,
		record.CreatedAt, record.UpdatedAt, nullableTime(record.ReadyAt),
	)
	if err != nil {
		return fmt.Errorf("创建导出记录失败: %w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO export_record_items (
			export_record_id, position, video_id, title, author, publish_time,
			original_video_url, oss_video_url, cover_url, duration_ms, file_size,
			like_count, comment_count, fav_count, forward_count, captured_at,
			download_status, download_progress, downloaded_mb, total_mb,
			oss_status, oss_progress, oss_uploaded_bytes, oss_total_bytes,
			oss_object_key, error_message, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("准备导出明细写入失败: %w", err)
	}
	defer stmt.Close()

	for i := range items {
		item := &items[i]
		item.ExportRecordID = record.ID
		item.Position = i
		item.CreatedAt = now
		item.UpdatedAt = now
		if record.OSSUploadEnabled {
			item.DownloadStatus = "pending"
			item.OSSStatus = "pending"
		} else {
			item.DownloadStatus = "not_required"
			item.DownloadProgress = 100
			item.OSSStatus = "not_required"
		}
		_, err = stmt.Exec(
			item.ExportRecordID, item.Position, item.VideoID, item.Title, item.Author,
			item.PublishTime, item.OriginalVideoURL, item.OSSVideoURL, item.CoverURL,
			item.DurationMs, item.FileSize, item.LikeCount, item.CommentCount,
			item.FavCount, item.ForwardCount, item.CapturedAt, item.DownloadStatus,
			item.DownloadProgress, item.DownloadedMB, item.TotalMB, item.OSSStatus,
			item.OSSProgress, item.OSSUploadedBytes, item.OSSTotalBytes,
			item.OSSObjectKey, item.ErrorMessage, item.CreatedAt, item.UpdatedAt,
		)
		if err != nil {
			return fmt.Errorf("创建导出明细失败（video=%s）: %w", item.VideoID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交导出记录失败: %w", err)
	}
	return nil
}

func nullableTime(value *time.Time) interface{} {
	if value == nil || value.IsZero() {
		return nil
	}
	return *value
}

func scanExportRecord(scanner interface{ Scan(...interface{}) error }) (*ExportRecord, error) {
	record := &ExportRecord{}
	var readyAt sql.NullTime
	var creativeRadarSyncedAt sql.NullTime
	if err := scanner.Scan(
		&record.ID, &record.FileName, &record.Status, &record.OSSUploadEnabled,
		&record.TotalCount, &record.CompletedCount, &record.FailedCount,
		&record.ErrorMessage, &record.CreativeRadarSyncStatus,
		&record.CreativeRadarSyncTotal, &record.CreativeRadarSyncCompleted,
		&record.CreativeRadarSyncFailed, &record.CreativeRadarInserted,
		&record.CreativeRadarUpdated, &record.CreativeRadarSyncError,
		&record.CreatedAt, &record.UpdatedAt, &readyAt, &creativeRadarSyncedAt,
	); err != nil {
		return nil, err
	}
	if readyAt.Valid {
		record.ReadyAt = &readyAt.Time
	}
	if creativeRadarSyncedAt.Valid {
		record.CreativeRadarSyncedAt = &creativeRadarSyncedAt.Time
	}
	record.DownloadReady = record.Status == ExportStatusReady
	return record, nil
}

const exportRecordSelect = `
	SELECT id, file_name, status, oss_upload_enabled, total_count, completed_count,
		failed_count, COALESCE(error_message, ''), creative_radar_sync_status,
		creative_radar_sync_total, creative_radar_sync_completed, creative_radar_sync_failed,
		creative_radar_inserted, creative_radar_updated, COALESCE(creative_radar_sync_error, ''),
		created_at, updated_at, ready_at, creative_radar_synced_at
	FROM export_records`

func (r *ExportRecordRepository) GetByID(id string) (*ExportRecord, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	record, err := scanExportRecord(r.db.QueryRow(exportRecordSelect+" WHERE id = ?", id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取导出记录失败: %w", err)
	}
	return record, nil
}

func (r *ExportRecordRepository) List(page, pageSize int) (*PagedResult[ExportRecord], error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	var total int64
	if err := r.db.QueryRow("SELECT COUNT(*) FROM export_records").Scan(&total); err != nil {
		return nil, fmt.Errorf("统计导出记录失败: %w", err)
	}
	rows, err := r.db.Query(exportRecordSelect+" ORDER BY created_at DESC LIMIT ? OFFSET ?", pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, fmt.Errorf("查询导出记录失败: %w", err)
	}
	defer rows.Close()

	items := make([]ExportRecord, 0)
	for rows.Next() {
		record, scanErr := scanExportRecord(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("读取导出记录行失败: %w", scanErr)
		}
		items = append(items, *record)
	}
	return NewPagedResult(items, total, page, pageSize), rows.Err()
}

// ListCreativeRadarSyncCandidates returns ready CSV records that have not yet
// synchronized successfully. Failed records are intentionally included so the
// page-level sync action also acts as a retry.
func (r *ExportRecordRepository) ListCreativeRadarSyncCandidates() ([]ExportRecord, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(exportRecordSelect+`
		WHERE status = ? AND creative_radar_sync_status <> ?
		ORDER BY created_at ASC`, ExportStatusReady, CreativeRadarSyncSuccess)
	if err != nil {
		return nil, fmt.Errorf("查询待同步创意雷达的导出记录失败: %w", err)
	}
	defer rows.Close()

	records := make([]ExportRecord, 0)
	for rows.Next() {
		record, scanErr := scanExportRecord(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("读取待同步创意雷达的导出记录失败: %w", scanErr)
		}
		records = append(records, *record)
	}
	return records, rows.Err()
}

// PrepareCreativeRadarSync marks every selected CSV as queued in one
// transaction, so users can distinguish waiting records from unsynchronized
// records while the batch is running.
func (r *ExportRecordRepository) PrepareCreativeRadarSync(records []ExportRecord) error {
	if err := r.available(); err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("开始准备创意雷达同步失败: %w", err)
	}
	defer tx.Rollback()

	now := time.Now()
	for _, record := range records {
		if _, err := tx.Exec(`
			UPDATE export_records SET creative_radar_sync_status = ?,
				creative_radar_sync_total = total_count, creative_radar_sync_completed = 0,
				creative_radar_sync_failed = 0, creative_radar_inserted = 0,
				creative_radar_updated = 0, creative_radar_sync_error = '',
				creative_radar_synced_at = NULL, updated_at = ?
			WHERE id = ? AND status = ?`,
			CreativeRadarSyncPending, now, record.ID, ExportStatusReady); err != nil {
			return fmt.Errorf("准备导出记录 %s 的创意雷达同步失败: %w", record.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交创意雷达同步准备状态失败: %w", err)
	}
	return nil
}

func (r *ExportRecordRepository) BeginCreativeRadarSync(exportRecordID string, total int) error {
	if err := r.available(); err != nil {
		return err
	}
	result, err := r.db.Exec(`
		UPDATE export_records SET creative_radar_sync_status = ?,
			creative_radar_sync_total = ?, creative_radar_sync_completed = 0,
			creative_radar_sync_failed = 0, creative_radar_inserted = 0,
			creative_radar_updated = 0, creative_radar_sync_error = '',
			creative_radar_synced_at = NULL, updated_at = ?
		WHERE id = ? AND status = ?`,
		CreativeRadarSyncing, total, time.Now(), exportRecordID, ExportStatusReady)
	if err != nil {
		return fmt.Errorf("标记创意雷达同步开始失败: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("可下载的导出记录不存在: %s", exportRecordID)
	}
	return nil
}

func (r *ExportRecordRepository) UpdateCreativeRadarSyncProgress(exportRecordID string, completed, failed, inserted, updated int, message string) error {
	if err := r.available(); err != nil {
		return err
	}
	_, err := r.db.Exec(`
		UPDATE export_records SET creative_radar_sync_completed = ?,
			creative_radar_sync_failed = ?, creative_radar_inserted = ?,
			creative_radar_updated = ?, creative_radar_sync_error = ?, updated_at = ?
		WHERE id = ?`, completed, failed, inserted, updated, message, time.Now(), exportRecordID)
	if err != nil {
		return fmt.Errorf("更新创意雷达同步进度失败: %w", err)
	}
	return nil
}

func (r *ExportRecordRepository) FinishCreativeRadarSync(exportRecordID string, successful bool, completed, failed, inserted, updated int, message string) error {
	if err := r.available(); err != nil {
		return err
	}
	status := CreativeRadarSyncFailed
	var syncedAt interface{}
	if successful {
		status = CreativeRadarSyncSuccess
		syncedAt = time.Now()
	}
	result, err := r.db.Exec(`
		UPDATE export_records SET creative_radar_sync_status = ?,
			creative_radar_sync_completed = ?, creative_radar_sync_failed = ?,
			creative_radar_inserted = ?, creative_radar_updated = ?,
			creative_radar_sync_error = ?, creative_radar_synced_at = ?, updated_at = ?
		WHERE id = ?`, status, completed, failed, inserted, updated, message,
		syncedAt, time.Now(), exportRecordID)
	if err != nil {
		return fmt.Errorf("写入创意雷达同步结果失败: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("导出记录不存在: %s", exportRecordID)
	}
	return nil
}

// RecoverInterruptedCreativeRadarSync makes an interrupted app run retryable
// after restart instead of leaving records permanently in a syncing state.
func (r *ExportRecordRepository) RecoverInterruptedCreativeRadarSync() error {
	if err := r.available(); err != nil {
		return err
	}
	_, err := r.db.Exec(`
		UPDATE export_records SET creative_radar_sync_status = ?,
			creative_radar_sync_error = ?, updated_at = ?
		WHERE creative_radar_sync_status IN (?, ?)`, CreativeRadarSyncFailed,
		"应用已重启，上一次同步被中断，请重新点击同步", time.Now(),
		CreativeRadarSyncPending, CreativeRadarSyncing)
	if err != nil {
		return fmt.Errorf("恢复中断的创意雷达同步状态失败: %w", err)
	}
	return nil
}

func (r *ExportRecordRepository) Stats() (*ExportRecordStats, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	stats := &ExportRecordStats{}
	err := r.db.QueryRow(`
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN status = 'processing' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'ready' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0)
		FROM export_records`,
	).Scan(&stats.Total, &stats.Processing, &stats.Ready, &stats.Failed)
	if err != nil {
		return nil, fmt.Errorf("统计导出记录状态失败: %w", err)
	}
	return stats, nil
}

func scanExportRecordItem(scanner interface{ Scan(...interface{}) error }) (*ExportRecordItem, error) {
	item := &ExportRecordItem{}
	err := scanner.Scan(
		&item.ExportRecordID, &item.Position, &item.VideoID, &item.Title, &item.Author,
		&item.PublishTime, &item.OriginalVideoURL, &item.OSSVideoURL, &item.CoverURL,
		&item.DurationMs, &item.FileSize, &item.LikeCount, &item.CommentCount,
		&item.FavCount, &item.ForwardCount, &item.CapturedAt, &item.DownloadStatus,
		&item.DownloadProgress, &item.DownloadedMB, &item.TotalMB, &item.OSSStatus,
		&item.OSSProgress, &item.OSSUploadedBytes, &item.OSSTotalBytes,
		&item.OSSObjectKey, &item.ErrorMessage, &item.CreatedAt, &item.UpdatedAt,
	)
	return item, err
}

const exportItemSelect = `
	SELECT export_record_id, position, video_id, title, author, publish_time,
		original_video_url, oss_video_url, cover_url, duration_ms, file_size,
		like_count, comment_count, fav_count, forward_count, captured_at,
		download_status, download_progress, downloaded_mb, total_mb,
		oss_status, oss_progress, oss_uploaded_bytes, oss_total_bytes,
		oss_object_key, error_message, created_at, updated_at
	FROM export_record_items`

func (r *ExportRecordRepository) ListItems(exportRecordID string) ([]ExportRecordItem, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(exportItemSelect+" WHERE export_record_id = ? ORDER BY position ASC", exportRecordID)
	if err != nil {
		return nil, fmt.Errorf("查询导出明细失败: %w", err)
	}
	defer rows.Close()
	items := make([]ExportRecordItem, 0)
	for rows.Next() {
		item, scanErr := scanExportRecordItem(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("读取导出明细失败: %w", scanErr)
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

type ExportItemProgressUpdate struct {
	DownloadStatus   string
	DownloadProgress float64
	DownloadedMB     float64
	TotalMB          float64
	FileSize         int64
	OSSStatus        string
	OSSProgress      float64
	OSSUploadedBytes int64
	OSSTotalBytes    int64
	OSSObjectKey     string
	OSSVideoURL      string
	ErrorMessage     string
}

func (r *ExportRecordRepository) UpdateItemProgress(exportRecordID, videoID string, update ExportItemProgressUpdate) error {
	if err := r.available(); err != nil {
		return err
	}
	result, err := r.db.Exec(`
		UPDATE export_record_items SET
			download_status = ?, download_progress = ?, downloaded_mb = ?, total_mb = ?,
			file_size = CASE WHEN ? > 0 THEN ? ELSE file_size END,
			oss_status = ?, oss_progress = ?, oss_uploaded_bytes = ?, oss_total_bytes = ?,
			oss_object_key = ?, oss_video_url = ?, error_message = ?, updated_at = ?
		WHERE export_record_id = ? AND video_id = ?`,
		update.DownloadStatus, update.DownloadProgress, update.DownloadedMB, update.TotalMB,
		update.FileSize, update.FileSize, update.OSSStatus, update.OSSProgress,
		update.OSSUploadedBytes, update.OSSTotalBytes, update.OSSObjectKey,
		update.OSSVideoURL, update.ErrorMessage, time.Now(), exportRecordID, videoID,
	)
	if err != nil {
		return fmt.Errorf("更新导出明细进度失败: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("未找到导出明细: export=%s video=%s", exportRecordID, videoID)
	}
	return r.RefreshStatus(exportRecordID)
}

func (r *ExportRecordRepository) RefreshStatus(exportRecordID string) error {
	if err := r.available(); err != nil {
		return err
	}
	var total, completed, failed, terminal int
	err := r.db.QueryRow(`
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN oss_status = 'done' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN download_status = 'failed' OR oss_status = 'failed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN oss_status IN ('done', 'failed') OR download_status = 'failed' THEN 1 ELSE 0 END), 0)
		FROM export_record_items WHERE export_record_id = ?`, exportRecordID,
	).Scan(&total, &completed, &failed, &terminal)
	if err != nil {
		return fmt.Errorf("刷新导出记录状态失败: %w", err)
	}

	status := ExportStatusProcessing
	var readyAt interface{}
	if total > 0 && completed == total {
		status = ExportStatusReady
		readyAt = time.Now()
	} else if total > 0 && terminal == total && failed > 0 {
		status = ExportStatusFailed
	}
	_, err = r.db.Exec(`
		UPDATE export_records SET status = ?, total_count = ?, completed_count = ?,
			failed_count = ?, updated_at = ?, ready_at = CASE WHEN ? IS NOT NULL THEN ? ELSE ready_at END
		WHERE id = ?`,
		status, total, completed, failed, time.Now(), readyAt, readyAt, exportRecordID,
	)
	if err != nil {
		return fmt.Errorf("写入导出记录状态失败: %w", err)
	}
	return nil
}

func (r *ExportRecordRepository) MarkFailed(exportRecordID, message string) error {
	if err := r.available(); err != nil {
		return err
	}
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("开始标记导出失败事务失败: %w", err)
	}
	defer tx.Rollback()

	var status string
	if err := tx.QueryRow("SELECT status FROM export_records WHERE id = ?", exportRecordID).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("导出记录不存在: %s", exportRecordID)
		}
		return fmt.Errorf("读取导出记录失败: %w", err)
	}
	if status == ExportStatusReady {
		return nil
	}

	now := time.Now()
	if _, err := tx.Exec(`
		UPDATE export_record_items SET
			download_status = CASE WHEN download_status = 'done' THEN download_status ELSE 'failed' END,
			oss_status = CASE WHEN oss_status = 'done' THEN oss_status ELSE 'failed' END,
			error_message = CASE WHEN oss_status = 'done' THEN error_message ELSE ? END,
			updated_at = ?
		WHERE export_record_id = ? AND oss_status <> 'done'`, message, now, exportRecordID); err != nil {
		return fmt.Errorf("标记导出明细失败状态失败: %w", err)
	}

	result, err := tx.Exec(`
		UPDATE export_records SET status = ?, error_message = ?,
			completed_count = (SELECT COUNT(*) FROM export_record_items WHERE export_record_id = ? AND oss_status = 'done'),
			failed_count = (SELECT COUNT(*) FROM export_record_items WHERE export_record_id = ? AND oss_status = 'failed'),
			updated_at = ? WHERE id = ?`,
		ExportStatusFailed, message, exportRecordID, exportRecordID, now, exportRecordID,
	)
	if err != nil {
		return fmt.Errorf("标记导出记录失败状态失败: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("导出记录不存在: %s", exportRecordID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交导出失败状态失败: %w", err)
	}
	return nil
}

func (r *ExportRecordRepository) ListOSSUploadQueue(limit int) ([]OSSUploadQueueItem, error) {
	if err := r.available(); err != nil {
		return nil, err
	}
	if limit < 1 {
		limit = 100000
	}
	if limit > 100000 {
		limit = 100000
	}
	rows, err := r.db.Query(`
		SELECT i.export_record_id, i.position, i.video_id, i.title, i.author, i.publish_time,
			i.original_video_url, i.oss_video_url, i.cover_url, i.duration_ms, i.file_size,
			i.like_count, i.comment_count, i.fav_count, i.forward_count, i.captured_at,
			i.download_status, i.download_progress, i.downloaded_mb, i.total_mb,
			i.oss_status, i.oss_progress, i.oss_uploaded_bytes, i.oss_total_bytes,
			i.oss_object_key, i.error_message, i.created_at, i.updated_at,
			r.file_name, r.status
		FROM export_record_items i
		JOIN export_records r ON r.id = i.export_record_id
		WHERE r.oss_upload_enabled = 1
		ORDER BY i.updated_at DESC, i.position ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询 OSS 上传队列失败: %w", err)
	}
	defer rows.Close()

	items := make([]OSSUploadQueueItem, 0)
	for rows.Next() {
		var item OSSUploadQueueItem
		err := rows.Scan(
			&item.ExportRecordID, &item.Position, &item.VideoID, &item.Title, &item.Author,
			&item.PublishTime, &item.OriginalVideoURL, &item.OSSVideoURL, &item.CoverURL,
			&item.DurationMs, &item.FileSize, &item.LikeCount, &item.CommentCount,
			&item.FavCount, &item.ForwardCount, &item.CapturedAt, &item.DownloadStatus,
			&item.DownloadProgress, &item.DownloadedMB, &item.TotalMB, &item.OSSStatus,
			&item.OSSProgress, &item.OSSUploadedBytes, &item.OSSTotalBytes,
			&item.OSSObjectKey, &item.ErrorMessage, &item.CreatedAt, &item.UpdatedAt,
			&item.ExportFileName, &item.ExportStatus,
		)
		if err != nil {
			return nil, fmt.Errorf("读取 OSS 上传队列失败: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
