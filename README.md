# HTTPie CLI · Go

以附带的 HTTPie 3.2.4 源码为基线，用 Go 实现 `http`、`https`、`httpie` 三个命令。保留内置请求、认证、会话、格式化、下载和管理功能。运行时不需要 Python，不提供 SDK，也不执行第三方 Python 插件。

## 构建

Go 版本固定在 `mise.toml`。TLS 使用 Cgo 调用 OpenSSL，以支持原有的 cipher 表达式、协议版本和客户端证书能力。需要 C 编译器、pkg-config，以及 OpenSSL 3 或 4 的开发文件。

macOS：

```sh
xcode-select --install
brew install mise openssl@3 pkg-config
export PKG_CONFIG_PATH="$(brew --prefix openssl@3)/lib/pkgconfig"
mise install
mise run build
```

Debian / Ubuntu（先安装 mise）：

```sh
sudo apt-get install build-essential pkg-config libssl-dev
mise install
mise run build
```

Windows 使用 MSYS2 UCRT64，安装 `mingw-w64-ucrt-x86_64-gcc`、`mingw-w64-ucrt-x86_64-openssl`、`mingw-w64-ucrt-x86_64-pkgconf`，在能找到 mise 的 UCRT64 shell 中运行相同命令。二进制运行时需要对应 OpenSSL 动态库；Windows 还需要把 UCRT64 的 `bin` 目录放入 PATH。

生成的命令位于 `bin/`。可以将该目录加入 PATH，或直接调用：

```sh
bin/http --offline example.org name=HTTPie count:=42
bin/http --help
bin/https example.org
bin/httpie cli sessions --help
```

## 安装

macOS / Linux 上，在项目目录执行：

```sh
mise run install
```

任务会先构建，再将 `http`、`https`、`httpie` 复制到 `~/.local/bin`，不需要 sudo。该目录加入 PATH 后，可以在任意目录直接调用：

```sh
http --version
http --offline example.org name=HTTPie count:=42
```

更新代码后重新执行 `mise run install`，即可覆盖更新已安装的命令。安装后的二进制不依赖项目目录，运行时仍需系统中的 OpenSSL 动态库。

## 验证

```sh
mise run check
mise exec uv@0.13.0 -- scripts/setup-reference.sh
mise run parity
mise run bench
```

`parity` 和 `bench` 需要 Python 基线，仅用于开发。基线解压至 `.reference/`，不会进入 CLI 运行路径。差分使用本地服务，性能结果写入 `.bench/results.json`。

详细目标、架构、功能和验证结果见 [docs](docs/README.md)。性能测量见 [性能报告](docs/performance.md)。
