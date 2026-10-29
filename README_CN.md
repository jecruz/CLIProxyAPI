# LLM Dynamix Engine 后端源码

此目录包含 LLM Dynamix macOS 应用用于构建 `llmdynamix-engine` 的 Go 后端源码。

源码由 `../CLIProxyAPI.pin` 固定版本，并通过仓库内的
`../../scripts/sync-backend-source.sh` 同步。生产应用构建必须使用
`../../scripts/build-backend-engine.sh`，不要从相邻 checkout 构建发布引擎。

## 构建

在 LLM Dynamix 仓库根目录执行：

```bash
TARGET_ARCH=arm64 ./scripts/build-backend-engine.sh
TARGET_ARCH=x86_64 ./scripts/build-backend-engine.sh
```

输出文件：

```text
src/Sources/Resources/llmdynamix-engine
```

## 运行默认值

- 运行时产品名：`LLM Dynamix Engine`
- 默认后端端口：`12445`
- 默认认证和配置目录：`~/.llmdynamix`
- 可选管理发布元数据仓库：`jecruz/llmdynamix`
- 应用内置静态资源路径：`LLM Dynamix.app/Contents/Resources/static`

## 兼容边界

Go module 路径和内部 import 路径会继续保持与 `../CLIProxyAPI.pin` 记录的上游模块兼容。
除非已经规划并完整测试 module 迁移，否则不要为了产品改名而修改 Go import 路径。

此目录中的开发示例和 Docker 文件应使用 LLM Dynamix Engine 的二进制名称、容器名称、端口和运行目录。
