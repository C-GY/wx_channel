package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"wx_channel/internal/database"
	"wx_channel/internal/response"

	"github.com/google/uuid"
)

const (
	creativeRadarUploadURL = "http://120.27.143.111:3002/api/external/upload"
	creativeRadarAPIKey    = "idear-external-2026"
	creativeRadarSource    = "wechat_channel"
	creativeRadarBatchSize = 200
)

// CreativeRadarSyncJob is the page-level progress of one click of
// "同步创意雷达系统". A record in this job is one independently processed CSV.
type CreativeRadarSyncJob struct {
	ID               string     `json:"id,omitempty"`
	Status           string     `json:"status"` // idle, running, completed
	TotalRecords     int        `json:"totalRecords"`
	CompletedRecords int        `json:"completedRecords"`
	SuccessRecords   int        `json:"successRecords"`
	FailedRecords    int        `json:"failedRecords"`
	CurrentRecordID  string     `json:"currentRecordId,omitempty"`
	CurrentFileName  string     `json:"currentFileName,omitempty"`
	StartedAt        *time.Time `json:"startedAt,omitempty"`
	FinishedAt       *time.Time `json:"finishedAt,omitempty"`
	Message          string     `json:"message,omitempty"`
}

type creativeRadarSyncController struct {
	mu     sync.RWMutex
	job    CreativeRadarSyncJob
	client *creativeRadarClient
}

func newCreativeRadarSyncController() *creativeRadarSyncController {
	return &creativeRadarSyncController{
		job: CreativeRadarSyncJob{Status: "idle"},
		client: &creativeRadarClient{
			endpoint: creativeRadarUploadURL,
			apiKey:   creativeRadarAPIKey,
			httpClient: &http.Client{
				Timeout: 35 * time.Second,
			},
		},
	}
}

func (c *creativeRadarSyncController) snapshot() CreativeRadarSyncJob {
	if c == nil {
		return CreativeRadarSyncJob{Status: "idle"}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.job
}

func (c *creativeRadarSyncController) setCurrent(record database.ExportRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.job.CurrentRecordID = record.ID
	c.job.CurrentFileName = record.FileName
}

func (c *creativeRadarSyncController) finishRecord(successful bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.job.CompletedRecords++
	if successful {
		c.job.SuccessRecords++
	} else {
		c.job.FailedRecords++
	}
}

func (c *creativeRadarSyncController) finish() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.job.Status = "completed"
	c.job.CurrentRecordID = ""
	c.job.CurrentFileName = ""
	c.job.FinishedAt = &now
	if c.job.FailedRecords > 0 {
		c.job.Message = fmt.Sprintf("同步完成：成功 %d 个 CSV，失败 %d 个 CSV", c.job.SuccessRecords, c.job.FailedRecords)
	} else {
		c.job.Message = fmt.Sprintf("同步完成：成功 %d 个 CSV", c.job.SuccessRecords)
	}
}

func (h *ExportRecordAPI) startCreativeRadarSync(w http.ResponseWriter) {
	records, err := h.repository.ListCreativeRadarSyncCandidates()
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
		return
	}
	h.startCreativeRadarRecords(w, records)
}

func (h *ExportRecordAPI) startSingleCreativeRadarSync(w http.ResponseWriter, exportRecordID string) {
	record, err := h.repository.GetByID(exportRecordID)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
		return
	}
	if record == nil {
		response.ErrorWithStatus(w, http.StatusNotFound, http.StatusNotFound, "导出记录不存在")
		return
	}
	if record.Status != database.ExportStatusReady {
		response.ErrorWithStatus(w, http.StatusConflict, http.StatusConflict, "CSV 尚未生成完成，暂时不能同步创意雷达")
		return
	}
	if record.CreativeRadarSyncStatus == database.CreativeRadarSyncSuccess {
		now := time.Now()
		response.Success(w, CreativeRadarSyncJob{
			Status:           "completed",
			TotalRecords:     1,
			CompletedRecords: 1,
			SuccessRecords:   1,
			FinishedAt:       &now,
			Message:          "该 CSV 已同步成功，无需重复同步",
		})
		return
	}
	h.startCreativeRadarRecords(w, []database.ExportRecord{*record})
}

func (h *ExportRecordAPI) startCreativeRadarRecords(w http.ResponseWriter, records []database.ExportRecord) {
	controller := h.creativeRadar
	if controller == nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, "创意雷达同步服务未初始化")
		return
	}

	controller.mu.Lock()
	if controller.job.Status == "running" {
		job := controller.job
		controller.mu.Unlock()
		response.ErrorWithStatus(w, http.StatusConflict, http.StatusConflict,
			fmt.Sprintf("同步任务正在进行（%d/%d 个 CSV）", job.CompletedRecords, job.TotalRecords))
		return
	}
	// Re-read under the job lock so two near-simultaneous requests cannot use a
	// stale candidate list and synchronize an already successful CSV twice.
	freshRecords := make([]database.ExportRecord, 0, len(records))
	for _, candidate := range records {
		record, err := h.repository.GetByID(candidate.ID)
		if err != nil {
			controller.mu.Unlock()
			response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
			return
		}
		if record != nil && record.Status == database.ExportStatusReady &&
			record.CreativeRadarSyncStatus != database.CreativeRadarSyncSuccess {
			freshRecords = append(freshRecords, *record)
		}
	}
	records = freshRecords

	now := time.Now()
	job := CreativeRadarSyncJob{
		ID:           uuid.NewString(),
		Status:       "running",
		TotalRecords: len(records),
		StartedAt:    &now,
	}
	if len(records) == 0 {
		job.Status = "completed"
		job.FinishedAt = &now
		job.Message = "没有需要同步的可下载 CSV"
		controller.job = job
		controller.mu.Unlock()
		response.Success(w, job)
		return
	}
	if err := h.repository.PrepareCreativeRadarSync(records); err != nil {
		controller.mu.Unlock()
		response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
		return
	}
	controller.job = job
	controller.mu.Unlock()

	go h.runCreativeRadarSync(records)
	response.Success(w, job)
}

func (h *ExportRecordAPI) runCreativeRadarSync(records []database.ExportRecord) {
	for _, record := range records {
		h.creativeRadar.setCurrent(record)
		successful := h.syncCreativeRadarRecord(record)
		h.creativeRadar.finishRecord(successful)
	}
	h.creativeRadar.finish()
}

func (h *ExportRecordAPI) syncCreativeRadarRecord(record database.ExportRecord) bool {
	items, err := h.repository.ListItems(record.ID)
	if err != nil {
		message := "读取 CSV 数据失败: " + err.Error()
		_ = h.repository.FinishCreativeRadarSync(record.ID, false, 0, record.TotalCount, 0, 0, message)
		return false
	}
	if err := h.repository.BeginCreativeRadarSync(record.ID, len(items)); err != nil {
		_ = h.repository.FinishCreativeRadarSync(record.ID, false, 0, len(items), 0, 0, err.Error())
		return false
	}
	if len(items) == 0 {
		_ = h.repository.FinishCreativeRadarSync(record.ID, false, 0, 0, 0, 0, "CSV 中没有可同步的数据")
		return false
	}

	videos := buildCreativeRadarVideos(record, items)
	completed := 0
	failed := 0
	inserted := 0
	updated := 0
	errorMessages := make([]string, 0)

	for offset := 0; offset < len(videos); offset += creativeRadarBatchSize {
		end := offset + creativeRadarBatchSize
		if end > len(videos) {
			end = len(videos)
		}
		result, uploadErr := h.creativeRadar.client.upload(context.Background(), videos[offset:end])
		completed += end - offset
		if uploadErr != nil {
			failed += end - offset
			errorMessages = append(errorMessages,
				fmt.Sprintf("第 %d-%d 条同步失败: %v", offset+1, end, uploadErr))
		} else {
			inserted += result.Inserted
			updated += result.Updated
			failedInBatch := len(result.Errors)
			if failedInBatch > end-offset {
				failedInBatch = end - offset
			}
			failed += failedInBatch
			for _, itemError := range result.Errors {
				row := offset + itemError.Index + 1
				if row < offset+1 || row > end {
					row = offset + 1
				}
				errorMessages = append(errorMessages,
					fmt.Sprintf("第 %d 条（%s）: %s", row, creativeRadarErrorTitle(items, row-1), itemError.Reason))
			}
		}
		message := limitCreativeRadarSyncError(strings.Join(errorMessages, "；"))
		if progressErr := h.repository.UpdateCreativeRadarSyncProgress(record.ID, completed, failed, inserted, updated, message); progressErr != nil {
			errorMessages = append(errorMessages, "保存同步进度失败: "+progressErr.Error())
		}
	}

	message := limitCreativeRadarSyncError(strings.Join(errorMessages, "；"))
	successful := failed == 0 && message == ""
	if err := h.repository.FinishCreativeRadarSync(record.ID, successful, completed, failed, inserted, updated, message); err != nil {
		return false
	}
	return successful
}

func creativeRadarErrorTitle(items []database.ExportRecordItem, index int) string {
	if index < 0 || index >= len(items) {
		return "未知视频"
	}
	if title := strings.TrimSpace(items[index].Title); title != "" {
		return title
	}
	return items[index].VideoID
}

func limitCreativeRadarSyncError(value string) string {
	const maxRunes = 4000
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes-1]) + "…"
}

type creativeRadarVideo struct {
	Platform      string `json:"platform"`
	Title         string `json:"title"`
	AccountName   string `json:"account_name"`
	VideoURL      string `json:"video_url,omitempty"`
	CoverURL      string `json:"cover_url"`
	LikeCount     int64  `json:"like_count,omitempty"`
	CommentCount  int64  `json:"comment_count,omitempty"`
	FavoriteCount int64  `json:"favorite_count,omitempty"`
	ForwardCount  int64  `json:"forward_count,omitempty"`
	PublishTime   string `json:"publish_time,omitempty"`
	Duration      string `json:"duration,omitempty"`
	ExportID      string `json:"export_id,omitempty"`
}

func buildCreativeRadarVideos(record database.ExportRecord, items []database.ExportRecordItem) []creativeRadarVideo {
	videos := make([]creativeRadarVideo, 0, len(items))
	for _, item := range items {
		videoURL := item.OriginalVideoURL
		if record.OSSUploadEnabled {
			videoURL = item.OSSVideoURL
		}
		videos = append(videos, creativeRadarVideo{
			Platform:      creativeRadarSource,
			Title:         limitExportText(item.Title, 500),
			AccountName:   limitExportText(item.Author, 200),
			VideoURL:      videoURL,
			CoverURL:      item.CoverURL,
			LikeCount:     item.FavCount,
			CommentCount:  item.CommentCount,
			FavoriteCount: item.LikeCount,
			ForwardCount:  item.ForwardCount,
			PublishTime:   item.PublishTime,
			Duration:      formatCreativeRadarDuration(item.DurationMs),
			ExportID:      item.VideoID,
		})
	}
	return videos
}

func formatCreativeRadarDuration(durationMs int64) string {
	if durationMs < 0 {
		durationMs = 0
	}
	totalSeconds := durationMs / 1000
	return fmt.Sprintf("%02d:%02d", totalSeconds/60, totalSeconds%60)
}

type creativeRadarClient struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
}

type creativeRadarUploadError struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

type creativeRadarUploadResult struct {
	Inserted int
	Updated  int
	Errors   []creativeRadarUploadError
}

func (c *creativeRadarClient) upload(parent context.Context, videos []creativeRadarVideo) (creativeRadarUploadResult, error) {
	payload := struct {
		APIKey string               `json:"api_key"`
		Source string               `json:"source"`
		Videos []creativeRadarVideo `json:"videos"`
	}{
		APIKey: c.apiKey,
		Source: creativeRadarSource,
		Videos: videos,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return creativeRadarUploadResult{}, fmt.Errorf("生成请求失败: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return creativeRadarUploadResult{}, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return creativeRadarUploadResult{}, fmt.Errorf("请求创意雷达接口失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return creativeRadarUploadResult{}, fmt.Errorf("读取创意雷达响应失败: %w", err)
	}
	var remote struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Inserted []json.RawMessage          `json:"inserted"`
			Updated  []json.RawMessage          `json:"updated"`
			Errors   []creativeRadarUploadError `json:"errors"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &remote); err != nil {
		return creativeRadarUploadResult{}, fmt.Errorf("创意雷达返回了无效响应（HTTP %d）: %s", resp.StatusCode, limitCreativeRadarSyncError(string(raw)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || remote.Code != http.StatusOK {
		message := strings.TrimSpace(remote.Msg)
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return creativeRadarUploadResult{}, fmt.Errorf("创意雷达接口返回 HTTP %d / code %d: %s", resp.StatusCode, remote.Code, message)
	}
	return creativeRadarUploadResult{
		Inserted: len(remote.Data.Inserted),
		Updated:  len(remote.Data.Updated),
		Errors:   remote.Data.Errors,
	}, nil
}
