# 安装与首次运行

## 系统要求

- Windows 10 或更高版本（64 位推荐）
- 最新版微信
- 预编译版本无需安装开发工具
- 从源码运行需要 Go `1.24.3+` 和支持 CGO 的 64 位 MinGW-w64/GCC

## 方式一：运行预编译版本（推荐）

1. 从 [GitHub Releases](https://github.com/nobiyou/wx_channel/releases) 下载 `wx_channel.exe`。
2. 解压到可写目录，例如 `C:\wx_channel`。
3. 在该目录打开管理员 PowerShell。
4. 启动：

```powershell
.\wx_channel.exe
```

管理员权限用于安装根证书和启动 `WeChatAppEx.exe` 进程注入。非管理员运行时，Web 控制台和本地 API 通常仍可使用，但视频号页面可能不会出现下载按钮。

## 方式二：从源码运行

### 1. 安装开发环境

安装并验证 Go 与 GCC：

```powershell
go version
gcc --version
go env CGO_ENABLED CC CXX
```

`CGO_ENABLED` 应为 `1`，编译器应为 `gcc` / `g++`。

### 2. 获取源码

```powershell
git clone https://github.com/nobiyou/wx_channel.git
cd wx_channel
go mod download
```

### 3. 测试、构建或运行

```powershell
# 全量测试
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 -Action test

# 构建到 .tmp_runtime\wx_channel.exe
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 -Action build

# 从源码直接运行
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 -Action run
```

开发脚本会设置项目需要的 CGO 参数，并在缺失时从固定的 `SunnyNet v1.0.3` Go 模块恢复 `nfapi.dll`。完整说明见[构建指南](BUILD.md)。

## 首次运行

### 根证书

程序会检测名为 `SunnyNet` 的根证书：

- 已存在：直接继续启动；
- 可自动安装：日志显示“证书安装成功”；
- 权限不足：证书保存为 `downloads\SunnyRoot.cer`，请手动安装到“受信任的根证书颁发机构”。

安装或更新证书后，重新打开微信视频号页面。

### 进程注入

成功日志应包含：

```text
视频号注入引擎已就绪 (WeChatAppEx.exe)
```

如果出现“注入引擎启动失败”，关闭程序后在管理员 PowerShell 中重启。

## 验证服务

默认监听：

- `2025`：代理、Web 控制台、本地 API
- `2026`：WebSocket 与管理 API
- `9090`：Prometheus 指标（启用时）

检查健康状态：

```powershell
Invoke-RestMethod http://127.0.0.1:2025/api/health
```

返回 `success: true` 后，打开：

```text
http://127.0.0.1:2025/console
```

## 常用配置

```powershell
# 修改端口
.\wx_channel.exe -p 8080

# 修改下载目录
$env:WX_CHANNEL_DOWNLOAD_DIR='D:\Videos'
.\wx_channel.exe

# 查看版本
.\wx_channel.exe version
```

需要独立运行、避免连接可选 Hub 时，在 `config.yaml` 中设置：

```yaml
cloud_enabled: false
radar_enabled: false
hub_sync:
  enabled: false
  push_enabled: false
```

更多配置见[配置说明](CONFIGURATION.md)。

## 停止与卸载

前台运行时按 `Ctrl+C` 停止。

卸载根证书：

```powershell
.\wx_channel.exe uninstall
```

随后可按需删除程序目录；`downloads\`、`logs\` 和 `config.yaml` 包含本地数据与设置，删除前请先备份。

## 常见问题

- 端口冲突：`Get-NetTCPConnection -LocalPort 2025`
- 页面无按钮：确认管理员权限，并查看注入引擎日志
- 控制台打不开：确认 `/api/health` 可访问
- 源码构建失败：使用 `scripts\dev.ps1`，不要直接关闭 CGO

更多信息见[故障排除](TROUBLESHOOTING.md)。
