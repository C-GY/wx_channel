# Web 控制台与 API 令牌

## 当前行为

当前版本只提供一个控制台页面：

```text
http://127.0.0.1:2025/console
```

不存在 `/console/full` 路由。控制台 HTML 本身可直接读取；真正限制 API 访问的是本地 API 令牌。

## 保护本地 API

启动前设置 `WX_CHANNEL_TOKEN`：

```powershell
$env:WX_CHANNEL_TOKEN = 'replace-with-a-long-random-token'
.\wx_channel.exe
```

设置后，除健康检查和令牌验证外的 API 请求必须携带以下任一凭据：

```http
X-Local-Auth: replace-with-a-long-random-token
```

或：

```http
Authorization: Bearer replace-with-a-long-random-token
```

查询参数 `?token=...` 也受支持，但令牌可能出现在日志和浏览器历史中，不推荐日常使用。

示例：

```powershell
$headers = @{ 'X-Local-Auth' = 'replace-with-a-long-random-token' }
Invoke-RestMethod http://127.0.0.1:2025/api/settings -Headers $headers
```

## `WEB_CONSOLE_TOKEN` 的用途

`WX_CHANNEL_WEB_CONSOLE_TOKEN` 映射到 `web_console_token`，仅供下面的验证接口比对：

```text
POST /api/console/verify-token
```

请求体：

```json
{
  "token": "your-token"
}
```

它当前不会阻止 `/console` 页面加载，也不会替代 `WX_CHANNEL_TOKEN` 的 API 鉴权。因此如需真实访问控制，请配置 `WX_CHANNEL_TOKEN`。

## 公共端点

即使配置了 `WX_CHANNEL_TOKEN`，以下端点仍可匿名访问：

- `/api/health`
- `/api/system/health`
- `/api/v1/system/health`
- `/api/console/verify-token`

## 安全建议

- 控制台仅供本机或可信内网使用，不要直接暴露到公网。
- 使用至少 32 字符的随机令牌。
- 不要把包含真实令牌的 `config.yaml` 提交到 Git。
- 必须远程访问时，在前面增加 HTTPS 反向代理和独立身份认证。

PowerShell 生成随机令牌：

```powershell
-join ((48..57) + (65..90) + (97..122) | Get-Random -Count 48 | ForEach-Object { [char]$_ })
```
