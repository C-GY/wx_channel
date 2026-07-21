package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"wx_channel/internal/database"
	"wx_channel/internal/response"

	"github.com/google/uuid"
)

const maxExportRecordItems = 100000

// ExportRecordAPI 提供批量 CSV 导出记录、延迟下载和 OSS 上传队列接口。
type ExportRecordAPI struct {
	repository    *database.ExportRecordRepository
	now           func() time.Time
	creativeRadar *creativeRadarSyncController
}

func NewExportRecordAPI() *ExportRecordAPI {
	repository := database.NewExportRecordRepository()
	_ = repository.RecoverInterruptedCreativeRadarSync()
	return &ExportRecordAPI{
		repository:    repository,
		now:           time.Now,
		creativeRadar: newCreativeRadarSyncController(),
	}
}

func (h *ExportRecordAPI) HandleExportRecords(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/export-records")
	path = strings.Trim(path, "/")
	if path == "" {
		switch r.Method {
		case http.MethodGet:
			h.listExportRecords(w, r)
		case http.MethodPost:
			h.createExportRecord(w, r)
		default:
			response.ErrorWithStatus(w, http.StatusMethodNotAllowed, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}
	if path == "creative-radar-sync" {
		if r.Method != http.MethodPost {
			response.ErrorWithStatus(w, http.StatusMethodNotAllowed, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		h.startCreativeRadarSync(w)
		return
	}

	parts := strings.Split(path, "/")
	exportRecordID := strings.TrimSpace(parts[0])
	if exportRecordID == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, http.StatusBadRequest, "导出记录 ID 不能为空")
		return
	}
	if len(parts) == 2 && parts[1] == "csv" {
		if r.Method != http.MethodGet {
			response.ErrorWithStatus(w, http.StatusMethodNotAllowed, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		h.downloadCSV(w, exportRecordID)
		return
	}
	if len(parts) == 2 && parts[1] == "fail" {
		if r.Method != http.MethodPost {
			response.ErrorWithStatus(w, http.StatusMethodNotAllowed, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		h.markExportFailed(w, r, exportRecordID)
		return
	}
	if len(parts) == 2 && parts[1] == "creative-radar-sync" {
		if r.Method != http.MethodPost {
			response.ErrorWithStatus(w, http.StatusMethodNotAllowed, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		h.startSingleCreativeRadarSync(w, exportRecordID)
		return
	}
	if len(parts) != 1 || r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusNotFound, http.StatusNotFound, "导出记录接口不存在")
		return
	}
	h.getExportRecord(w, exportRecordID)
}

func (h *ExportRecordAPI) HandleOSSUploadQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := h.repository.ListOSSUploadQueue(limit)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
		return
	}
	stats := map[string]int{"total": len(items), "pending": 0, "uploading": 0, "done": 0, "failed": 0}
	for _, item := range items {
		switch item.OSSStatus {
		case "done":
			stats["done"]++
		case "failed":
			stats["failed"]++
		case "uploading", "retrying":
			stats["uploading"]++
		default:
			stats["pending"]++
		}
	}
	response.Success(w, map[string]interface{}{"items": items, "stats": stats})
}

func (h *ExportRecordAPI) createExportRecord(w http.ResponseWriter, r *http.Request) {
	var request struct {
		FileName         string                      `json:"fileName"`
		OSSUploadEnabled bool                        `json:"ossUploadEnabled"`
		Videos           []database.ExportRecordItem `json:"videos"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024*1024))
	if err := decoder.Decode(&request); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, http.StatusBadRequest, "解析导出数据失败: "+err.Error())
		return
	}
	if len(request.Videos) == 0 {
		response.ErrorWithStatus(w, http.StatusBadRequest, http.StatusBadRequest, "至少需要一条导出视频")
		return
	}
	if len(request.Videos) > maxExportRecordItems {
		response.ErrorWithStatus(w, http.StatusBadRequest, http.StatusBadRequest, fmt.Sprintf("单次最多导出 %d 条视频", maxExportRecordItems))
		return
	}

	now := h.now()
	fileName := normalizeExportFileName(request.FileName, now)
	seenVideoIDs := make(map[string]struct{}, len(request.Videos))
	for i := range request.Videos {
		item := &request.Videos[i]
		item.VideoID = strings.TrimSpace(item.VideoID)
		if item.VideoID == "" {
			response.ErrorWithStatus(w, http.StatusBadRequest, http.StatusBadRequest, fmt.Sprintf("第 %d 条视频缺少视频 ID", i+1))
			return
		}
		if _, exists := seenVideoIDs[item.VideoID]; exists {
			response.ErrorWithStatus(w, http.StatusBadRequest, http.StatusBadRequest, "导出列表包含重复视频 ID: "+item.VideoID)
			return
		}
		seenVideoIDs[item.VideoID] = struct{}{}
		item.Title = limitExportText(item.Title, 10000)
		item.Author = limitExportText(item.Author, 1000)
		item.PublishTime = normalizeExportDateTime(item.PublishTime, "")
		item.CapturedAt = normalizeExportDateTime(item.CapturedAt, now.Format("2006-01-02 15:04:05"))
		item.OriginalVideoURL = limitExportText(item.OriginalVideoURL, 20000)
		item.CoverURL = limitExportText(item.CoverURL, 20000)
		if item.DurationMs < 0 {
			item.DurationMs = 0
		}
		if item.FileSize < 0 {
			item.FileSize = 0
		}
		if item.LikeCount < 0 {
			item.LikeCount = 0
		}
		if item.CommentCount < 0 {
			item.CommentCount = 0
		}
		if item.FavCount < 0 {
			item.FavCount = 0
		}
		if item.ForwardCount < 0 {
			item.ForwardCount = 0
		}
	}

	record := &database.ExportRecord{
		ID:               uuid.NewString(),
		FileName:         fileName,
		OSSUploadEnabled: request.OSSUploadEnabled,
	}
	if err := h.repository.Create(record, request.Videos); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(w, record)
}

func limitExportText(value string, maxLength int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > maxLength {
		return string(runes[:maxLength])
	}
	return value
}

func normalizeExportFileName(value string, now time.Time) string {
	value = strings.Map(func(character rune) rune {
		if character < 32 || character == 127 {
			return -1
		}
		switch character {
		case '"', ';', '/', '\\':
			return '_'
		default:
			return character
		}
	}, value)
	value = strings.TrimSpace(filepath.Base(value))
	if value == "" || value == "." || value == ".." {
		value = "batch_videos_" + now.Format("2006-01-02_15-04-05") + ".csv"
	}
	if !strings.EqualFold(filepath.Ext(value), ".csv") {
		value += ".csv"
	}
	if runes := []rune(value); len(runes) > 240 {
		value = string(runes[:236]) + ".csv"
	}
	return value
}

func normalizeExportDateTime(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	formats := []struct {
		layout      string
		hasTimeZone bool
	}{
		{layout: time.RFC3339Nano, hasTimeZone: true},
		{layout: time.RFC3339, hasTimeZone: true},
		{layout: "2006-01-02 15:04:05"},
		{layout: "2006-01-02T15:04:05"},
	}
	for _, format := range formats {
		var parsed time.Time
		var err error
		if format.hasTimeZone {
			parsed, err = time.Parse(format.layout, value)
		} else {
			// 无时区的“年-月-日 时:分:秒”已是页面本地时间，不能再当 UTC 转换。
			parsed, err = time.ParseInLocation(format.layout, value, time.Local)
		}
		if err == nil {
			return parsed.In(time.Local).Format("2006-01-02 15:04:05")
		}
	}
	return fallback
}

func (h *ExportRecordAPI) listExportRecords(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	result, err := h.repository.List(page, pageSize)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
		return
	}
	stats, err := h.repository.Stats()
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(w, map[string]interface{}{
		"items":                result.Items,
		"total":                result.Total,
		"page":                 result.Page,
		"pageSize":             result.PageSize,
		"totalPages":           result.TotalPages,
		"stats":                stats,
		"creativeRadarSyncJob": h.creativeRadar.snapshot(),
	})
}

func (h *ExportRecordAPI) getExportRecord(w http.ResponseWriter, exportRecordID string) {
	record, err := h.repository.GetByID(exportRecordID)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
		return
	}
	if record == nil {
		response.ErrorWithStatus(w, http.StatusNotFound, http.StatusNotFound, "导出记录不存在")
		return
	}
	items, err := h.repository.ListItems(exportRecordID)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(w, map[string]interface{}{"record": record, "items": items})
}

func (h *ExportRecordAPI) markExportFailed(w http.ResponseWriter, r *http.Request, exportRecordID string) {
	var request struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&request)
	message := strings.TrimSpace(request.Message)
	if message == "" {
		message = "启动关联的批量下载任务失败"
	}
	if err := h.repository.MarkFailed(exportRecordID, limitExportText(message, 2000)); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(w, map[string]bool{"failed": true})
}

func (h *ExportRecordAPI) downloadCSV(w http.ResponseWriter, exportRecordID string) {
	record, err := h.repository.GetByID(exportRecordID)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
		return
	}
	if record == nil {
		response.ErrorWithStatus(w, http.StatusNotFound, http.StatusNotFound, "导出记录不存在")
		return
	}
	if !record.DownloadReady {
		message := "CSV 尚未就绪，需要等待全部视频上传 OSS 完成"
		if record.Status == database.ExportStatusFailed {
			message = "CSV 无法下载：存在下载或 OSS 上传失败的视频"
		}
		response.ErrorWithStatus(w, http.StatusConflict, http.StatusConflict, message)
		return
	}
	items, err := h.repository.ListItems(exportRecordID)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
		return
	}
	data, err := buildExportRecordCSV(record, items)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	escapedName := url.PathEscape(record.FileName)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"; filename*=UTF-8''%s", record.FileName, escapedName))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func buildExportRecordCSV(record *database.ExportRecord, items []database.ExportRecordItem) ([]byte, error) {
	var builder strings.Builder
	builder.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(&builder)
	writer.UseCRLF = true
	videoLinkHeader := "视频链接（原始地址）"
	if record.OSSUploadEnabled {
		videoLinkHeader = "视频链接（OSS地址）"
	}
	headers := []string{
		"视频ID", "视频标题", "作者昵称", "发布时间", videoLinkHeader,
		"视频封面链接", "视频时长", "文件大小", "点赞数", "评论数",
		"收藏数", "转发数", "数据采集时间",
	}
	if err := writer.Write(headers); err != nil {
		return nil, fmt.Errorf("写入 CSV 表头失败: %w", err)
	}
	for _, item := range items {
		videoURL := item.OriginalVideoURL
		if record.OSSUploadEnabled {
			videoURL = item.OSSVideoURL
		}
		row := []string{
			sanitizeExportCSVCell(item.VideoID),
			sanitizeExportCSVCell(item.Title),
			sanitizeExportCSVCell(item.Author),
			sanitizeExportCSVCell(item.PublishTime),
			sanitizeExportCSVCell(videoURL),
			sanitizeExportCSVCell(item.CoverURL),
			formatExportDuration(item.DurationMs),
			formatExportFileSize(item.FileSize),
			// 视频号主页列表返回的 likeCount / favCount 与页面展示语义相反，
			// CSV 按客户看到的“点赞数 / 收藏数”顺序对调输出。
			strconv.FormatInt(item.FavCount, 10),
			strconv.FormatInt(item.CommentCount, 10),
			strconv.FormatInt(item.LikeCount, 10),
			strconv.FormatInt(item.ForwardCount, 10),
			sanitizeExportCSVCell(item.CapturedAt),
		}
		if err := writer.Write(row); err != nil {
			return nil, fmt.Errorf("写入 CSV 数据失败: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("生成 CSV 失败: %w", err)
	}
	return []byte(builder.String()), nil
}

func sanitizeExportCSVCell(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}

func formatExportDuration(durationMs int64) string {
	if durationMs < 0 {
		durationMs = 0
	}
	totalSeconds := durationMs / 1000
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}

func formatExportFileSize(sizeBytes int64) string {
	if sizeBytes < 0 {
		sizeBytes = 0
	}
	return fmt.Sprintf("%.2f MB", float64(sizeBytes)/(1024*1024))
}
