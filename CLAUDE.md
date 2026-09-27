# 项目说明（供 AI 助手 / 开发者参考）

这是 **雷电面板（ui3344）** —— 基于 3x-ui 的定制分支。

- 目录：`frontend/`（React+antd 前端，构建产物 embed 到 `internal/web/dist`）、`internal/`（Go 后端）、`packaging/`（安装脚本 `install.sh` / `create-inbounds.py` / `configure-subscription.py`）、`tools/`（本地构建脚本）。
- 打包：`bash build-release.sh` → `dist/ui3344-linux-amd64.tar.gz`。
- 验证：前端 `npm run typecheck && npm run build`；后端 `go build ./...`。
- 注意：改前端后需重建二进制（前端 embed 进 Go 二进制）。

## ⚠️ Windows 上默认在本地编译，不要把编译放到验证服务器

**编译机只负责编译；产物打包后发到需要验证安装的服务器上运行。** 验证服务器是验证环境，
编译会吃满它的 CPU，把面板和验证过程一起拖垮。

Windows 上不能用 `GOOS=linux go build` 直接出包：本仓库依赖 `github.com/mattn/go-sqlite3`
（cgo，见 `internal/database/db.go`），`CGO_ENABLED=0` 编出来的二进制运行时会报
`go-sqlite3 requires cgo to work`。仓库的解法是用 **Zig 自带的 C 工具链**交叉编译
（`zig cc -target x86_64-linux-gnu`），不需要 WSL / Docker。

**第一次上手，先把依赖和编译软件全部下好**（`ziglang.org` 与 `proxy.golang.org` 在国内经常超时）：

```powershell
pwsh -File tools\build-local.ps1 -SetupOnly     # 装 Go/Zig/Node 并预热 npm 与 Go 模块缓存
```

然后构建并出包：

```powershell
pwsh -File tools\build-local.ps1 -Tag v1.5 -Token <CF Token> -OtpSecret <TOTP 密钥>
```

再把 `dist/ui3344-linux-amd64.tar.gz{,.sha256}` 传到验证服务器安装（脚本结尾会打印现成的 scp 命令）。

完整说明、常见坑与备选 Linux 编译路径见 **《雷电面板-Windows本地编译指南.md》**。
详细背景见仓库根目录的《雷电面板-*.md》与《定制说明.md》。
