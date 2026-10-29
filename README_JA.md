# LLM Dynamix Engine バックエンドソース

このディレクトリには、LLM Dynamix macOS アプリの `llmdynamix-engine` をビルドするための
Go バックエンドソースが含まれています。

ソースは `../CLIProxyAPI.pin` で固定され、リポジトリ内の
`../../scripts/sync-backend-source.sh` で同期されます。製品アプリ用のエンジンは
`../../scripts/build-backend-engine.sh` でビルドしてください。隣接 checkout から出荷用エンジンを
ビルドしないでください。

## ビルド

LLM Dynamix リポジトリのルートで実行します。

```bash
TARGET_ARCH=arm64 ./scripts/build-backend-engine.sh
TARGET_ARCH=x86_64 ./scripts/build-backend-engine.sh
```

出力先：

```text
src/Sources/Resources/llmdynamix-engine
```

## ランタイム既定値

- ランタイム製品名：`LLM Dynamix Engine`
- 既定のバックエンドポート：`12445`
- 既定の認証および設定ディレクトリ：`~/.llmdynamix`
- 任意の管理リリースメタデータリポジトリ：`jecruz/llmdynamix`
- アプリ同梱の静的アセットパス：`LLM Dynamix.app/Contents/Resources/static`

## 互換性の境界

Go module パスと内部 import パスは、`../CLIProxyAPI.pin` に記録された上流 module との互換性を
維持します。module 移行を計画し、完全にテストするまでは、製品名変更のためだけに Go import
パスを変更しないでください。

このディレクトリ内の開発例と Docker ファイルは、LLM Dynamix Engine のバイナリ名、コンテナ名、
ポート、ランタイムディレクトリを使用してください。
