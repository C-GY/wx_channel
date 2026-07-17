# Prometheus 监控快速开始

## 启用监控

普通版 `wx_channel.exe` 已内置 Prometheus 端点，不需要单独的 metrics 可执行文件。

在 `config.yaml` 中确认：

```yaml
metrics_enabled: true
metrics_port: 9090
```

然后启动：

```powershell
.\wx_channel.exe
```

日志出现以下内容即表示成功：

```text
Prometheus 监控已启动: http://localhost:9090/metrics
```

## 验证

浏览器打开：

```text
http://127.0.0.1:9090/metrics
```

或在 PowerShell 中执行：

```powershell
(Invoke-WebRequest http://127.0.0.1:9090/metrics -UseBasicParsing).StatusCode
```

预期返回 `200`。

## 修改端口或禁用

```yaml
metrics_enabled: false
metrics_port: 9191
```

修改后需要重启程序。端口冲突可用以下命令检查：

```powershell
Get-NetTCPConnection -LocalPort 9090 -ErrorAction SilentlyContinue
```
