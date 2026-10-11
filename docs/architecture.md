# 架构

## 数据流

命令入口 → 配置默认项与参数解析 → 请求项及正文构建 → 认证与会话 → HTTP 传输 → 输出或下载 → 会话保存与退出状态。

## 模块

| 文件 | 职责 |
| --- | --- |
| `cmd/http`、`cmd/https`、`cmd/httpie` | 进程入口，退出码 |
| `internal/cli/options.go` | 参数定义、短参数组合、交错参数、重置语义 |
| `request.go`、`items.go`、`json.go`、`uploads.go` | URL、请求项、嵌套 JSON、文件、表单、可重放的 multipart 正文 |
| `transport.go`、`tls_openssl.go` | HTTP/1.1、代理、连接、空闲超时、OpenSSL TLS |
| `auth.go`、`run.go` | 认证、重定向、请求重放、信号取消、执行顺序 |
| `output.go`、`colors.go`、`xml.go` | 输出选择、格式化、颜色、编码、流处理 |
| `session.go` | 配置目录、默认参数、会话和 Cookie 持久化 |
| `download.go` | 文件名、续传、完整性校验、进度显示 |
| `manager.go`、`updates.go` | 帮助、参数导出、会话升级、更新检查 |

所有实现均位于 `internal/`。没有公开库接口、插件框架或额外服务进程。

## HTTP 与 TLS

使用 `net.Conn`、`bufio` 和 `net/http.ReadResponse` 实现 HTTP/1.1 传输。自行写入请求行和头部，是为了保留头部顺序、重复头、空头及删除 Host 等原版行为。正文按流传输，允许 chunked、认证重试与重定向重放。

TLS 交给 OpenSSL。Go 标准库不能覆盖原版支持的全部 cipher 表达式、套件和 security level，因此使用 Cgo 与内存 BIO 连接 Go 网络流。HTTP/HTTPS/SOCKS 代理和超时仍由 Go 管理。构建需要 C 编译器、pkg-config 和 OpenSSL 开发文件，运行需要对应动态库；不支持 `CGO_ENABLED=0` 构建。

默认使用 OpenSSL 的信任库。`--verify`、`REQUESTS_CA_BUNDLE`、`CURL_CA_BUNDLE` 可指定信任来源。不同机器上的 CA 库、可用协议和默认密码套件由 OpenSSL 安装决定。只有建立 TLS 连接时才读取自定义 CA 和加载客户端证书。

## I/O 与状态

原始文件和 multipart 按需打开，不整体读入。stdin 和请求压缩写入临时文件，以便重放且控制内存。格式化完整 JSON/XML 时需要缓冲；原始管道输出和下载保持流式 I/O。

会话兼容旧字典格式和新列表格式，保留未知元数据。通过同目录临时文件和重命名写入配置与会话。只读会话遵循原版“已有文件不更新，新会话可创建”的行为。

退出码：普通错误 1、超时 2、未跟随重定向 3、HTTP 4xx 为 4、HTTP 5xx 为 5、重定向超限 6、用户中断 130。HTTP 状态检查仅在 `--check-status` 或下载模式启用。错误诊断和堆栈使用 Go/OpenSSL 的信息。

## 外部实现

Chroma 提供一般语法高亮，JSON 和 HTTP 使用与原版 ANSI token 对应的处理。内置主题表由基线 Pygments 导出，许可证保留在 `LICENSES/Pygments.txt`。Brotli、Chardet 和 `golang.org/x/text` 分别补充响应解压、无声明字符集检测和字符集转换。依赖版本锁定在 `go.mod`、`go.sum`。
