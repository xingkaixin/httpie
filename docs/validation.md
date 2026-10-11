# 验证记录

## 基线与复现

`cli-master.zip` 为唯一源码基线。`scripts/setup-reference.sh` 校验 SHA-256，解压到 `.reference/cli-master`，使用隔离的 Python 3.11.15 环境安装原版。运行时依赖固定在 `scripts/reference-requirements.txt`。

```sh
mise exec uv@0.13.0 -- scripts/setup-reference.sh
mise run check
mise run parity
mise run bench
```

Go 构建版本由 `mise.toml` 固定为 1.27.2。项目设置 `idiomatic_version_file_ignore_minimum_versions = true`，避免启用全局 Go 版本文件探测时，把 `go.mod` 的最低兼容版本当成构建版本。这采用 [mise 的新版本探测行为](https://mise.jdx.dev/lang/go.html#version-files)，不会修改用户的全局配置。

原版未锁定完整依赖图。本次固定 `urllib3==2.2.3` 和 `charset-normalizer==3.3.2`：较新的 urllib3 2.8.0 在流式原始 deflate 场景丢失尾部字节；charset-normalizer 3.5.2 将上游 Big5 fixture 识别为 Johab，使原版自身两项测试失败。恢复到 3.3.2 后，上游六项请求/响应字符集自动检测测试全部通过。没有对这些错误输出进行归一化。

## 差分

`scripts/parity.py` 分别启动已安装的 Python CLI 和已编译的 Go CLI，比较退出码及 stdout 字节。测试使用本地 HTTP 服务；固定 multipart boundary，不引入动态服务端日期。终端场景使用 pseudo-terminal。

覆盖：参数及别名、默认值重置、请求方法、URL、头部、查询、嵌套 JSON、重复键、数值、表单、文件、multipart、stdin、压缩、响应解码、格式化、主题、字符集、重定向、状态码、超时和输入错误。测试包含上游嵌套 JSON、XML 和字符集样例。可用 `PARITY_FILTER` 重跑指定用例。

`scripts/state-parity.py` 对比会话文件的 JSON 内容、只读文件是否变更、Cookie、旧会话升级、配置默认值、下载文件字节和管理命令输出。配置目录、会话和下载文件均使用临时目录。

随机 Digest nonce、动态时间/进度、Go/Python 堆栈和底层错误文案不做字节一致断言；对应的认证结果、退出码、正文和文件内容仍需验证。

## 集成检查

Go 测试使用本地真实 HTTP/TLS/TCP 服务，验证密码验证成功和失败、CA、双向证书、加密私钥、密码套件表达式、HTTP/HTTPS 代理隧道、SOCKS4/4a/5/5h、代理认证和握手超时、Digest/307 重放、跨主机凭据清除、会话只读、下载续传及非法范围。只读响应头的测试确保不会等待未到达的正文。

`mise run check` 执行 gofmt 检查、`go vet` 和 `go test -race`。这些测试保护外部行为和连接/文件生命周期，不为包装函数添加单独的测试接口。

## 环境范围

本机 macOS arm64 已执行上述检查。GitHub Actions 定义了 Linux、macOS、Windows 构建及 Go 检查，以及 Linux 差分任务；没有远程仓库，因此不能声称远程 CI 或其他系统已经通过。

TLS 默认信任库及可用算法取决于 OpenSSL 安装。无声明字符集的检测仍属于启发式判断；明确编码的数据应通过 Content-Type 或 `--response-charset` 指定。测试结果证明已覆盖场景的行为，不是所有可能输入或所有平台的穷举证明。

## 最终结果

- `mise run check`：格式、静态分析、Go 行为及竞态检查通过。
- 命令差分：359/359 通过。
- 状态与管理命令对比：39/39 通过，包含损坏配置警告后继续运行。
- 性能：8/8 场景中位耗时、P95 和峰值 RSS 均改善；详见 [性能报告](performance.md)。
