# GitGud 构建产物上传

GitGud 的双平台构建完成后，`scripts/ci/artifacts/upload.go` 先在临时文件中生成完整 ZIP，再通过作业原有的 `CI_JOB_TOKEN` 上传到当前作业的 artifacts API。下载路径仍包含 `dist/<平台>-<架构>/` 下的三个程序，文件内容和权限保持，保留期仍为 7 天。

该入口解决 Runner 边压缩边发送、请求长度未知时上传反复遇到代理 HTTP 524 的兼容问题。请求使用确定的 `Content-Length`，上传重试复用同一 ZIP；不增加密钥、不修改游戏构建参数。日志记录归档大小、SHA256、HTTP 状态，不记录令牌、响应正文或原始网络错误。

只将 HTTP 201 视为成功。网络错误、408、429 和服务端 5xx 最多尝试三次，每次最多 150 秒，总时间最多 8 分钟；其他状态立即失败，重定向不携带令牌继续访问。最终失败仍使构建作业失败，不使用 `allow_failure` 或忽略退出码。领域、桥接、控制与频道测试保留，不下载其不消费的构建产物。

回归命令：

```sh
go test -count=1 scripts/ci/artifacts/upload.go scripts/ci/artifacts/upload_test.go
```

回归覆盖未知长度请求被拒绝、固定长度 multipart 成功、524 后同字节重传、错误状态与限次失败、重定向和凭据保护、ZIP 文件内容及路径保持。代理夹具用于验证请求差异；真实上传恢复须读取 GitGud 作业 HTTP 201 和下载后的归档摘要。
