# AI Coding Agent Instructions for wx_channel

## Project Overview
**微信视频号下载助手** (WeChat Channel Video Downloader) is a Windows desktop application written in Go 1.24.3+ that:
- Intercepts and downloads videos from WeChat Channels
- Automatically decrypts encrypted videos
- Provides batch download capabilities and a web console for management
- Uses HTTP proxy interception to hook into WeChat's JavaScript

## Architecture Overview

### Core Layering
```
CLI Entry (cmd/root.go) 
  ↓
App Orchestrator (internal/app/App)
  ↓
HTTP Proxy (SunnyNet library) 
  ↓
Request Interceptor Chain:
  - ScriptHandler: Modifies WeChat JS
  - UploadHandler: Manages downloads  
  - RecordHandler: Logs download history
  - BatchHandler: Bulk operations
  - APIRouter: REST API endpoints
  ↓
Service Layer (internal/services/): 
  - DownloadRecordService
  - StatisticsService
  - QueueService, etc.
  ↓
Data Layer (SQLite via database/sql repositories in internal/database/)
```

### Critical Components

| Component | Role | Key Files |
|-----------|------|-----------|
| **Proxy Interception** | Hooks HTTP requests using SunnyNet | `app.go`, `handlers/*.go` |
| **JavaScript Injection** | Injects custom JS to intercept WeChat video events | `internal/handlers/script.go`, `internal/assets/inject/` |
| **Download Engine** | Integrates Gopeed for download management | `services/gopeed_service.go` |
| **WebSocket Hub** | Real-time comms with web console | `internal/websocket/hub.go`, `handlers/websocket.go` |
| **Configuration** | Dynamic Viper-based config with DB override | `internal/config/config.go` |

## Essential Development Patterns

### 1. Handler Chain Pattern
Each request flows through multiple handlers that can intercept/modify it:
```go
// Handlers implement router.Interceptor interface
func (h *MyHandler) Handle(Conn *SunnyNet.HttpConn) bool {
    // Return true if handled, false to pass to next handler
}
```
Add new handlers to `internal/app/app.go` in the `requestInterceptors`/`responseInterceptors` chains.

### 2. Dependency Injection via App Constructor
All handlers receive dependencies through constructors:
```go
func NewUploadHandler(cfg *config.Config, wsHub *websocket.Hub, gopeedService *services.GopeedService) *UploadHandler
```
Services are singleton instances created in `internal/app/app.go` and shared across handlers.

### 3. JavaScript Interception Events
WeChat JS fires custom events via eventbus (defined in `internal/assets/inject/eventbus.js`):
- `PCFlowLoaded`: Video list loaded
- `FeedProfileLoaded`: Single video details loaded  
- `BeforeDownloadMedia`: Before video fetch
- `MediaDownloaded`: Video fetch complete

New handlers intercept these via regex replacements in JavaScript strings. See `script.go` for patterns.

### 4. Configuration Priority
1. Command-line flags (currently the proxy port)
2. Environment variables (`WX_CHANNEL_*`)
3. YAML config file (`config.yaml`)
4. Defaults

When modifying config, use `config.Get()` to get current instance, not `Config` passed at startup.

### 5. WebSocket Communication Pattern
For web console updates:
```go
app.WSHub.Broadcast([]byte(`{"type":"download_progress","progress":45}`))
```
Clients receive real-time updates without polling.

## Critical Files to Understand

| File | Purpose | Read First? |
|------|---------|---|
| `internal/app/app.go` | Main App struct, initializes all components | ✅ Yes |
| `internal/handlers/script.go` | ~1500 lines, most complex - modifies WeChat JS | ⚠️ Complex |
| `internal/router/api_routes.go` | REST API routing and CORS | ✅ Yes |
| `internal/services/gopeed_service.go` | Download orchestration via Gopeed lib | When modifying downloads |
| `config.yaml.example` | Configuration schema | Reference |
| `web/` | Frontend source (Vue/HTML) | When fixing UI issues |

## Build & Testing

### Building
```powershell
# The script restores pinned SunnyNet runtime files and sets required CGO flags.
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 -Action test
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 -Action build

# Run from source
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 -Action run
```

### Key Dependencies
- `github.com/qtgolang/SunnyNet`: HTTP proxy interception (Windows only)
- `github.com/GopeedLab/gopeed`: Download engine integration
- `github.com/mattn/go-sqlite3`: Local records database
- `github.com/spf13/cobra`: CLI framework
- `github.com/coder/websocket`: WebSocket support

### Testing
The repository has unit and integration-style package tests. Run all of them through `scripts/dev.ps1 -Action test` so the required CGO flags are present. When adding tests:
- Use Go's testing package in `*_test.go` files
- Mock SunnyNet/Gopeed dependencies
- Test service logic independently from HTTP handlers

## Common Tasks & Approaches

### Adding a New Configuration Option
1. Add field to `Config` struct in `internal/config/config.go`
2. Add YAML mapping via `mapstructure` tag
3. Set default in `loadConfig()` via Viper
4. Access via `config.Get()` in handlers

### Intercepting a New WeChat API
1. Find the JavaScript pattern in `script.go` 
2. Use regexp to locate the function (see `finderPcFlow` example)
3. Inject interrupt code that calls `WXU.emit(WXU.Events.YourEvent, data)`
4. Listen for event in Go handlers via request/response inspection

### Adding a New Download Endpoint
1. Create handler in `internal/handlers/`
2. Register in `APIRouter.registerRoutes()` 
3. Use `NewSunnyNetResponseWriter` to write responses
4. Broadcast WebSocket updates via `app.WSHub.Broadcast()`

### Debugging Request Interception
1. Check logs in `config.yaml` (set `log_file`, `max_log_size_mb`)
2. Use `utils.LogFileInfo("message")` for detailed logging
3. Enable `save_page_js: true` in config to inspect intercepted JavaScript
4. Monitor WebSocket messages in web console

## Critical Conventions

- **Error Handling**: Always log errors with context. No silent failures.
- **Goroutines**: Use goroutine-safe patterns (sync.Mutex, channels). See `handlers/websocket.go` for Hub pattern.
- **JSON Responses**: Use `response.APIResponse` struct for consistency
- **Database**: Use repository pattern (e.g., `SettingsRepository`). Queries live in `internal/database/`.
- **Path Handling**: Use `filepath` package for Windows compatibility (not string concatenation)
- **File Operations**: Handle cleanup in defer blocks; watch for lock contention in concurrent downloads

## Known Limitations & Workarounds

1. **Windows-Only**: SunnyNet proxy library is Windows-specific. No Linux/macOS support.
2. **WeChat JS Changes**: Regex patterns for JS interception can break when WeChat updates bundled code. Keep implementation notes and public docs aligned with the current handlers.
3. **Concurrent Download Issues**: Multiple handlers accessing same file can cause corruption. Semaphores used to limit concurrency (see `UploadHandler.chunkSem`).
4. **Config Reloading**: Not all config changes take effect immediately. Some require restart or manual handler reload.

## Documentation Resources
- **Build and local run**: `docs/BUILD.md`
- **API endpoints**: `web/docs/API.md`
- **Changelog**: `CHANGELOG.md`
- **Web console**: `docs/WEB_CONSOLE.md`
