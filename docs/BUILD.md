# Windows 构建与运行指南

## 适用范围

本项目是 Windows 本地代理程序。SunnyNet、进程注入和 SQLite 依赖都使用 CGO，因此推荐直接在 64 位 Windows 10 或更高版本上构建；不要使用 `CGO_ENABLED=0`。

## 环境要求

| 工具 | 要求 | 验证命令 |
|---|---|---|
| Go | `1.24.3+`，以根目录 `go.mod` 为准 | `go version` |
| MinGW-w64 / GCC | 64 位、支持 CGO；已验证 WinLibs GCC 16.1.0 UCRT | `gcc --version` |
| Git | 可选，用于克隆和查看版本信息 | `git --version` |
| go-winres | 仅重新生成 Windows 图标或版本资源时需要 | `go-winres --help` |

Go 与 GCC 的 `bin` 目录都必须位于 `PATH`。确认 Go 已启用 CGO：

```powershell
go env CGO_ENABLED CC CXX
```

预期至少看到 `1`、`gcc`、`g++`。

如果当前网络不能访问 `proxy.golang.org`，可切换 Go 模块代理：

```powershell
go env -w GOPROXY=https://goproxy.cn,direct
```

## 推荐流程

仓库提供了统一开发脚本，它会：

- 检查 `go` 与 `gcc`；
- 从校验过的 `SunnyNet v1.0.3` Go 模块补齐两个 `nfapi.dll`（仅在缺失时）；
- 设置当前 MinGW 所需的 CGO 编译和链接参数；
- 执行测试、构建或运行。

### 1. 下载依赖并运行测试

```powershell
go mod download
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 -Action test
```

### 2. 构建

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 -Action build
```

默认输出：

```text
.tmp_runtime\wx_channel.exe
```

指定其他输出位置：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 `
  -Action build `
  -Output .\build\wx_channel.exe
```

### 3. 从源码运行

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 -Action run
```

传递程序参数：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 -Action run -- -p 8080
```

要让视频号页面出现注入按钮，请在管理员 PowerShell 中运行。非管理员模式仍可启动 Web 控制台和本地 API，但进程注入引擎可能无法启动。

## 直接运行预编译版本

仓库或 Release 包中已有可执行文件时，不需要 Go/GCC：

```powershell
.\wx_channel.exe
```

常用命令：

```powershell
.\wx_channel.exe version
.\wx_channel.exe -p 8080
.\wx_channel.exe uninstall
```

三个发行变体：

| 文件 | 默认能力 |
|---|---|
| `wx_channel.exe` | 普通版，关闭 Hub 与雷达 |
| `wx_channel_cloud.exe` | 开启 Hub，关闭雷达 |
| `wx_channel_radar.exe` | 关闭 Hub，开启雷达 |

`config.yaml` 可覆盖对应默认值。

## 手动构建

如果不使用 `scripts/dev.ps1`，PowerShell 中需要显式设置以下参数：

```powershell
$env:CGO_ENABLED = '1'
$env:CGO_CFLAGS = '-std=gnu17 -D_WIN32_WINNT=0x0501'
$env:CGO_CXXFLAGS = '-std=gnu++17 -D_WIN32_WINNT=0x0501'
$env:CGO_LDFLAGS = '-Wl,--allow-multiple-definition -lwinpthread'

go test ./...
go build "-ldflags=-w -s -extldflags '-static'" -o .\build\wx_channel.exe .
.\build\wx_channel.exe version
```

这些参数分别处理：

- GCC 16 默认 C23 与 `go-libutp` 的 `bool` 类型冲突；
- 新版 MinGW IP Helper 头文件与 SunnyNet 兼容定义冲突；
- Gopeed 与项目数据库驱动同时链接 SQLite 时的重复符号。

## 发行构建

生成普通版、Hub 版和雷达版：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build-variants.ps1
```

只生成普通版和 Hub 版：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build-dual.ps1
```

脚本会自动设置 CGO 参数、按变体临时修改默认配置，并在结束后恢复 `internal/config/config.go`。

如需重新生成 Windows 资源，脚本会优先使用已安装的 `go-winres`，否则通过 `go run` 临时执行：

```powershell
go install github.com/tc-hib/go-winres@latest
go-winres make
```

## 启动验证

程序默认监听三个端口：

| 端口 | 用途 |
|---|---|
| `2025` | HTTP 代理、Web 控制台、本地 API |
| `2026` | WebSocket 与管理 API（代理端口 + 1） |
| `9090` | Prometheus 指标（启用时） |

启动后执行：

```powershell
Invoke-RestMethod http://127.0.0.1:2025/api/health
Invoke-WebRequest http://127.0.0.1:2025/console -UseBasicParsing
Invoke-WebRequest http://127.0.0.1:9090/metrics -UseBasicParsing
```

健康检查应返回 `success: true`，控制台应返回 HTTP 200。

## 常见构建错误

### `nfapi.dll: no matching files found`

执行一次开发脚本即可从固定版本模块补齐：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 -Action build
```

### `typedef uint8 bool` 或 `MIB_TCPROW2` 重定义

说明绕过了项目脚本，或缺少上面的 `CGO_CFLAGS` / `CGO_CXXFLAGS`。改用 `scripts/dev.ps1`。

### `multiple definition of sqlite3_*`

缺少 `CGO_LDFLAGS=-Wl,--allow-multiple-definition -lwinpthread`。改用项目脚本。

### Web 控制台可用，但视频号没有下载按钮

查看日志是否出现“注入引擎启动失败”。关闭现有进程后，在管理员 PowerShell 中重新启动。

## 相关文档

- [安装指南](INSTALLATION.md)
- [配置说明](CONFIGURATION.md)
- [故障排除](TROUBLESHOOTING.md)
