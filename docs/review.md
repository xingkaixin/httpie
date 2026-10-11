# CLI 兼容性复审

复审日期：2026-10-11。基线仍为 `cli-master.zip` 中的 HTTPie 3.2.4，未改写 Python 基线以适应 Go 输出。范围为三个命令的内置功能；第三方 Python 插件及插件 SDK 按用户确认排除。

## 核对方法

逐项核对 [54 项参数清单](features.md)，再沿基线的参数解析、请求构建、Requests 交互、输出和持久化路径检查。对新发现的边界输入先运行 Python/Go 差分，确认差异后修复。对“必须在 stdin EOF 之前发出数据”这类不能仅靠最终 stdout 证明的行为，使用真实管道和本地服务验证。

| 功能组 | 基线源码 | 本轮重点与验证 |
| --- | --- | --- |
| 参数、别名、组合、缩写、重置 | `cli/definition.py`、`cli/argparser.py`、`cli/argtypes.py` | 必选值不能吞掉后续选项；短参数等号；帮助之前的校验；会话互斥；负数及非有限超时；所有原有差分继续执行 |
| URL、方法、请求项 | `cli/requestitems.py`、`cli/argparser.py` | 值与键的反斜杠转义；头部删除后重设；重复查询/表单字段按首个键分组；用户目录展开；上游嵌套 JSON 样例 |
| JSON、表单、multipart、文件 | `client.py`、`uploads.py` | 自定义 Content-Type 的 boundary；文件正文显示；大整数重复键排序；混合类型排序错误；不排序时保持原顺序 |
| stdin、chunked、压缩 | `uploads.py` | chunked 和显式 Content-Length 均在 EOF 前发送；chunked 文件正文按行显示；无 stdin 数据时的延迟警告；已有压缩与重放用例 |
| Basic、Digest、Bearer、netrc | `cli/argparser.py`、`plugins/builtin.py`、Requests | 用户名中的转义冒号；URL 凭据；netrc 引号、注释、宏及 account；编码与会话优先级；Digest 内部握手不多打印 |
| 重定向与认证边界 | `client.py`、`core.py`、Requests | 输出每次请求；显式 Host；默认端口等价；跨主机清除旧认证后读取目标 netrc；方法与正文重放 |
| 代理与 NO_PROXY | `client.py`、Requests 环境合并逻辑 | 大小写环境变量优先级、空值、CGI、协议/主机代理选择、IPv4/CIDR；原有 HTTP/HTTPS/SOCKS 集成检查 |
| TLS、超时、响应头上限 | `ssl_.py`、`client.py` | 复核 CA、客户端证书、加密私钥及 cipher/协议配置；继续执行本地 TLS、代理隧道、握手与读取超时、折叠头及头数限制测试 |
| 选择输出、历史、元数据、静默 | `core.py`、`models.py`、`output/writer.py` | 请求必须在连接失败前可见；消息分隔符；中间响应；原有退出码和 quiet 用例。基线未实际使用 history-print 的独立选择，保持其现有行为 |
| 响应解码、流式、格式与颜色 | `output/streams.py`、`formatters/`、`lexers/` | CR/LF 边界；空压缩正文与 HEAD；未知 MIME；TERM=dumb；JSON 注释、XSSI、XML token；二进制提示分隔；全部内置主题与字符集样例 |
| 下载、续传、文件名 | `downloads.py`、`cli/argparser.py` | 空文件的 206 校验；实际字节与总长度；未知总长；错误状态的文件创建；`-o-`；宽松 Content-Disposition、路径清理及重复 filename 参数 |
| 配置、会话、Cookie | `config.py`、`sessions.py`、Python CookieJar | 错误配置类型与损坏 JSON 区别；未知字段保留；Cookie 组内次序、覆盖、路径、域、secure、显式 Cookie 与会话合并；只读及旧格式升级 |
| 帮助与手册 | `cli/argparser.py`、`output/ui/man_pages.py` | 按终端宽度生成帮助；运行时调用 `man 1`；不可用时回退；HTTPIE_NO_MAN_PAGES；MANPAGER/PAGER；不再嵌入本机 man 渲染结果 |
| 管理命令与版本缓存 | `manager/`、`internal/update_warnings.py` | 子命令参数作用域、短参数/缩写、帮助、调试、升级目录不存在；基线 unknown 通道、预发布版本比较、缓存额外字段及静默提示时间 |

## 修复与提交

直接在 `main` 开发，按 mise、凭据、Cookie、参数、输出、重定向、管理命令、请求项、代理/上传、下载、手册、配置/缓存、JSON 排序等问题拆分提交。新增测试只覆盖已确认的外部行为差异，没有增加 SDK、插件系统或新的运行时服务。

mise 继续从项目 `mise.toml` 选择 Go 1.27.2，同时启用新的最低版本文件忽略设置。`mise exec -- go version` 和 `mise ls go` 均未再出现 `idiomatic.go.mod.go-directive` 警告。

## 证据与边界

最终差分数量、Go 检查结果见 [验证记录](validation.md)。本轮性能重新测量，结果与原始数据见 [性能报告](performance.md)。

差分比较退出码、stdout 字节和文件状态，并针对 stdin 警告、版本提示补充 stderr 存在性检查。动态进度、堆栈及底层错误文案仍不要求逐字一致。测试在本机 macOS arm64 执行；没有远程仓库，未声称跨平台 CI 已通过。该记录是参数及实现路径的复审和具体输入的验证，不是所有输入的穷举等价证明。
