package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"wx_channel/internal/config"
	"wx_channel/internal/database"
	"wx_channel/internal/response"
	"wx_channel/internal/services"
	"wx_channel/internal/utils"

	"github.com/GopeedLab/gopeed/pkg/base"
	"github.com/qtgolang/SunnyNet/SunnyNet"
)

var errBatchPaused = errors.New("batch download paused")

type batchOSSUploader interface {
	UploadVideo(ctx context.Context, filePath, materialID string, scrapedDate time.Time) (services.OSSUploadResult, error)
}

type batchOSSProgressUploader interface {
	UploadVideoWithProgress(
		ctx context.Context,
		filePath, materialID string,
		scrapedDate time.Time,
		onProgress func(uploaded, total int64),
	) (services.OSSUploadResult, error)
}

type batchQueueItem struct {
	ID              string
	Tasks           []BatchTask
	ForceRedownload bool
	OSSUploader     batchOSSUploader
}

// BatchHandler 批量下载处理器
type BatchHandler struct {
	downloadService  *services.DownloadRecordService
	settingsRepo     *database.SettingsRepository
	exportRepo       *database.ExportRecordRepository
	gopeedService    *services.GopeedService // Injected Gopeed Service
	mu               sync.RWMutex
	tasks            []BatchTask
	currentBatchID   string
	pendingBatches   []batchQueueItem
	completedBatches map[string][]BatchTask
	completedOrder   []string
	running          bool
	cancelFunc       context.CancelFunc // 用于取消时立即中断下载
	ossUploader      batchOSSUploader   // 当前批次使用的 OSS 上传器（不对前端暴露凭证）
}

// BatchTask 批量下载任务
type BatchTask struct {
	ID              string            `json:"id"`
	URL             string            `json:"url"`
	Title           string            `json:"title"`
	AuthorName      string            `json:"authorName,omitempty"` // 兼容旧格式
	Author          string            `json:"author,omitempty"`     // 新格式
	Headers         map[string]string `json:"headers,omitempty"`
	UserAgent       string            `json:"userAgent,omitempty"`
	SourceURL       string            `json:"sourceUrl,omitempty"`
	Key             string            `json:"key,omitempty"`             // 加密密钥（新方式，后端生成解密数组）
	DecryptorPrefix string            `json:"decryptorPrefix,omitempty"` // 解密前缀（旧方式，前端传递）
	PrefixLen       int               `json:"prefixLen,omitempty"`
	FileFormat      string            `json:"fileFormat,omitempty"`
	Status          string            `json:"status"` // pending, downloading, done, failed
	Error           string            `json:"error,omitempty"`
	Progress        float64           `json:"progress,omitempty"`
	DownloadedMB    float64           `json:"downloadedMB,omitempty"`
	TotalMB         float64           `json:"totalMB,omitempty"`
	// 额外字段用于下载记录（批量下载JSON格式）
	Duration   string `json:"duration,omitempty"`   // 时长字符串，如 "00:22"
	SizeMB     string `json:"sizeMB,omitempty"`     // 大小字符串，如 "28.77MB"
	Cover      string `json:"cover,omitempty"`      // 封面URL（批量下载格式）
	Resolution string `json:"resolution,omitempty"` // 分辨率
	PageSource string `json:"pageSource,omitempty"` // 页面来源（batch_console/batch_feed/batch_home等）
	// 统计数据字段
	PlayCount    string `json:"playCount,omitempty"`    // 播放量（字符串格式）
	LikeCount    string `json:"likeCount,omitempty"`    // 点赞数（字符串格式）
	CommentCount string `json:"commentCount,omitempty"` // 评论数（字符串格式）
	FavCount     string `json:"favCount,omitempty"`     // 收藏数（字符串格式）
	ForwardCount string `json:"forwardCount,omitempty"` // 转发数（字符串格式）
	CreateTime   string `json:"createTime,omitempty"`   // 创建时间
	CapturedAt   string `json:"capturedAt,omitempty"`   // 数据采集时间（OSS 对象日期优先使用此字段）
	IPRegion     string `json:"ipRegion,omitempty"`     // IP所在地
	// 兼容数据库导出格式
	VideoURL   string `json:"videoUrl,omitempty"`   // 视频URL（数据库格式）
	CoverURL   string `json:"coverUrl,omitempty"`   // 封面URL（数据库格式）
	DecryptKey string `json:"decryptKey,omitempty"` // 解密密钥（数据库格式）
	DurationMs int64  `json:"durationMs,omitempty"` // 时长毫秒（数据库格式，字段名为duration但类型是int64）
	Size       int64  `json:"size,omitempty"`       // 大小字节（数据库格式）
	// 关联 CSV 导出记录；仅由“勾选 OSS 后导出 CSV”流程设置。
	ExportRecordID string `json:"exportRecordId,omitempty"`
	DownloadStatus string `json:"downloadStatus,omitempty"`
	// OSS 同步上传状态；访问凭证只保存在本地设置中，不进入任务或失败清单。
	OSSUploadEnabled bool    `json:"ossUploadEnabled,omitempty"`
	OSSStatus        string  `json:"ossStatus,omitempty"`
	OSSProgress      float64 `json:"ossProgress,omitempty"`
	OSSUploadedBytes int64   `json:"ossUploadedBytes,omitempty"`
	OSSTotalBytes    int64   `json:"ossTotalBytes,omitempty"`
	OSSObjectKey     string  `json:"ossObjectKey,omitempty"`
	OSSError         string  `json:"ossError,omitempty"`
	OSSURL           string  `json:"-"`
	GopeedTaskID     string  `json:"-"`
	TempPath         string  `json:"-"`
	FinalPath        string  `json:"-"`
}

// GetAuthor 获取作者名称，兼容两种字段
func (t *BatchTask) GetAuthor() string {
	if t.Author != "" {
		return t.Author
	}
	return t.AuthorName
}

// GetURL 获取视频URL，兼容两种格式
func (t *BatchTask) GetURL() string {
	if t.URL != "" {
		return t.URL
	}
	return t.VideoURL
}

// GetKey 获取解密密钥，兼容两种格式
func (t *BatchTask) GetKey() string {
	if t.Key != "" {
		return t.Key
	}
	return t.DecryptKey
}

// Handle implements router.Interceptor
func (h *BatchHandler) Handle(Conn *SunnyNet.HttpConn) bool {
	// Defensive checks
	if h == nil {
		return false
	}
	if Conn == nil || Conn.Request == nil || Conn.Request.URL == nil {
		return false
	}

	if h.HandleBatchOSSConfig(Conn) {
		return true
	}
	// Debug log
	// utils.Info("BatchHandler checking: %s", Conn.Request.URL.Path)

	if h.HandleBatchStart(Conn) {
		return true
	}
	if h.HandleBatchProgress(Conn) {
		return true
	}
	if h.HandleBatchCancel(Conn) {
		return true
	}
	if h.HandleBatchResume(Conn) {
		return true
	}
	if h.HandleBatchClear(Conn) {
		return true
	}
	if h.HandleBatchFailed(Conn) {
		return true
	}
	return false
}

// GetCover 获取封面URL，兼容两种格式
func (t *BatchTask) GetCover() string {
	if t.Cover != "" {
		return t.Cover
	}
	return t.CoverURL
}

// NewBatchHandler 创建批量下载处理器
func NewBatchHandler(cfg *config.Config, gopeedService *services.GopeedService) *BatchHandler {
	return &BatchHandler{
		downloadService:  services.NewDownloadRecordService(),
		settingsRepo:     database.NewSettingsRepository(),
		exportRepo:       database.NewExportRecordRepository(),
		gopeedService:    gopeedService,
		tasks:            make([]BatchTask, 0),
		pendingBatches:   make([]batchQueueItem, 0),
		completedBatches: make(map[string][]BatchTask),
	}
}

func (h *BatchHandler) persistExportTask(task BatchTask) {
	if h == nil || h.exportRepo == nil || strings.TrimSpace(task.ExportRecordID) == "" || strings.TrimSpace(task.ID) == "" {
		return
	}
	errorMessage := firstNonEmpty(task.OSSError, task.Error)
	if err := h.exportRepo.UpdateItemProgress(task.ExportRecordID, task.ID, database.ExportItemProgressUpdate{
		DownloadStatus:   task.DownloadStatus,
		DownloadProgress: task.Progress,
		DownloadedMB:     task.DownloadedMB,
		TotalMB:          task.TotalMB,
		FileSize:         task.Size,
		OSSStatus:        task.OSSStatus,
		OSSProgress:      task.OSSProgress,
		OSSUploadedBytes: task.OSSUploadedBytes,
		OSSTotalBytes:    task.OSSTotalBytes,
		OSSObjectKey:     task.OSSObjectKey,
		OSSVideoURL:      task.OSSURL,
		ErrorMessage:     errorMessage,
	}); err != nil {
		utils.Warn("更新导出记录进度失败: export=%s video=%s error=%v", task.ExportRecordID, task.ID, err)
	}
}

func (h *BatchHandler) markExportFailed(exportRecordID string, err error) {
	if h == nil || h.exportRepo == nil || strings.TrimSpace(exportRecordID) == "" || err == nil {
		return
	}
	if updateErr := h.exportRepo.MarkFailed(exportRecordID, err.Error()); updateErr != nil {
		utils.Warn("标记导出记录失败状态失败: export=%s error=%v", exportRecordID, updateErr)
	}
}

// getConfig 获取当前配置（动态获取最新配置）
func (h *BatchHandler) getConfig() *config.Config {
	return config.Get()
}

// getDownloadsDir 获取解析后的下载目录
func (h *BatchHandler) getDownloadsDir() (string, error) {
	cfg := h.getConfig()
	return cfg.GetResolvedDownloadsDir()
}

func (h *BatchHandler) authorizeBatchRequest(Conn *SunnyNet.HttpConn) error {
	if h.getConfig() != nil && h.getConfig().SecretToken != "" {
		if Conn.Request.Header.Get("X-Local-Auth") != h.getConfig().SecretToken {
			return fmt.Errorf("unauthorized")
		}
	}
	return nil
}

func (h *BatchHandler) loadBatchOSSUploader() (batchOSSUploader, error) {
	if h.settingsRepo == nil {
		return nil, fmt.Errorf("本地设置服务未初始化")
	}
	accessKeyID, err := h.settingsRepo.Get(database.SettingKeyOSSAccessKeyID)
	if err != nil {
		return nil, fmt.Errorf("读取 OSS_ACCESS_KEY_ID 失败: %w", err)
	}
	accessKeySecret, err := h.settingsRepo.Get(database.SettingKeyOSSAccessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("读取 OSS_ACCESS_KEY_SECRET 失败: %w", err)
	}
	uploader, err := services.NewOSSService(services.DefaultBatchOSSSettings(accessKeyID, accessKeySecret))
	if err != nil {
		return nil, err
	}
	return uploader, nil
}

// HandleBatchOSSConfig manages the two cached OSS credentials. The secret is
// never returned to the page; callers only receive a hasSecret marker.
func (h *BatchHandler) HandleBatchOSSConfig(Conn *SunnyNet.HttpConn) bool {
	if Conn.Request.URL.Path != "/__wx_channels_api/oss_config" {
		return false
	}
	if Conn.Request.Method == http.MethodOptions {
		h.sendSuccessResponse(Conn, map[string]interface{}{"message": "OK"})
		return true
	}
	if err := h.authorizeBatchRequest(Conn); err != nil {
		h.sendErrorResponse(Conn, err)
		return true
	}
	if h.settingsRepo == nil {
		h.sendErrorResponse(Conn, fmt.Errorf("本地设置服务未初始化"))
		return true
	}

	switch Conn.Request.Method {
	case http.MethodGet:
		accessKeyID, err := h.settingsRepo.Get(database.SettingKeyOSSAccessKeyID)
		if err != nil {
			h.sendErrorResponse(Conn, err)
			return true
		}
		accessKeySecret, err := h.settingsRepo.Get(database.SettingKeyOSSAccessKeySecret)
		if err != nil {
			h.sendErrorResponse(Conn, err)
			return true
		}
		h.sendSuccessResponse(Conn, map[string]interface{}{
			"accessKeyId": strings.TrimSpace(accessKeyID),
			"hasSecret":   strings.TrimSpace(accessKeySecret) != "",
		})
		return true

	case http.MethodPost:
		if Conn.Request.Body == nil {
			h.sendErrorResponse(Conn, fmt.Errorf("request body is nil"))
			return true
		}
		body, err := io.ReadAll(io.LimitReader(Conn.Request.Body, 64*1024))
		Conn.Request.Body.Close()
		if err != nil {
			h.sendErrorResponse(Conn, fmt.Errorf("读取 OSS 配置失败: %w", err))
			return true
		}
		var req struct {
			AccessKeyID     string `json:"accessKeyId"`
			AccessKeySecret string `json:"accessKeySecret"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			h.sendErrorResponse(Conn, fmt.Errorf("解析 OSS 配置失败: %w", err))
			return true
		}
		accessKeyID := strings.TrimSpace(req.AccessKeyID)
		accessKeySecret := strings.TrimSpace(req.AccessKeySecret)
		if accessKeySecret == "" {
			storedAccessKeyID, getErr := h.settingsRepo.Get(database.SettingKeyOSSAccessKeyID)
			if getErr != nil {
				h.sendErrorResponse(Conn, getErr)
				return true
			}
			if strings.TrimSpace(storedAccessKeyID) != accessKeyID {
				h.sendErrorResponse(Conn, fmt.Errorf("OSS_ACCESS_KEY_ID 已变更，请同时填写新的 OSS_ACCESS_KEY_SECRET"))
				return true
			}
			accessKeySecret, err = h.settingsRepo.Get(database.SettingKeyOSSAccessKeySecret)
			if err != nil {
				h.sendErrorResponse(Conn, err)
				return true
			}
		}
		if _, err := services.NewOSSService(services.DefaultBatchOSSSettings(accessKeyID, accessKeySecret)); err != nil {
			h.sendErrorResponse(Conn, err)
			return true
		}
		if err := h.settingsRepo.SetOSSConfig(accessKeyID, accessKeySecret); err != nil {
			h.sendErrorResponse(Conn, err)
			return true
		}
		h.sendSuccessResponse(Conn, map[string]interface{}{
			"accessKeyId": accessKeyID,
			"hasSecret":   true,
			"saved":       true,
		})
		return true

	case http.MethodDelete:
		if err := h.settingsRepo.DeleteOSSConfig(); err != nil {
			h.sendErrorResponse(Conn, err)
			return true
		}
		// 正在运行的批次保留其启动时的上传器快照；空闲时立即清除内存中的凭证。
		h.mu.Lock()
		if !h.running && h.cancelFunc == nil {
			h.ossUploader = nil
		}
		h.mu.Unlock()
		h.sendSuccessResponse(Conn, map[string]interface{}{
			"cleared": true,
		})
		return true

	default:
		h.sendErrorResponse(Conn, fmt.Errorf("method not allowed: %s", Conn.Request.Method))
		return true
	}
}

func (h *BatchHandler) batchResumeEnabled() bool {
	cfg := h.getConfig()
	if cfg == nil {
		return true
	}
	return cfg.DownloadResumeEnabled
}

func (h *BatchHandler) cleanupTaskArtifacts(taskID, tempPath string, removeFiles bool) {
	if h.gopeedService != nil && strings.TrimSpace(taskID) != "" {
		if err := h.gopeedService.DeleteTask(taskID, removeFiles); err != nil && !strings.Contains(strings.ToLower(err.Error()), "task not found") {
			utils.Warn("清理 Gopeed 任务失败: %v", err)
		}
	}
	if removeFiles && strings.TrimSpace(tempPath) != "" {
		_ = os.Remove(tempPath)
	}
}

func batchTasksHavePending(tasks []BatchTask) bool {
	for _, task := range tasks {
		if task.Status != "done" && task.Status != "failed" {
			return true
		}
	}
	return false
}

func (h *BatchHandler) enqueueBatch(batch batchQueueItem) (oldTasks []BatchTask, queuePosition int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	busy := h.running || h.cancelFunc != nil || batchTasksHavePending(h.tasks)
	if busy {
		h.pendingBatches = append(h.pendingBatches, batch)
		return nil, len(h.pendingBatches)
	}

	oldTasks = append([]BatchTask(nil), h.tasks...)
	h.tasks = batch.Tasks
	h.currentBatchID = batch.ID
	h.ossUploader = batch.OSSUploader
	h.running = true
	return oldTasks, 0
}

func (h *BatchHandler) promoteNextBatchLocked() (batchQueueItem, bool) {
	if len(h.pendingBatches) == 0 {
		return batchQueueItem{}, false
	}
	queued := h.pendingBatches[0]
	h.pendingBatches = h.pendingBatches[1:]
	h.tasks = queued.Tasks
	h.currentBatchID = queued.ID
	h.ossUploader = queued.OSSUploader
	h.running = true
	return queued, true
}

func (h *BatchHandler) archiveBatchLocked(batchID string, tasks []BatchTask) {
	if strings.TrimSpace(batchID) == "" {
		return
	}
	if h.completedBatches == nil {
		h.completedBatches = make(map[string][]BatchTask)
	}
	if _, exists := h.completedBatches[batchID]; !exists {
		h.completedOrder = append(h.completedOrder, batchID)
	}
	h.completedBatches[batchID] = append([]BatchTask(nil), tasks...)

	const completedBatchLimit = 100
	for len(h.completedOrder) > completedBatchLimit {
		oldestID := h.completedOrder[0]
		h.completedOrder = h.completedOrder[1:]
		delete(h.completedBatches, oldestID)
	}
}

// finishBatchDownload releases the current runner and, only after a terminal
// batch result, promotes the next queued batch. A paused batch keeps ownership
// of the runner so it can be resumed without losing its task state.
func (h *BatchHandler) finishBatchDownload(cancel context.CancelFunc, advanceQueue bool) {
	cancel()

	var next *batchQueueItem
	h.mu.Lock()
	if advanceQueue {
		h.archiveBatchLocked(h.currentBatchID, h.tasks)
	}
	h.running = false
	h.cancelFunc = nil
	h.ossUploader = nil

	if advanceQueue {
		queued, ok := h.promoteNextBatchLocked()
		if ok {
			next = &queued
		}
	}
	h.mu.Unlock()

	if next != nil {
		utils.Info("🚀 [批量下载] 自动启动队列中的下一批: %s", next.ID)
		go h.startBatchDownload(next.ForceRedownload)
	}
}

// HandleBatchStart 处理批量下载开始请求
func (h *BatchHandler) HandleBatchStart(Conn *SunnyNet.HttpConn) bool {
	path := Conn.Request.URL.Path
	if path != "/__wx_channels_api/batch_start" {
		return false
	}

	utils.Info("📥 [批量下载] 收到 batch_start 请求")

	// 处理 CORS 预检请求
	if Conn.Request.Method == "OPTIONS" {
		h.sendSuccessResponse(Conn, map[string]interface{}{"message": "OK"})
		return true
	}

	// 只处理 POST 请求
	if Conn.Request.Method != "POST" {
		h.sendErrorResponse(Conn, fmt.Errorf("method not allowed: %s", Conn.Request.Method))
		return true
	}

	// 授权校验
	if h.getConfig() != nil && h.getConfig().SecretToken != "" {
		if Conn.Request.Header.Get("X-Local-Auth") != h.getConfig().SecretToken {
			h.sendErrorResponse(Conn, fmt.Errorf("unauthorized"))
			return true
		}
	}

	utils.Info("📥 [批量下载] 开始读取请求体...")

	// 检查请求体是否为空
	if Conn.Request.Body == nil {
		err := fmt.Errorf("request body is nil")
		utils.HandleError(err, "读取batch_start请求体")
		h.sendErrorResponse(Conn, err)
		return true
	}

	body, err := io.ReadAll(Conn.Request.Body)
	if err != nil {
		utils.HandleError(err, "读取batch_start请求体")
		h.sendErrorResponse(Conn, err)
		return true
	}
	defer Conn.Request.Body.Close()

	bodySize := len(body)
	utils.Info("📥 [批量下载] 请求体大小: %.2f MB", float64(bodySize)/(1024*1024))

	var req struct {
		Videos           []BatchTask `json:"videos"`
		ForceRedownload  bool        `json:"forceRedownload"`
		OSSUploadEnabled bool        `json:"ossUploadEnabled"`
		ExportRecordID   string      `json:"exportRecordId"`
		PageSource       string      `json:"pageSource,omitempty"` // 页面来源
	}

	utils.Info("📥 [批量下载] 开始解析 JSON...")
	if err := json.Unmarshal(body, &req); err != nil {
		utils.HandleError(err, "解析batch_start JSON")
		h.sendErrorResponse(Conn, err)
		return true
	}
	utils.Info("📥 [批量下载] JSON 解析完成，视频数: %d", len(req.Videos))
	req.ExportRecordID = strings.TrimSpace(req.ExportRecordID)

	// 判断批量下载来源
	pageSource := req.PageSource
	if pageSource == "" {
		// 如果请求体中没有指定，则通过请求头判断
		origin := Conn.Request.Header.Get("Origin")
		referer := Conn.Request.Header.Get("Referer")

		if strings.Contains(origin, "channels.weixin.qq.com") || strings.Contains(referer, "channels.weixin.qq.com") {
			// 从视频号页面发起的请求，尝试从Referer中提取页面类型
			if strings.Contains(referer, "/web/pages/feed") {
				pageSource = "batch_feed"
			} else if strings.Contains(referer, "/web/pages/home") {
				pageSource = "batch_home"
			} else if strings.Contains(referer, "/web/pages/profile") {
				pageSource = "batch_profile"
			} else {
				pageSource = "batch_channels" // 默认标记为视频号批量下载
			}
		} else {
			// 从Web控制台发起的请求
			pageSource = "batch_console"
		}
	}
	utils.Info("📥 [批量下载] 来源: %s", pageSource)

	if len(req.Videos) == 0 {
		err := fmt.Errorf("视频列表为空")
		h.markExportFailed(req.ExportRecordID, err)
		h.sendErrorResponse(Conn, err)
		return true
	}

	if req.ExportRecordID != "" {
		record, recordErr := h.exportRepo.GetByID(req.ExportRecordID)
		if recordErr != nil {
			h.markExportFailed(req.ExportRecordID, recordErr)
			h.sendErrorResponse(Conn, fmt.Errorf("读取关联导出记录失败: %w", recordErr))
			return true
		}
		if record == nil {
			h.sendErrorResponse(Conn, fmt.Errorf("关联导出记录不存在"))
			return true
		}
		if record.Status != database.ExportStatusProcessing {
			err := fmt.Errorf("关联导出记录当前不可启动：%s", record.Status)
			h.sendErrorResponse(Conn, err)
			return true
		}
		if !req.OSSUploadEnabled || !record.OSSUploadEnabled {
			err := fmt.Errorf("关联导出记录与 OSS 上传模式不一致")
			h.markExportFailed(req.ExportRecordID, err)
			h.sendErrorResponse(Conn, err)
			return true
		}
		if record.TotalCount != len(req.Videos) {
			err := fmt.Errorf("关联导出记录的视频数不一致：记录=%d，请求=%d", record.TotalCount, len(req.Videos))
			h.markExportFailed(req.ExportRecordID, err)
			h.sendErrorResponse(Conn, err)
			return true
		}
		recordItems, itemsErr := h.exportRepo.ListItems(req.ExportRecordID)
		if itemsErr != nil {
			h.markExportFailed(req.ExportRecordID, itemsErr)
			h.sendErrorResponse(Conn, fmt.Errorf("读取关联导出明细失败: %w", itemsErr))
			return true
		}
		expectedVideoIDs := make(map[string]struct{}, len(recordItems))
		for _, item := range recordItems {
			expectedVideoIDs[item.VideoID] = struct{}{}
		}
		seenVideoIDs := make(map[string]struct{}, len(req.Videos))
		for _, video := range req.Videos {
			videoID := strings.TrimSpace(video.ID)
			_, expected := expectedVideoIDs[videoID]
			_, duplicate := seenVideoIDs[videoID]
			if videoID == "" || !expected || duplicate {
				err := fmt.Errorf("关联导出记录与下载任务的视频 ID 不一致: %q", videoID)
				h.markExportFailed(req.ExportRecordID, err)
				h.sendErrorResponse(Conn, err)
				return true
			}
			seenVideoIDs[videoID] = struct{}{}
		}
	}

	var ossUploader batchOSSUploader
	if req.OSSUploadEnabled {
		ossUploader, err = h.loadBatchOSSUploader()
		if err != nil {
			wrappedErr := fmt.Errorf("OSS 同步上传配置不可用: %w", err)
			h.markExportFailed(req.ExportRecordID, wrappedErr)
			h.sendErrorResponse(Conn, wrappedErr)
			return true
		}
	}

	// 初始化任务。任务先在局部构建，避免新批次覆盖正在运行的批次。
	tasks := make([]BatchTask, len(req.Videos))
	defaultHeaders := map[string]string{}
	if origin := strings.TrimSpace(Conn.Request.Header.Get("Origin")); origin != "" {
		defaultHeaders["Origin"] = origin
	}
	if referer := strings.TrimSpace(Conn.Request.Header.Get("Referer")); referer != "" {
		defaultHeaders["Referer"] = referer
	}
	defaultUserAgent := strings.TrimSpace(Conn.Request.Header.Get("User-Agent"))
	defaultSourceURL := strings.TrimSpace(Conn.Request.Header.Get("Referer"))
	for i, v := range req.Videos {
		taskHeaders := cloneStringMap(v.Headers)
		if taskHeaders == nil {
			taskHeaders = map[string]string{}
		}
		for k, val := range defaultHeaders {
			if strings.TrimSpace(taskHeaders[k]) == "" {
				taskHeaders[k] = val
			}
		}
		tasks[i] = BatchTask{
			ID:              v.ID,
			URL:             v.GetURL(),
			Title:           v.Title,
			AuthorName:      v.GetAuthor(), // 兼容 author 和 authorName
			Author:          v.Author,
			Headers:         taskHeaders,
			UserAgent:       firstNonEmpty(v.UserAgent, defaultUserAgent),
			SourceURL:       firstNonEmpty(v.SourceURL, defaultSourceURL),
			Key:             v.GetKey(),
			DecryptorPrefix: v.DecryptorPrefix,
			PrefixLen:       v.PrefixLen,
			FileFormat:      v.FileFormat,
			Status:          "pending",
			DownloadStatus:  "pending",
			// 保留额外字段
			Duration:         v.Duration,
			SizeMB:           v.SizeMB,
			Cover:            v.Cover,
			CoverURL:         v.CoverURL,
			Resolution:       v.Resolution,
			PageSource:       pageSource, // 保存页面来源
			PlayCount:        v.PlayCount,
			LikeCount:        v.LikeCount,
			CommentCount:     v.CommentCount,
			FavCount:         v.FavCount,
			ForwardCount:     v.ForwardCount,
			CreateTime:       v.CreateTime,
			CapturedAt:       v.CapturedAt,
			IPRegion:         v.IPRegion,
			DurationMs:       v.DurationMs,
			Size:             v.Size,
			ExportRecordID:   req.ExportRecordID,
			OSSUploadEnabled: req.OSSUploadEnabled,
		}
		if req.OSSUploadEnabled {
			tasks[i].OSSStatus = "pending"
		}
	}

	batchID := req.ExportRecordID
	if batchID == "" {
		batchID = fmt.Sprintf("batch-%d-%d", time.Now().UnixNano(), rand.Int63())
	}
	batch := batchQueueItem{
		ID:              batchID,
		Tasks:           tasks,
		ForceRedownload: req.ForceRedownload,
		OSSUploader:     ossUploader,
	}

	oldTasks, queuePosition := h.enqueueBatch(batch)

	for _, oldTask := range oldTasks {
		h.cleanupTaskArtifacts(oldTask.GopeedTaskID, oldTask.TempPath, true)
	}

	// 获取并发数配置
	concurrency := 5 // 默认值（与配置默认值一致）
	if h.getConfig() != nil && h.getConfig().DownloadConcurrency > 0 {
		concurrency = h.getConfig().DownloadConcurrency
	}

	if queuePosition > 0 {
		utils.Info("📋 [批量下载] 批次 %s 已进入队列，前方批次数: %d", batchID, queuePosition)
	} else {
		utils.Info("🚀 [批量下载] 开始下载 %d 个视频，并发数: %d", len(req.Videos), concurrency)
	}
	if req.OSSUploadEnabled {
		utils.Info("☁️ [批量下载] 已启用视频同步上传 OSS")
	}

	// 当前无任务时立即启动；否则由队列在前一批完成后自动启动。
	if queuePosition == 0 {
		go h.startBatchDownload(req.ForceRedownload)
	}

	h.sendSuccessResponse(Conn, map[string]interface{}{
		"total":            len(req.Videos),
		"concurrency":      concurrency,
		"ossUploadEnabled": req.OSSUploadEnabled,
		"exportRecordId":   req.ExportRecordID,
		"batchId":          batchID,
		"queued":           queuePosition > 0,
		"queuePosition":    queuePosition,
	})
	return true
}

// startBatchDownload 开始批量下载（并发版本）
func (h *BatchHandler) startBatchDownload(forceRedownload bool) {
	// 创建可取消的 context
	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	h.cancelFunc = cancel
	h.mu.Unlock()

	advanceQueue := false
	defer func() {
		h.finishBatchDownload(cancel, advanceQueue)
	}()

	// 获取下载目录
	downloadsDir, err := h.getDownloadsDir()
	if err != nil {
		utils.HandleError(err, "获取下载目录")
		h.mu.Lock()
		for i := range h.tasks {
			h.tasks[i].Status = "failed"
			h.tasks[i].DownloadStatus = "failed"
			h.tasks[i].Error = err.Error()
		}
		failedTasks := append([]BatchTask(nil), h.tasks...)
		h.mu.Unlock()
		for _, task := range failedTasks {
			h.persistExportTask(task)
		}
		for exportRecordID := range collectBatchExportRecordIDs(failedTasks) {
			h.markExportFailed(exportRecordID, err)
		}
		advanceQueue = true
		return
	}

	// 获取并发数
	concurrency := 5 // 默认值（与配置默认值一致）
	if h.getConfig() != nil && h.getConfig().DownloadConcurrency > 0 {
		concurrency = h.getConfig().DownloadConcurrency
	}
	if concurrency < 1 {
		concurrency = 1
	}

	// 创建任务通道
	taskChan := make(chan int, len(h.tasks))
	var wg sync.WaitGroup

	// 启动 worker
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for taskIdx := range taskChan {
				// 检查是否取消
				select {
				case <-ctx.Done():
					return
				default:
				}

				h.mu.Lock()
				task := &h.tasks[taskIdx]
				task.Status = "downloading"
				task.DownloadStatus = "downloading"
				startedSnapshot := *task
				h.mu.Unlock()
				h.persistExportTask(startedSnapshot)

				utils.Info("📥 [Worker %d] 开始下载: %s", workerID, task.Title)

				// 下载视频
				err := h.downloadVideo(ctx, task, downloadsDir, forceRedownload, taskIdx)

				h.mu.Lock()
				if errors.Is(err, errBatchPaused) {
					if task.Status == "downloading" {
						task.Status = "pending"
					}
					if task.DownloadStatus != "done" {
						task.DownloadStatus = "paused"
					}
					task.Error = ""
					pausedSnapshot := *task
					h.mu.Unlock()
					h.persistExportTask(pausedSnapshot)
					utils.Info("⏸️ [Worker %d] 已暂停: %s", workerID, task.Title)
					continue
				}
				if err != nil {
					task.Status = "failed"
					if task.DownloadStatus != "done" {
						task.DownloadStatus = "failed"
					}
					task.Error = err.Error()
					if task.DownloadStatus == "failed" {
						task.Progress = 0
					}
					utils.Error("❌ [Worker %d] 失败: %s - %v", workerID, task.Title, err)
				} else {
					task.Status = "done"
					task.DownloadStatus = "done"
					task.Progress = 100
					utils.Info("✅ [Worker %d] 完成: %s", workerID, task.Title)
				}
				finishedSnapshot := *task
				h.mu.Unlock()
				h.persistExportTask(finishedSnapshot)
			}
		}(w)
	}

	// 分发任务（只处理 pending 状态的任务，跳过 done 和 failed）
	pendingCount := 0
	for i := range h.tasks {
		h.mu.RLock()
		taskStatus := h.tasks[i].Status
		h.mu.RUnlock()

		// 只处理 pending 状态的任务
		if taskStatus != "pending" {
			continue
		}

		select {
		case <-ctx.Done():
			close(taskChan)
			wg.Wait()
			utils.Info("⏹️ [批量下载] 已取消")
			return
		case taskChan <- i:
			pendingCount++
		}
	}
	close(taskChan)

	if pendingCount == 0 {
		utils.Info("ℹ️ [批量下载] 没有待处理的任务（所有任务已完成或失败）")
		advanceQueue = true
		return
	}
	utils.Info("📋 [批量下载] 开始处理 %d 个待处理任务", pendingCount)

	// 等待所有 worker 完成
	wg.Wait()

	// 统计结果
	h.mu.RLock()
	done, failed := 0, 0
	for _, t := range h.tasks {
		if t.Status == "done" {
			done++
		} else if t.Status == "failed" {
			failed++
		}
	}
	h.mu.RUnlock()

	utils.Info("✅ [批量下载] 全部完成！成功: %d, 失败: %d", done, failed)
	h.mu.RLock()
	finalTasks := append([]BatchTask(nil), h.tasks...)
	h.mu.RUnlock()
	for exportRecordID := range collectBatchExportRecordIDs(finalTasks) {
		if h.exportRepo != nil {
			if err := h.exportRepo.RefreshStatus(exportRecordID); err != nil {
				utils.Warn("刷新导出记录最终状态失败: export=%s error=%v", exportRecordID, err)
			}
		}
	}
	advanceQueue = !batchTasksHavePending(finalTasks)
}

func collectBatchExportRecordIDs(tasks []BatchTask) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, task := range tasks {
		if id := strings.TrimSpace(task.ExportRecordID); id != "" {
			ids[id] = struct{}{}
		}
	}
	return ids
}

func (h *BatchHandler) uploadBatchTaskToOSS(ctx context.Context, task *BatchTask, filePath string) error {
	if task == nil || !task.OSSUploadEnabled {
		return nil
	}
	h.mu.RLock()
	uploader := h.ossUploader
	h.mu.RUnlock()
	if uploader == nil {
		err := fmt.Errorf("OSS 上传器未初始化，请重新保存 OSS 配置后重试")
		h.mu.Lock()
		task.OSSStatus = "failed"
		task.OSSError = err.Error()
		snapshot := *task
		h.mu.Unlock()
		h.persistExportTask(snapshot)
		return err
	}

	scrapedDate := parseBatchCreateTime(firstNonEmpty(task.CapturedAt, task.CreateTime))
	objectKey, err := services.BuildOSSObjectKey(task.ID, services.DefaultOSSObjectPrefix, scrapedDate)
	if err != nil {
		h.mu.Lock()
		task.OSSStatus = "failed"
		task.OSSError = err.Error()
		snapshot := *task
		h.mu.Unlock()
		h.persistExportTask(snapshot)
		return err
	}
	h.mu.Lock()
	task.OSSObjectKey = objectKey
	task.OSSStatus = "uploading"
	task.OSSProgress = 0
	task.OSSUploadedBytes = 0
	task.OSSError = ""
	if stat, statErr := os.Stat(filePath); statErr == nil {
		task.OSSTotalBytes = stat.Size()
		task.Size = stat.Size()
	}
	uploadingSnapshot := *task
	h.mu.Unlock()
	h.persistExportTask(uploadingSnapshot)

	lastPersistedPercent := -1
	lastPersistedAt := time.Time{}
	onProgress := func(uploaded, total int64) {
		progress := float64(0)
		if total > 0 {
			progress = float64(uploaded) / float64(total) * 100
			if progress > 100 {
				progress = 100
			}
		}
		now := time.Now()
		percent := int(progress)
		h.mu.Lock()
		task.OSSUploadedBytes = uploaded
		task.OSSTotalBytes = total
		task.OSSProgress = progress
		if total > 0 {
			task.Size = total
		}
		shouldPersist := percent >= lastPersistedPercent+1 || now.Sub(lastPersistedAt) >= time.Second || uploaded == total
		progressSnapshot := *task
		h.mu.Unlock()
		if shouldPersist {
			lastPersistedPercent = percent
			lastPersistedAt = now
			h.persistExportTask(progressSnapshot)
		}
	}

	const maxRetries = 3
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return errBatchPaused
		default:
		}

		if attempt > 1 {
			delay := time.Duration(1<<uint(attempt-1)) * time.Second
			h.mu.Lock()
			task.OSSStatus = "retrying"
			retrySnapshot := *task
			h.mu.Unlock()
			h.persistExportTask(retrySnapshot)
			utils.Warn("☁️ [OSS] 等待 %v 后重试 (%d/%d): %s", delay, attempt, maxRetries, task.Title)
			select {
			case <-ctx.Done():
				return errBatchPaused
			case <-time.After(delay):
			}
			h.mu.Lock()
			task.OSSStatus = "uploading"
			task.OSSProgress = 0
			task.OSSUploadedBytes = 0
			uploadRetrySnapshot := *task
			h.mu.Unlock()
			h.persistExportTask(uploadRetrySnapshot)
		}

		utils.Info("☁️ [OSS] 开始同步上传: %s -> %s", task.Title, objectKey)
		var result services.OSSUploadResult
		var uploadErr error
		if progressUploader, ok := uploader.(batchOSSProgressUploader); ok {
			result, uploadErr = progressUploader.UploadVideoWithProgress(ctx, filePath, task.ID, scrapedDate, onProgress)
		} else {
			result, uploadErr = uploader.UploadVideo(ctx, filePath, task.ID, scrapedDate)
		}
		if uploadErr == nil {
			h.mu.Lock()
			task.OSSStatus = "done"
			task.OSSProgress = 100
			task.OSSUploadedBytes = result.SizeBytes
			task.OSSTotalBytes = result.SizeBytes
			if result.SizeBytes > 0 {
				task.Size = result.SizeBytes
				task.TotalMB = float64(result.SizeBytes) / (1024 * 1024)
				task.DownloadedMB = task.TotalMB
			}
			task.OSSObjectKey = result.ObjectKey
			task.OSSURL = result.URL
			task.OSSError = ""
			doneSnapshot := *task
			h.mu.Unlock()
			h.persistExportTask(doneSnapshot)
			utils.Info("✅ [OSS] 上传并校验完成: %s", result.ObjectKey)
			return nil
		}
		if ctx.Err() != nil {
			h.mu.Lock()
			task.OSSStatus = "paused"
			pausedSnapshot := *task
			h.mu.Unlock()
			h.persistExportTask(pausedSnapshot)
			return errBatchPaused
		}
		lastErr = uploadErr
		h.mu.Lock()
		task.OSSError = uploadErr.Error()
		errorSnapshot := *task
		h.mu.Unlock()
		h.persistExportTask(errorSnapshot)
		utils.Warn("⚠️ [OSS] 上传失败 (%d/%d): %s - %v", attempt, maxRetries, task.Title, uploadErr)
	}

	h.mu.Lock()
	task.OSSStatus = "failed"
	failedSnapshot := *task
	h.mu.Unlock()
	h.persistExportTask(failedSnapshot)
	return fmt.Errorf("OSS 同步上传失败（已重试 %d 次）: %w", maxRetries, lastErr)
}

func (h *BatchHandler) markBatchTaskDownloadDone(task *BatchTask, filePath string) {
	if task == nil {
		return
	}
	var size int64
	if stat, err := os.Stat(filePath); err == nil {
		size = stat.Size()
	}
	h.mu.Lock()
	task.DownloadStatus = "done"
	task.Progress = 100
	if size > 0 {
		task.Size = size
		task.TotalMB = float64(size) / (1024 * 1024)
		task.DownloadedMB = task.TotalMB
	}
	snapshot := *task
	h.mu.Unlock()
	h.persistExportTask(snapshot)
}

// downloadVideo 下载单个视频（带重试和断点续传）
func (h *BatchHandler) downloadVideo(ctx context.Context, task *BatchTask, downloadsDir string, forceRedownload bool, taskIdx int) error {
	// 创建作者目录
	authorFolder := utils.CleanFolderName(task.GetAuthor())
	savePath := filepath.Join(downloadsDir, authorFolder)
	if err := utils.EnsureDir(savePath); err != nil {
		return fmt.Errorf("创建作者目录失败: %v", err)
	}

	settings, err := h.settingsRepo.Load()
	if err != nil {
		utils.Warn("加载下载命名设置失败，继续使用默认命名策略: %v", err)
	}
	includeVideoID := true
	if settings != nil {
		includeVideoID = settings.DownloadFilenameWithVideoID
	}
	filenameTemplate := ""
	if cfg := h.getConfig(); cfg != nil {
		filenameTemplate = cfg.DownloadFilenameTemplate
	}

	// 如果取消发生在本地文件已完成、OSS 上传尚未完成的阶段，继续任务时直接
	// 复用已解密的视频，避免再次下载并产生重复文件。
	if !forceRedownload && task.OSSUploadEnabled && task.OSSStatus != "done" && strings.TrimSpace(task.FinalPath) != "" {
		if stat, statErr := os.Stat(task.FinalPath); statErr == nil && stat.Mode().IsRegular() && stat.Size() > 0 {
			utils.Info("☁️ [批量下载] 复用已下载文件继续上传 OSS: %s", task.Title)
			h.markBatchTaskDownloadDone(task, task.FinalPath)
			if err := h.uploadBatchTaskToOSS(ctx, task, task.FinalPath); err != nil {
				return err
			}
			h.saveDownloadRecord(task, task.FinalPath, "completed")
			return nil
		}
	}

	if !forceRedownload && task.ID != "" && h.downloadService != nil {
		if exists, err := h.downloadService.GetByID(task.ID); err == nil && exists != nil && exists.FilePath != "" {
			if _, statErr := os.Stat(exists.FilePath); statErr == nil {
				utils.Info("⏭️ [批量下载] 视频已存在，跳过: ID=%s", task.ID)
				h.markBatchTaskDownloadDone(task, exists.FilePath)
				if err := h.uploadBatchTaskToOSS(ctx, task, exists.FilePath); err != nil {
					return err
				}
				h.saveDownloadRecord(task, exists.FilePath, "completed")
				return nil
			}
		}
	} else if h.downloadService == nil {
		utils.Warn("downloadService is nil, skipping DB check")
	}

	// 生成文件名：默认仅使用标题；如配置模板，则优先按模板渲染。
	cleanFilename := utils.BuildVideoFilename(utils.VideoFilenameMeta{
		Title:      task.Title,
		VideoID:    task.ID,
		Author:     task.GetAuthor(),
		Duration:   resolveBatchTaskDuration(task),
		CreateTime: parseBatchCreateTime(task.CreateTime),
		SizeBytes:  task.Size,
		SizeText:   task.SizeMB,
	}, includeVideoID, filenameTemplate)
	cleanFilename = utils.EnsureExtension(cleanFilename, ".mp4")
	desiredPath := task.FinalPath
	if strings.TrimSpace(desiredPath) == "" {
		desiredPath = filepath.Join(savePath, cleanFilename)
		if !forceRedownload {
			if _, err := os.Stat(desiredPath); err == nil {
				desiredPath = utils.GenerateUniquePath(savePath, cleanFilename)
				utils.Info("🪪 [批量下载] 同名文件已存在，将使用新文件名: %s", filepath.Base(desiredPath))
			}
		}
		task.FinalPath = desiredPath
	}

	// 使用配置的重试次数
	maxRetries := 3
	if h.getConfig() != nil {
		maxRetries = h.getConfig().DownloadRetryCount
	}
	if maxRetries < 1 {
		maxRetries = 3
	}
	var lastErr error

	for retry := 0; retry < maxRetries; retry++ {
		// 检查是否取消
		select {
		case <-ctx.Done():
			return errBatchPaused
		default:
		}

		if retry > 0 {
			// 指数退避 + 随机抖动
			baseDelay := time.Duration(1<<uint(retry)) * time.Second // 2s, 4s, 8s...
			jitter := time.Duration(rand.Intn(1000)) * time.Millisecond
			delay := baseDelay + jitter
			utils.Info("🔄 [批量下载] 等待 %v 后重试 (%d/%d): %s", delay, retry, maxRetries-1, task.Title)

			select {
			case <-ctx.Done():
				return errBatchPaused
			case <-time.After(delay):
			}
		}

		// 使用配置的超时时间
		timeout := 10 * time.Minute
		if h.getConfig() != nil && h.getConfig().DownloadTimeout > 0 {
			timeout = h.getConfig().DownloadTimeout
		}
		downloadCtx, cancel := context.WithTimeout(ctx, timeout)
		actualPath, err := h.downloadVideoOnce(downloadCtx, task, desiredPath, taskIdx)
		cancel()

		if err == nil {
			h.markBatchTaskDownloadDone(task, actualPath)
			if err := h.uploadBatchTaskToOSS(ctx, task, actualPath); err != nil {
				return err
			}
			// 下载成功，保存到下载记录数据库
			h.saveDownloadRecord(task, actualPath, "completed")
			return nil
		}
		if errors.Is(err, errBatchPaused) || errors.Is(err, context.Canceled) {
			return errBatchPaused
		}

		lastErr = err
		utils.LogDownloadRetry(task.ID, task.Title, retry+1, maxRetries, err)
		utils.Warn("⚠️ [批量下载] 下载失败 (尝试 %d/%d): %v", retry+1, maxRetries, err)

	}

	// 记录最终失败的详细错误
	utils.LogDownloadError(task.ID, task.Title, task.GetAuthor(), task.URL, lastErr, maxRetries)
	return fmt.Errorf("下载失败（已重试 %d 次）: %v", maxRetries, lastErr)
}

// downloadVideoOnce 执行一次下载尝试（支持断点续传）
func (h *BatchHandler) downloadVideoOnce(ctx context.Context, task *BatchTask, desiredPath string, taskIdx int) (string, error) {
	// 使用 Gopeed 下载
	if h.gopeedService == nil {
		return "", fmt.Errorf("Gopeed下载服务未初始化")
	}

	// 开始下载
	utils.Info("🚀 [批量下载] 使用 Gopeed 下载: %s", task.Title)
	tmpHint := task.ID
	if tmpHint == "" {
		tmpHint = strconv.Itoa(taskIdx)
	}
	if strings.TrimSpace(task.TempPath) == "" {
		task.TempPath = utils.BuildTempDownloadPath(desiredPath, tmpHint)
	}
	tmpPath := task.TempPath

	lastPersistedDownloadPercent := -1
	lastPersistedDownloadAt := time.Time{}
	onProgress := func(progress float64, downloaded int64, total int64) {
		h.mu.Lock()
		shouldPersist := false
		var progressSnapshot BatchTask

		// 确保任务索引有效
		if taskIdx >= 0 && taskIdx < len(h.tasks) {
			task := &h.tasks[taskIdx]

			// 只在下载中状态更新，避免覆盖完成状态
			if task.Status == "downloading" {
				task.DownloadStatus = "downloading"
				task.Progress = progress * 100 // 转换为百分比
				task.DownloadedMB = float64(downloaded) / (1024 * 1024)
				task.TotalMB = float64(total) / (1024 * 1024)
				if total > 0 {
					task.Size = total
				}
				// 也可以根据需要计算 SizeMB 字符串
				if total > 0 {
					task.SizeMB = fmt.Sprintf("%.2fMB", task.TotalMB)
				}

				// 每10%输出一次日志
				if int(task.Progress)%10 == 0 && task.Progress > 0 {
					utils.Info("📊 [批量下载] %s 进度: %.1f%% (%.2f/%.2f MB)",
						task.Title, task.Progress, task.DownloadedMB, task.TotalMB)
				}
				now := time.Now()
				percent := int(task.Progress)
				shouldPersist = percent >= lastPersistedDownloadPercent+1 ||
					now.Sub(lastPersistedDownloadAt) >= time.Second || downloaded == total
				if shouldPersist {
					lastPersistedDownloadPercent = percent
					lastPersistedDownloadAt = now
					progressSnapshot = *task
				}
			}
		}
		h.mu.Unlock()
		if shouldPersist {
			h.persistExportTask(progressSnapshot)
		}
	}

	// 获取单文件连接数配置
	connections := 8 // 默认值
	if h.getConfig() != nil && h.getConfig().DownloadConnections > 0 {
		connections = h.getConfig().DownloadConnections
	}

	downloadURL, mode := NormalizeDownloadURL(task.GetURL(), task.FileFormat)
	if downloadURL != task.GetURL() {
		utils.Info("🩹 [批量下载] 原始视频链接已归一化为 encfilekey+token 直链")
	}
	connections = ResolveDownloadConnections(mode, connections)
	if mode == downloadVideoModeOriginal {
		utils.Info("🎯 [批量下载] 原始视频使用单连接模式")
	}

	headers := cloneStringMap(task.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	if strings.TrimSpace(headers["Origin"]) == "" {
		headers["Origin"] = "https://channels.weixin.qq.com"
	}
	if task.UserAgent != "" {
		headers["User-Agent"] = task.UserAgent
	}
	if task.SourceURL != "" {
		headers["Referer"] = task.SourceURL
	}
	utils.Info("🌐 [批量下载] 请求头: Referer=%s | UA=%s | 连接数=%d", headers["Referer"], headers["User-Agent"], connections)

	createTask := func() error {
		_ = os.Remove(tmpPath)
		taskID, err := h.gopeedService.CreateTask(downloadURL, tmpPath, connections, headers)
		if err != nil {
			return err
		}
		task.GopeedTaskID = taskID
		return nil
	}

	if strings.TrimSpace(task.GopeedTaskID) == "" {
		if err := createTask(); err != nil {
			return "", err
		}
	} else if h.batchResumeEnabled() {
		snapshot, err := h.gopeedService.GetTaskSnapshot(task.GopeedTaskID)
		if err != nil {
			utils.Warn("⚠️ [批量下载] 已丢失 Gopeed 任务，重新创建: %s - %v", task.Title, err)
			h.cleanupTaskArtifacts(task.GopeedTaskID, tmpPath, true)
			task.GopeedTaskID = ""
			if err := createTask(); err != nil {
				return "", err
			}
		} else {
			switch snapshot.Status {
			case base.DownloadStatusPause, base.DownloadStatusWait, base.DownloadStatusReady:
				if err := h.gopeedService.ContinueTask(task.GopeedTaskID); err != nil {
					return "", err
				}
			case base.DownloadStatusError:
				h.cleanupTaskArtifacts(task.GopeedTaskID, tmpPath, true)
				task.GopeedTaskID = ""
				if err := createTask(); err != nil {
					return "", err
				}
			}
		}
	}

	actualPath, err := h.gopeedService.WaitTask(ctx, task.GopeedTaskID, onProgress)
	if err != nil {
		if errors.Is(err, services.ErrTaskPaused) {
			if actualPath == "" {
				actualPath = tmpPath
			}
			return actualPath, errBatchPaused
		}
		if errors.Is(err, context.Canceled) {
			if actualPath == "" {
				actualPath = tmpPath
			}
			if !h.batchResumeEnabled() {
				h.cleanupTaskArtifacts(task.GopeedTaskID, tmpPath, true)
				task.GopeedTaskID = ""
				task.TempPath = ""
			}
			return actualPath, errBatchPaused
		}
		h.cleanupTaskArtifacts(task.GopeedTaskID, tmpPath, true)
		task.GopeedTaskID = ""
		task.TempPath = ""
		return "", err
	}
	if actualPath == "" {
		actualPath = tmpPath
	}

	stat, err := os.Stat(actualPath)
	if err != nil || stat.Size() == 0 {
		h.cleanupTaskArtifacts(task.GopeedTaskID, actualPath, true)
		task.GopeedTaskID = ""
		return "", fmt.Errorf("下载文件无效")
	}

	// 解密逻辑（如果需要）
	needDecrypt := task.Key != "" || (task.DecryptorPrefix != "" && task.PrefixLen > 0)
	if needDecrypt {
		utils.Info("🔐 [批量下载] 开始解密视频...")
		if err := utils.DecryptFileInPlace(actualPath, task.GetKey(), task.DecryptorPrefix, task.PrefixLen); err != nil {
			h.cleanupTaskArtifacts(task.GopeedTaskID, actualPath, true)
			task.GopeedTaskID = ""
			return "", fmt.Errorf("解密失败: %v", err)
		}
		utils.Info("✓ [批量下载] 解密完成")
	}

	finalPath, err := utils.MoveFileToAvailablePath(actualPath, desiredPath)
	if err != nil {
		h.cleanupTaskArtifacts(task.GopeedTaskID, actualPath, true)
		task.GopeedTaskID = ""
		return "", fmt.Errorf("移动文件失败: %v", err)
	}
	if finalPath != desiredPath {
		utils.Warn("📁 [批量下载] 目标文件已存在，已自动保存为: %s", filepath.Base(finalPath))
	}
	if err := h.gopeedService.DeleteTask(task.GopeedTaskID, false); err != nil && !strings.Contains(strings.ToLower(err.Error()), "task not found") {
		utils.Warn("清理 Gopeed 任务失败: %v", err)
	}
	task.GopeedTaskID = ""
	task.TempPath = ""
	task.FinalPath = finalPath

	return finalPath, nil
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			continue
		}
		dst[k] = v
	}
	if len(dst) == 0 {
		return nil
	}
	return dst
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// saveDownloadRecord 保存下载记录到数据库
func (h *BatchHandler) saveDownloadRecord(task *BatchTask, filePath string, status string) {
	// 检查DB中是否已存在记录
	if h.downloadService != nil {
		if existing, err := h.downloadService.GetByID(task.ID); err == nil && existing != nil {
			utils.Info("📝 [下载记录] 记录已存在(DB)，跳过保存: %s - %s", task.Title, task.GetAuthor())
			return
		}
	}

	// 获取文件大小
	var fileSize int64 = 0
	if stat, err := os.Stat(filePath); err == nil {
		fileSize = stat.Size()
	}

	// 解析时长字符串为毫秒 (格式: "00:22" 或 "1:23:45")
	duration := resolveBatchTaskDurationMs(task)

	// 尝试从浏览记录获取更多信息（分辨率、封面等）
	resolution := task.Resolution
	coverURL := task.Cover
	if resolution == "" || coverURL == "" {
		browseRepo := database.NewBrowseHistoryRepository()
		if browseRecord, err := browseRepo.GetByID(task.ID); err == nil && browseRecord != nil {
			if resolution == "" {
				resolution = browseRecord.Resolution
			}
			if coverURL == "" {
				coverURL = browseRecord.CoverURL
			}
			// 如果时长为0，也从浏览记录获取
			if duration == 0 {
				duration = browseRecord.Duration
			}
		}
	}

	// 创建下载记录
	// 使用格式化后的文件名作为标题，确保与实际文件名一致
	cleanTitle := utils.CleanFilename(task.Title)
	record := &database.DownloadRecord{
		ID:           task.ID,
		VideoID:      task.ID,
		Title:        cleanTitle,
		Author:       task.GetAuthor(),
		CoverURL:     coverURL,
		Duration:     duration,
		FileSize:     fileSize,
		FilePath:     filePath,
		Format:       "mp4",
		Resolution:   resolution,
		Status:       status,
		DownloadTime: time.Now(),
	}

	// 保存到数据库
	if h.downloadService != nil {
		if err := h.downloadService.Create(record); err != nil {
			// 如果是重复记录，尝试更新
			if strings.Contains(err.Error(), "UNIQUE constraint") {
				if updateErr := h.downloadService.Update(record); updateErr != nil {
					utils.Warn("更新下载记录失败: %v", updateErr)
				}
			} else {
				utils.Warn("保存下载记录失败: %v", err)
			}
		} else {
			utils.Info("📝 [下载记录] 已保存(DB): %s - %s", task.Title, task.GetAuthor())
		}
	}
}

// parseDurationToMs 解析时长字符串为毫秒
// 支持格式: "00:22", "1:23", "1:23:45"
func parseDurationToMs(duration string) int64 {
	if duration == "" {
		return 0
	}

	parts := strings.Split(duration, ":")
	var totalSeconds int64 = 0

	switch len(parts) {
	case 2: // MM:SS
		minutes, _ := strconv.ParseInt(parts[0], 10, 64)
		seconds, _ := strconv.ParseInt(parts[1], 10, 64)
		totalSeconds = minutes*60 + seconds
	case 3: // HH:MM:SS
		hours, _ := strconv.ParseInt(parts[0], 10, 64)
		minutes, _ := strconv.ParseInt(parts[1], 10, 64)
		seconds, _ := strconv.ParseInt(parts[2], 10, 64)
		totalSeconds = hours*3600 + minutes*60 + seconds
	}

	return totalSeconds * 1000 // 转换为毫秒
}

func resolveBatchTaskDurationMs(task *BatchTask) int64 {
	if task == nil {
		return 0
	}

	if durationMs := parseDurationToMs(task.Duration); durationMs > 0 {
		return durationMs
	}

	if task.DurationMs > 0 {
		return task.DurationMs
	}

	return 0
}

func resolveBatchTaskDuration(task *BatchTask) time.Duration {
	return time.Duration(resolveBatchTaskDurationMs(task)) * time.Millisecond
}

func parseBatchCreateTime(raw string) time.Time {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}
	}

	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
		"2006/01/02 15:04:05",
		"2006/01/02 15:04",
		"2006/01/02",
	}

	for _, layout := range layouts {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed
		}
	}

	return time.Time{}
}

// HandleBatchProgress 处理批量下载进度查询请求
func (h *BatchHandler) HandleBatchProgress(Conn *SunnyNet.HttpConn) bool {
	path := Conn.Request.URL.Path
	if path != "/__wx_channels_api/batch_progress" {
		return false
	}

	// 处理 CORS 预检请求
	if Conn.Request.Method == "OPTIONS" {
		h.sendSuccessResponse(Conn, map[string]interface{}{"message": "OK"})
		return true
	}

	// 授权校验
	if h.getConfig() != nil && h.getConfig().SecretToken != "" {
		if Conn.Request.Header.Get("X-Local-Auth") != h.getConfig().SecretToken {
			h.sendErrorResponse(Conn, fmt.Errorf("unauthorized"))
			return true
		}
	}

	requestedBatchID := strings.TrimSpace(Conn.Request.URL.Query().Get("batchId"))
	if Conn.Request.Body != nil {
		body, readErr := io.ReadAll(io.LimitReader(Conn.Request.Body, 64*1024))
		Conn.Request.Body.Close()
		if readErr == nil && len(body) > 0 {
			var req struct {
				BatchID        string `json:"batchId"`
				ExportRecordID string `json:"exportRecordId"`
			}
			if json.Unmarshal(body, &req) == nil {
				requestedBatchID = firstNonEmpty(strings.TrimSpace(req.BatchID), strings.TrimSpace(req.ExportRecordID), requestedBatchID)
			}
		}
	}

	h.mu.RLock()
	activeBatchID := h.currentBatchID
	resolvedBatchID := activeBatchID
	tasks := append([]BatchTask(nil), h.tasks...)
	isRunning := h.running
	queued := false
	queuePosition := 0
	found := requestedBatchID == "" || requestedBatchID == activeBatchID
	queueLength := len(h.pendingBatches)

	if requestedBatchID != "" && requestedBatchID != activeBatchID {
		found = false
		tasks = nil
		isRunning = false
		resolvedBatchID = requestedBatchID
		for i, pending := range h.pendingBatches {
			if pending.ID == requestedBatchID {
				tasks = append([]BatchTask(nil), pending.Tasks...)
				queued = true
				queuePosition = i + 1
				found = true
				break
			}
		}
		if !found {
			if completed, ok := h.completedBatches[requestedBatchID]; ok {
				tasks = append([]BatchTask(nil), completed...)
				found = true
			}
		}
	}
	h.mu.RUnlock()

	total := len(tasks)
	done, failed, running := 0, 0, 0
	var downloadingTasks []map[string]interface{}
	var allTasks []map[string]interface{}

	for _, t := range tasks {
		taskInfo := map[string]interface{}{
			"id":               t.ID,
			"title":            t.Title,
			"authorName":       t.GetAuthor(),
			"status":           t.Status,
			"downloadStatus":   t.DownloadStatus,
			"progress":         t.Progress,
			"downloadedMB":     t.DownloadedMB,
			"totalMB":          t.TotalMB,
			"error":            t.Error,
			"exportRecordId":   t.ExportRecordID,
			"ossUploadEnabled": t.OSSUploadEnabled,
			"ossStatus":        t.OSSStatus,
			"ossProgress":      t.OSSProgress,
			"ossUploadedBytes": t.OSSUploadedBytes,
			"ossTotalBytes":    t.OSSTotalBytes,
			"ossObjectKey":     t.OSSObjectKey,
			"ossUrl":           t.OSSURL,
			"ossError":         t.OSSError,
		}
		allTasks = append(allTasks, taskInfo)

		switch t.Status {
		case "done":
			done++
		case "failed":
			failed++
		case "downloading":
			// 只有在真正运行中时才统计为 running
			if isRunning {
				running++
				downloadingTasks = append(downloadingTasks, taskInfo)
			}
		}
	}
	response := map[string]interface{}{
		"total":         total,
		"done":          done,
		"failed":        failed,
		"running":       running,
		"tasks":         allTasks,
		"batchId":       resolvedBatchID,
		"activeBatchId": activeBatchID,
		"queued":        queued,
		"queuePosition": queuePosition,
		"queueLength":   queueLength,
		"found":         found,
	}

	// 返回所有正在下载的任务（并发模式下可能有多个）
	if len(downloadingTasks) > 0 {
		response["currentTasks"] = downloadingTasks
		// 兼容旧版本，返回第一个
		response["currentTask"] = downloadingTasks[0]
	}

	h.sendSuccessResponse(Conn, response)
	return true
}

// HandleBatchCancel 处理批量下载取消请求
func (h *BatchHandler) HandleBatchCancel(Conn *SunnyNet.HttpConn) bool {
	path := Conn.Request.URL.Path
	if path != "/__wx_channels_api/batch_cancel" {
		return false
	}

	// 处理 CORS 预检请求
	if Conn.Request.Method == "OPTIONS" {
		h.sendSuccessResponse(Conn, map[string]interface{}{"message": "OK"})
		return true
	}

	// 授权校验
	if h.getConfig() != nil && h.getConfig().SecretToken != "" {
		if Conn.Request.Header.Get("X-Local-Auth") != h.getConfig().SecretToken {
			h.sendErrorResponse(Conn, fmt.Errorf("unauthorized"))
			return true
		}
	}

	requestedBatchID := strings.TrimSpace(Conn.Request.URL.Query().Get("batchId"))
	if Conn.Request.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(Conn.Request.Body, 64*1024))
		Conn.Request.Body.Close()
		if len(body) > 0 {
			var req struct {
				BatchID string `json:"batchId"`
			}
			if json.Unmarshal(body, &req) == nil && strings.TrimSpace(req.BatchID) != "" {
				requestedBatchID = strings.TrimSpace(req.BatchID)
			}
		}
	}

	h.mu.Lock()
	if requestedBatchID != "" && requestedBatchID != h.currentBatchID {
		var cancelledTasks []BatchTask
		for i, pending := range h.pendingBatches {
			if pending.ID == requestedBatchID {
				cancelledTasks = append([]BatchTask(nil), pending.Tasks...)
				h.pendingBatches = append(h.pendingBatches[:i], h.pendingBatches[i+1:]...)
				break
			}
		}
		h.mu.Unlock()

		if len(cancelledTasks) > 0 {
			cancelErr := fmt.Errorf("排队中的批量下载及 OSS 上传任务已取消")
			for exportRecordID := range collectBatchExportRecordIDs(cancelledTasks) {
				h.markExportFailed(exportRecordID, cancelErr)
			}
			h.sendSuccessResponse(Conn, map[string]interface{}{
				"message": "排队任务已取消",
				"batchId": requestedBatchID,
				"queued":  true,
			})
			return true
		}

		// 目标批次可能已完成；不应因此误取消其他页面的当前批次。
		h.sendSuccessResponse(Conn, map[string]interface{}{
			"message": "目标批次已不在运行或排队中",
			"batchId": requestedBatchID,
		})
		return true
	}

	cancel := h.cancelFunc
	resumeEnabled := h.batchResumeEnabled()
	taskIDs := make([]string, 0)
	pausedTasks := make([]BatchTask, 0)
	if h.running && cancel != nil {
		h.running = false
		for i := range h.tasks {
			if h.tasks[i].Status == "downloading" {
				h.tasks[i].Status = "pending"
				if h.tasks[i].DownloadStatus != "done" {
					h.tasks[i].DownloadStatus = "paused"
				}
				if h.tasks[i].OSSUploadEnabled && h.tasks[i].OSSStatus != "done" && h.tasks[i].OSSStatus != "failed" {
					h.tasks[i].OSSStatus = "paused"
				}
				h.tasks[i].Error = ""
				pausedTasks = append(pausedTasks, h.tasks[i])
				if resumeEnabled && strings.TrimSpace(h.tasks[i].GopeedTaskID) != "" {
					taskIDs = append(taskIDs, h.tasks[i].GopeedTaskID)
				}
			}
		}
	}
	h.mu.Unlock()
	for _, task := range pausedTasks {
		h.persistExportTask(task)
	}

	if resumeEnabled {
		for _, taskID := range taskIDs {
			if err := h.gopeedService.PauseTask(taskID); err != nil && !strings.Contains(strings.ToLower(err.Error()), "task not found") {
				utils.Warn("暂停 Gopeed 任务失败: %v", err)
			}
		}
	}
	if cancel != nil {
		cancel()
	}

	utils.Info("⏹️ [批量下载] 用户取消下载")

	h.sendSuccessResponse(Conn, map[string]interface{}{
		"message": "下载已取消",
	})
	return true
}

// HandleBatchFailed 处理导出失败清单请求
func (h *BatchHandler) HandleBatchFailed(Conn *SunnyNet.HttpConn) bool {
	path := Conn.Request.URL.Path
	if path != "/__wx_channels_api/batch_failed" {
		return false
	}

	// 处理 CORS 预检请求
	if Conn.Request.Method == "OPTIONS" {
		h.sendSuccessResponse(Conn, map[string]interface{}{"message": "OK"})
		return true
	}

	// 授权校验
	if h.getConfig() != nil && h.getConfig().SecretToken != "" {
		if Conn.Request.Header.Get("X-Local-Auth") != h.getConfig().SecretToken {
			h.sendErrorResponse(Conn, fmt.Errorf("unauthorized"))
			return true
		}
	}

	h.mu.RLock()
	failedTasks := make([]BatchTask, 0)
	for _, t := range h.tasks {
		if t.Status == "failed" {
			failedTasks = append(failedTasks, t)
		}
	}
	h.mu.RUnlock()

	if len(failedTasks) == 0 {
		h.sendSuccessResponse(Conn, map[string]interface{}{
			"failed": 0,
		})
		return true
	}

	// 导出失败清单
	// 获取下载目录
	downloadsDir, err := h.getDownloadsDir()
	if err != nil {
		h.sendErrorResponse(Conn, err)
		return true
	}
	timestamp := time.Now().Format("20060102_150405")
	exportFile := filepath.Join(downloadsDir, fmt.Sprintf("failed_videos_%s.json", timestamp))

	data, err := json.MarshalIndent(failedTasks, "", "  ")
	if err != nil {
		h.sendErrorResponse(Conn, err)
		return true
	}

	if err := os.WriteFile(exportFile, data, 0644); err != nil {
		h.sendErrorResponse(Conn, err)
		return true
	}

	utils.Info("📄 [批量下载] 失败清单已导出: %s", exportFile)

	h.sendSuccessResponse(Conn, map[string]interface{}{
		"failed": len(failedTasks),
		"json":   exportFile,
	})
	return true
}

// HandleBatchResume 处理继续下载请求（从pending状态恢复）
func (h *BatchHandler) HandleBatchResume(Conn *SunnyNet.HttpConn) bool {
	path := Conn.Request.URL.Path
	if path != "/__wx_channels_api/batch_resume" {
		return false
	}

	// 处理 CORS 预检请求
	if Conn.Request.Method == "OPTIONS" {
		h.sendSuccessResponse(Conn, map[string]interface{}{"message": "OK"})
		return true
	}

	// 授权校验
	if h.getConfig() != nil && h.getConfig().SecretToken != "" {
		if Conn.Request.Header.Get("X-Local-Auth") != h.getConfig().SecretToken {
			h.sendErrorResponse(Conn, fmt.Errorf("unauthorized"))
			return true
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// 检查是否有待处理的任务
	// 包括 pending 状态的任务，以及 failed 状态但错误为"下载已取消"的任务
	pendingCount := 0
	ossUploadNeeded := false
	for i := range h.tasks {
		if h.tasks[i].Status == "pending" {
			pendingCount++
			ossUploadNeeded = ossUploadNeeded || h.tasks[i].OSSUploadEnabled
		} else if h.tasks[i].Status == "failed" && h.tasks[i].Error == "下载已取消" {
			// 将因取消而失败的任务重置为 pending 状态，以便继续下载
			// 注意：保留进度以支持断点续传
			h.tasks[i].Status = "pending"
			h.tasks[i].Error = ""
			// 不重置进度，保留已下载的进度以支持断点续传
			pendingCount++
			ossUploadNeeded = ossUploadNeeded || h.tasks[i].OSSUploadEnabled
		}
	}

	if pendingCount == 0 {
		h.sendErrorResponse(Conn, fmt.Errorf("没有待处理的任务"))
		return true
	}

	// 如果已经在运行，返回错误
	if h.running || h.cancelFunc != nil {
		h.sendErrorResponse(Conn, fmt.Errorf("下载正在进行中或上一轮仍在收尾，无法继续"))
		return true
	}
	if ossUploadNeeded {
		uploader, err := h.loadBatchOSSUploader()
		if err != nil {
			h.sendErrorResponse(Conn, fmt.Errorf("OSS 同步上传配置不可用: %w", err))
			return true
		}
		h.ossUploader = uploader
	}

	// 读取请求体获取 forceRedownload 参数
	var req struct {
		ForceRedownload bool `json:"forceRedownload"`
	}
	if Conn.Request.Body != nil {
		body, _ := io.ReadAll(Conn.Request.Body)
		json.Unmarshal(body, &req)
		Conn.Request.Body.Close()
	}

	// 启动下载
	h.running = true
	forceRedownload := req.ForceRedownload

	utils.Info("▶️ [批量下载] 继续下载 %d 个待处理任务", pendingCount)

	// 启动后台下载
	go h.startBatchDownload(forceRedownload)

	h.sendSuccessResponse(Conn, map[string]interface{}{
		"message": "继续下载已启动",
		"pending": pendingCount,
	})
	return true
}

// HandleBatchClear 处理清除任务请求
func (h *BatchHandler) HandleBatchClear(Conn *SunnyNet.HttpConn) bool {
	path := Conn.Request.URL.Path
	if path != "/__wx_channels_api/batch_clear" {
		return false
	}

	// 处理 CORS 预检请求
	if Conn.Request.Method == "OPTIONS" {
		h.sendSuccessResponse(Conn, map[string]interface{}{"message": "OK"})
		return true
	}

	// 授权校验
	if h.getConfig() != nil && h.getConfig().SecretToken != "" {
		if Conn.Request.Header.Get("X-Local-Auth") != h.getConfig().SecretToken {
			h.sendErrorResponse(Conn, fmt.Errorf("unauthorized"))
			return true
		}
	}

	h.mu.Lock()
	cancel := h.cancelFunc
	busy := h.running || h.cancelFunc != nil
	queuedBatches := append([]batchQueueItem(nil), h.pendingBatches...)
	h.pendingBatches = nil
	if h.running && cancel != nil {
		h.running = false
	}
	h.mu.Unlock()

	if busy && cancel != nil {
		cancel()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			h.mu.RLock()
			stillBusy := h.running || h.cancelFunc != nil
			h.mu.RUnlock()
			if !stillBusy {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	h.mu.Lock()
	oldTasks := append([]BatchTask(nil), h.tasks...)
	for _, queuedBatch := range queuedBatches {
		oldTasks = append(oldTasks, queuedBatch.Tasks...)
	}
	taskCount := len(oldTasks)
	h.tasks = nil
	h.currentBatchID = ""
	h.ossUploader = nil
	h.completedBatches = make(map[string][]BatchTask)
	h.completedOrder = nil
	h.mu.Unlock()

	utils.Info("🗑️ [批量下载] 已清除所有任务（%d 个）", taskCount)

	for _, oldTask := range oldTasks {
		h.cleanupTaskArtifacts(oldTask.GopeedTaskID, oldTask.TempPath, true)
	}
	for exportRecordID := range collectBatchExportRecordIDs(oldTasks) {
		h.markExportFailed(exportRecordID, fmt.Errorf("关联的批量下载及 OSS 上传任务已清除"))
	}

	h.sendSuccessResponse(Conn, map[string]interface{}{
		"message": "任务已清除",
		"cleared": taskCount,
	})
	return true
}

// sendSuccessResponse 发送成功响应
func (h *BatchHandler) sendSuccessResponse(Conn *SunnyNet.HttpConn, data interface{}) {
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	headers.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	headers.Set("Pragma", "no-cache")
	headers.Set("Expires", "0")
	headers.Set("X-Content-Type-Options", "nosniff")
	if h.getConfig() != nil && len(h.getConfig().AllowedOrigins) > 0 {
		origin := Conn.Request.Header.Get("Origin")
		if origin != "" {
			for _, o := range h.getConfig().AllowedOrigins {
				if o == origin {
					headers.Set("Access-Control-Allow-Origin", origin)
					headers.Set("Vary", "Origin")
					headers.Set("Access-Control-Allow-Headers", "Content-Type, X-Local-Auth")
					headers.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
					break
				}
			}
		}
	}

	Conn.StopRequest(200, string(response.SuccessJSON(data)), headers)
}

// sendErrorResponse 发送错误响应
func (h *BatchHandler) sendErrorResponse(Conn *SunnyNet.HttpConn, err error) {
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	headers.Set("X-Content-Type-Options", "nosniff")
	if h.getConfig() != nil && len(h.getConfig().AllowedOrigins) > 0 {
		origin := Conn.Request.Header.Get("Origin")
		if origin != "" {
			for _, o := range h.getConfig().AllowedOrigins {
				if o == origin {
					headers.Set("Access-Control-Allow-Origin", origin)
					headers.Set("Vary", "Origin")
					headers.Set("Access-Control-Allow-Headers", "Content-Type, X-Local-Auth")
					headers.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
					break
				}
			}
		}
	}
	Conn.StopRequest(500, string(response.ErrorJSON(500, err.Error())), headers)
}
