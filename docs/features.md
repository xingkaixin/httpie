# 功能清单

## 命令

- `http [METHOD] URL [REQUEST_ITEM ...]`：HTTP 请求。
- `https`：默认 HTTPS 的相同入口。
- `httpie cli export-args [-f json]`：导出参数规格。
- `httpie cli check-updates`：检查版本。
- `httpie cli sessions upgrade HOSTNAME SESSION [--bind-cookies]`、`upgrade-all`：升级会话格式。
- 第三方插件管理和插件 SDK 经用户确认排除。

## 参数

下表从基线 `httpie/cli/definition.py` 提取。每个普通长参数还支持 `--no-OPTION` 恢复默认值。上游在常规解析结束后应用重置，因此重置不取决于参数出现顺序。`--no-sorted` 与 `--no-unsorted` 是显式别名。

| 参数及别名 | 行为 |
| --- | --- |
| `--json`, `-j` | (default) Serialize data items from the command line as a JSON object. |
| `--form`, `-f` | Serialize data items from the command line as form field data. |
| `--multipart` | Similar to --form, but always sends a multipart/form-data request (i.e., even without files). |
| `--boundary` | Specify a custom boundary string for multipart/form-data requests. Only has effect only together with --form. |
| `--raw` | Pass raw request data without extra processing. |
| `--compress`, `-x` | Compress the content with Deflate algorithm. |
| `--pretty` | Control the processing of console outputs. |
| `--style`, `-s` |  |
| `--no-unsorted` |  |
| `--no-sorted` |  |
| `--unsorted` | Disables all sorting while formatting output. |
| `--sorted` | Re-enables all sorting options while formatting output. |
| `--response-charset` | Override the response encoding for terminal display purposes. |
| `--response-mime` | Override the response mime type for coloring and formatting for the terminal. |
| `--format-options` | Controls output formatting. |
| `--print`, `-p` | Options to specify what the console output should contain. |
| `--headers`, `-h` | Print only the response headers. |
| `--meta`, `-m` | Print only the response metadata. |
| `--body`, `-b` | Print only the response body. |
| `--verbose`, `-v` | Make output more verbose. |
| `--all` | Show any intermediary requests/responses. |
| `--history-print`, `-P` |  |
| `--stream`, `-S` | Always stream the response body by line, i.e., behave like `tail -f`. |
| `--output`, `-o` | Save output to FILE instead of stdout. |
| `--download`, `-d` | Download the body to a file instead of printing it to stdout. |
| `--continue`, `-c` | Resume an interrupted download (--output needs to be specified). |
| `--quiet`, `-q` | Do not print to stdout or stderr, except for errors and warnings when provided once. |
| `--session` | Create, or reuse and update a session. |
| `--session-read-only` | Create or read a session without updating it |
| `--auth`, `-a` | Credentials for the selected (-A) authentication method. |
| `--auth-type`, `-A` | The authentication mechanism to be used. |
| `--ignore-netrc` | Ignore credentials from .netrc. |
| `--offline` | Build the request and print it but don’t actually send it. |
| `--proxy` | String mapping of protocol to the URL of the proxy. |
| `--follow`, `-F` | Follow 30x Location redirects. |
| `--max-redirects` | The maximum number of redirects that should be followed (with --follow). |
| `--max-headers` | The maximum number of response headers to be read before giving up (default 0, i.e., no limit). |
| `--timeout` | The connection timeout of the request in seconds. |
| `--check-status` | Exit with an error status code if the server replies with an error. |
| `--path-as-is` | Bypass dot segment (/../ or /./) URL squashing. |
| `--chunked` | Enable streaming via chunked transfer encoding. The Transfer-Encoding header is set to chunked. |
| `--verify` | If "no", skip SSL verification. If a file path, use it as a CA bundle. |
| `--ssl` | The desired protocol version to used. |
| `--ciphers` | A string in the OpenSSL cipher list format. |
| `--cert` | Specifies a local cert to use as the client-side SSL certificate. |
| `--cert-key` | The private key to use with SSL. Only needed if --cert is given. |
| `--cert-key-pass` | The passphrase to be used to with the given private key. |
| `--ignore-stdin`, `-I` | Do not attempt to read stdin |
| `--help` | Show this help message and exit. |
| `--manual` | Show the full manual. |
| `--version` | Show version and exit. |
| `--traceback` | Prints the exception traceback should one occur. |
| `--default-scheme` | The default scheme to use if not specified in the URL. |
| `--debug` | Print useful diagnostic information for bug reports. |

## 请求项

`Header:value` 设置头；`Header:` 删除头；`Header;` 发送空值；`Header:@file` 从文件读取。重复头保留多值。

`name==value` 追加查询参数，`name==@file` 嵌入文件内容。`name=value` 为字符串，`name:=json` 为 JSON 值，`=@` 与 `:=@` 从文件读取。支持反斜杠转义分隔符。

JSON 支持对象路径 `a[b]`、数组索引 `a[0]`、追加 `a[]`、顶层数组、稀疏数组及括号转义。冲突类型和非法路径必须报错。

`name@file` 为 multipart 文件，支持 `;type=`；`@file` 为原始正文。管道 stdin、`--raw`、原始文件和结构化正文之间遵循上游互斥规则。

## 其他契约

- 未显式指定方法时，有输入正文使用 POST，否则 GET。支持 localhost 简写、默认协议、URL 用户信息及路径规范化。
- Basic、Digest、Bearer；URL 凭据、`.netrc`、终端密码提示。
- 环境代理、显式代理、NO_PROXY、CA 路径、客户端证书、私钥口令。
- gzip、deflate 等响应解码，请求 deflate 压缩及 chunked 上传。
- 默认不跟随重定向；跟随时保留正确的方法和凭据边界。
- 管道默认仅输出正文，TTY 默认显示响应头与正文；离线默认显示请求。
- 输出选择、历史、元数据、JSON/XML 格式化、排序、颜色、编码和二进制抑制。
- 下载文件名推断、防止目录穿越、避免覆盖、断点续传、完整性检查。
- `HTTPIE_CONFIG_DIR`、XDG 和旧配置目录；默认选项、命名及匿名会话、只读会话、旧格式升级。

测试范围及环境限制见 [验证记录](validation.md)。主题、高亮与字符集依赖的实现和版本见 [架构](architecture.md)。
