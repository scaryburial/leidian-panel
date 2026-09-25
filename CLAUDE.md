# 项目说明（供 AI 助手 / 开发者参考）

这是 **雷电面板（ui3344）** —— 基于 3x-ui 的定制分支。

- 目录：`frontend/`（React+antd 前端，构建产物 embed 到 `internal/web/dist`）、`internal/`（Go 后端）、`packaging/`（安装脚本 `install.sh` / `create-inbounds.py` / `configure-subscription.py`）。
- 打包：`bash build-release.sh` → `dist/ui3344-linux-amd64.tar.gz`。
- 验证：前端 `npm run typecheck && npm run build`；后端 `go build ./...`。
- 注意：改前端后需重建二进制（前端 embed 进 Go 二进制）。
- 详细背景见仓库根目录的《雷电面板-*.md》与《定制说明.md》。
