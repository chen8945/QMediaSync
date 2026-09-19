# 反向代理与 SSE

> 职责：说明 QMediaSync 的同源部署、可信代理和 SSE 反向代理配置要求。
>
> 权威范围：本文档维护代理层行为；SSE 消息和恢复语义见 [实时事件](../architecture/realtime-events.md)，Cookie 与可信来源见 [认证会话](../architecture/authentication-sessions.md)。
>
> 修改时机：修改代理拓扑、SSE 路由、可信转发 header、跨源策略或开发代理时必须更新本文档。
>
> 相关代码：`backend/internal/controllers/event_stream.go`、`backend/internal/controllers/log_stream.go`、`frontend/vite.config.ts`。

生产环境由 QMediaSync 在同一 origin 托管前端；开发环境由 Vite 将相对 `/api` 代理到后端。当前前端 axios 和 `EventSource` 都使用相对 `/api/...`，不配置跨 origin Cookie SSE。`trustedOrigins` 只用于明确受控的额外浏览器来源，不应替代同源部署。

`/api/events/stream`、`/api/logs/stream` 和 `/api/sync/tasks/:id/stream` 是持续 SSE 响应。代理必须关闭缓冲并提供足够长的读取超时。Nginx 对应 location 至少设置 `proxy_http_version 1.1`、`proxy_buffering off`、`proxy_cache off`、`gzip off` 和较长 `proxy_read_timeout`；Caddy 使用 `flush_interval -1`。

## 浏览器侧 HTTP/2

在证书、TLS 终止和客户端兼容条件允许时，浏览器到反向代理应使用 HTTPS + HTTP/2。HTTP/1.1 浏览器通常按同一 origin 限制约 6 条并发连接；SSE 会长时间占用其中的连接。当前前端单个页面最多可同时建立全局事件、日志和同步任务详情 3 条 SSE，多标签页或其他长连接可能使后续 SSE 或普通请求排队。

这不是 QMediaSync 后端的 6 客户端限制，后端没有 SSE 订阅数上限。HTTP/2 会在浏览器与代理之间多路复用 stream，避免 HTTP/1.1 的同源连接上限成为 SSE 使用瓶颈。代理到 QMediaSync 的 upstream 仍可使用 HTTP/1.1，因此 Nginx 的 `proxy_http_version 1.1` 与浏览器侧启用 HTTP/2 并不冲突。直接向浏览器暴露默认 HTTP `12333` 端口时，仍存在 HTTP/1.1 连接上限风险。

代理绑定域名时必须保留原始 `Host`，并且只有可信代理可以传递 `X-Forwarded-Proto: https`。当前应用没有可配置的可信代理 IP 白名单，会直接读取该 header 参与 Cookie Secure 属性和同源判断；后端监听地址必须通过防火墙、内网或代理网络隔离，避免客户端直接伪造 forwarded header。

## 不变量

- SSE 代理不得缓存或缓冲事件流。
- 前端和 API 的生产访问必须同源；本项目不支持跨 origin Cookie SSE。
- 只有可信代理的 `X-Forwarded-Proto` 可以参与 Secure Cookie 和同源判断。

## 最小代理配置

以下 Nginx 示例将同源流量转发到本机 HTTP `12333`。证书、域名、访问控制和上传体积限制按实际部署补充；SSE 的 location 必须保留在所有 API 路由之前。

```nginx
server {
    listen 443 ssl http2;
    server_name qms.example.com;

    location ~ ^/api/(events/stream|logs/stream|sync/tasks/[^/]+/stream)$ {
        proxy_pass http://127.0.0.1:12333;
        proxy_http_version 1.1;
        proxy_set_header Host $http_host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;
        proxy_cache off;
        gzip off;
        proxy_read_timeout 1h;
    }

    location / {
        proxy_pass http://127.0.0.1:12333;
        proxy_set_header Host $http_host;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

使用 Caddy 时，对三个 SSE 路由使用单独的 `reverse_proxy` 并设置 `flush_interval -1`；其他同源请求再代理到 `127.0.0.1:12333`。无论使用哪种代理，均不得允许公网直接访问后端端口后再伪造 forwarded header。

## 浏览器写入请求被拒绝

搜索、列表读取正常，但保存、删除或手动启动失败时，先查看失败请求的 HTTP 状态和响应体。HTTP `403` 中的 `REQUEST_ORIGIN_INVALID` 表示来源校验失败；响应体的业务 `code=500` 不表示 HTTP `500`。旧版本对应文案为“请求来源无效”。其他错误含义见 [请求错误码](../architecture/authentication-sessions.md#请求错误码)。

来源校验比较浏览器 `Origin`（缺失时使用 `Referer`）与后端识别的协议、主机和端口。当前同源判断使用实际请求 `Host`，不会用 `X-Forwarded-Host` 替代它，因此仅设置后者不能修复被改写为 `127.0.0.1` 的 `Host`。

代理应保留浏览器访问的 `Host`，包括非默认外部端口；示例使用 `$http_host`，避免 `$host` 丢失外部端口。TLS 在可信代理终止时，还需传递正确的 `X-Forwarded-Proto`。存在多层代理时，应逐层核对最终到达 QMediaSync 的值，不能以代理之间的内部 HTTP 协议覆盖外部 HTTPS。来源缺失或格式无效也会被拒绝，不应直接放宽来源校验来绕过错误。

`CSRF_TOKEN_INVALID`（旧文案“CSRF 校验失败”）则应先刷新页面后重试，持续失败时重新登录。错误截图和诊断记录可以包含请求方法、接口路径、HTTP 状态与错误码，不能包含 Cookie、CSRF Token、API Key、完整 URL 参数或登录请求体。

## 验证方式

- 运行 `(cd backend && go test ./internal/controllers/ -run 'Test.*(Stream|Event|Log)')`。
- 在代理测试环境订阅三个 SSE 路由，确认连接持续、心跳或事件及时到达，且响应没有代理缓存。
