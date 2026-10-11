# 架构

## 数据流

命令入口 → 配置默认项与参数解析 → 请求项及正文构建 → 认证与会话 → HTTP 传输 → 输出或下载 → 会话保存与退出状态。

## 模块

- `cmd/http`、`cmd/https`、`cmd/httpie`：进程入口，仅调用内部 CLI 并设置退出码。
- `internal/cli/options.go`：唯一的参数定义和重置语义，解析短参数组合及交错参数。
- `internal/cli/request.go`、`items.go`：URL、请求项、嵌套 JSON、文件、表单与 multipart。
- `internal/cli/transport.go`、`auth.go`：连接、代理、TLS、超时、重定向和内置认证。
- `internal/cli/output.go`：选择输出部分、格式化、颜色、字符集和流处理。
- `internal/cli/session.go`：配置及兼容会话格式，原子写入。
- `internal/cli/download.go`：文件名、续传和下载校验。
- `internal/cli/manager.go`：参数导出、会话升级和更新检查。

文件按行为归属拆分，不增加可插拔框架、通用命令总线或公开 SDK。使用 Go 标准库承载 HTTP 和 I/O；只有标准库不能覆盖的内置契约才引入依赖。

## 资源与错误

响应体及时关闭；普通重定向输出和文件传输保持流式 I/O。只有格式化需要完整内容时才缓冲。大文件上传使用可重新打开的正文来源，以便重定向或认证重试。

错误通过 CLI 边界转换为退出码：普通错误 1、超时 2、未跟随的重定向 3、HTTP 4xx 为 4、HTTP 5xx 为 5、重定向超限 6。HTTP 状态检查只在 `--check-status` 或下载模式启用。参数错误使用 1（与上游主入口一致）。
