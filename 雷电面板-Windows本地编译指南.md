# 雷电面板 — Windows 本地编译指南

> 面向下一位接手开发的人。**在 Windows 上默认走本地编译**：编译产物（安装包）在本机产出，
> 再把它发到需要验证安装的服务器上运行。不要在验证服务器上做编译——那台机器是**验证环境**，
> 不是构建机。

---

## 0. 为什么不能直接 `GOOS=linux go build`

本面板依赖 `github.com/mattn/go-sqlite3`（**cgo**，见 `internal/database/db.go`）。
只要 `CGO_ENABLED=0`，SQLite 在运行时会直接报
`Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work`。

所以要产出 Linux 二进制，就必须有一个能生成 Linux 目标代码的 C 编译器。
Windows 上没有 Linux 版 gcc；本仓库的做法是用 **Zig 自带的 C 工具链**做交叉编译
（`zig cc -target x86_64-linux-gnu`）。Zig 一条命令即可替代一整套交叉编译工具链，无需 WSL / Docker。

> 如果你的机器有 WSL2 或 Docker，也可以用它们编译（见 §6），但 Zig 是零依赖、最省事的路径。

---

## 1. 编译机一次性准备

**第一步永远是把依赖和编译软件全部下好**，不要等到构建时才现下——`ziglang.org` 与
`proxy.golang.org` 在国内网络下经常超时。

### 1.1 安装 Go（Windows）

```powershell
$Tools = "C:\Tools"; New-Item -ItemType Directory -Force -Path $Tools | Out-Null
curl.exe -L -o "$Tools\go.zip" "https://dl.google.com/go/go1.27.1.windows-amd64.zip"
tar.exe -xf "$Tools\go.zip" -C $Tools
& "$Tools\go\bin\go.exe" version      # 期望 go1.27.1 windows/amd64
```

> `go.mod` 里声明的是 `go 1.27.1`，用同版本能避免工具链自动下载。

### 1.2 安装 Zig（Windows）

`ziglang.org` 直连通常很慢甚至超时。**推荐让一台网络好的 Linux 机器代下再传回来**：

```bash
# 在一台网络好的 Linux 机器上
curl -L -o /root/zig-windows.zip https://ziglang.org/download/0.16.0/zig-x86_64-windows-0.16.0.zip
sha256sum /root/zig-windows.zip
```

```powershell
# 回到 Windows，拉回来并校验
scp root@<那台机器>:/root/zig-windows.zip C:\Tools\zig.zip
(Get-FileHash C:\Tools\zig.zip -Algorithm SHA256).Hash.ToLower()   # 与上面 sha256sum 比对
New-Item -ItemType Directory -Force -Path C:\Tools\zig | Out-Null
tar.exe -xf C:\Tools\zig.zip -C C:\Tools\zig
& (Get-ChildItem C:\Tools\zig -Recurse -Filter zig.exe | Select-Object -First 1).FullName version
```

### 1.3 Node（前端构建）

前端需要 Node 20+。本机若已有 `C:\Program Files\nodejs`，把它加进 PATH 即可。

### 1.4 预热依赖（关键，别跳过）

```powershell
$env:PATH = "C:\Program Files\nodejs;C:\Tools\go\bin;$env:PATH"
$repo = "<仓库路径>"

# 前端依赖
cd "$repo\frontend"
npm ci --no-audit --no-fund --registry=https://registry.npmmirror.com

# Go 依赖（proxy.golang.org 不通，必须换源；这一步会把模块缓存下满，约 450MB）
cd $repo
$env:GOPROXY = "https://goproxy.cn,direct"
$env:GOSUMDB = "sum.golang.google.cn"
$env:GOMODCACHE = "C:\Tools\gomodcache"
$env:GOCACHE    = "C:\Tools\gocache"
go mod download all
```

预热完成后，`C:\Tools\gomodcache` 应有 400MB 以上；后续编译不再需要网络。
**把这两个缓存目录固定下来**（而不是用默认的 `%USERPROFILE%\go\pkg\mod`），换机器/清 profile 都不会丢。

### 1.5 一键完成以上全部

直接运行仓库里的脚本即可（幂等，已装的会跳过）：

```powershell
pwsh -File tools\build-local.ps1 -SetupOnly
```

---

## 2. 构建（产出安装包）

```powershell
pwsh -File tools\build-local.ps1 -Tag v1.5
```

脚本做三件事，顺序不能颠倒（**前端构建产物会被 embed 进 Go 二进制**，先改前端不重建二进制等于没改）：

1. `frontend: npm run build` → 产出 `internal/web/dist/`（会被 embed）
2. 交叉编译 Linux 二进制（`GOOS=linux GOARCH=amd64 CGO_ENABLED=1 CC="zig cc -target x86_64-linux-gnu"`）
3. 组装 `dist/ui3344-linux-amd64.tar.gz` 并生成 `.sha256`

可用参数：

| 参数 | 说明 |
|---|---|
| `-Tag` | 版本号，注入 `uiVersion`（默认 `v1.5`） |
| `-SetupOnly` | 只做 §1 的环境准备，不编译 |
| `-NoFrontend` | 跳过前端构建（只改了 Go 代码时用，快很多） |
| `-Token` / `-OtpSecret` | 构建期注入的 CF Token / TOTP 密钥；不传则不注入（源码里就是空串） |

> ⚠️ `-Token` / `-OtpSecret` 的值会被写进二进制的只读数据段，**任何人拿到发布包都能用
> `strings` 提取**。共享凭据不要用这种方式分发；详见《雷电面板-项目说明与注意事项.md》。

---

## 3. 把安装包发到验证服务器

编译机只负责编译，**验证在服务器上做**：

```powershell
$d = Join-Path $env:TEMP "dsh-ssh"
$env:SSH_ASKPASS = "$d\askpass.cmd"; $env:SSH_ASKPASS_REQUIRE = "force"; $env:DISPLAY = "localhost:0"
# askpass.cmd 内容：两行 `@echo off` + `echo %DSH_SSH_PW%`
$env:DSH_SSH_PW = "<服务器密码>"

scp -o StrictHostKeyChecking=no -o PreferredAuthentications=password `
    -o PubkeyAuthentication=no -o NumberOfPasswordPrompts=1 `
    dist\ui3344-linux-amd64.tar.gz dist\ui3344-linux-amd64.tar.gz.sha256 `
    root@<服务器IP>:/root/
```

在服务器上安装并验证：

```bash
cd /root
sha256sum -c ui3344-linux-amd64.tar.gz.sha256
tar xzf ui3344-linux-amd64.tar.gz && cd ui3344 && bash install.sh
systemctl is-active ui3344
```

> 覆盖已装实例时：先 `cp -a /usr/local/ui3344/ui3344 /root/ui3344.bak.$(date +%s)` 备份，
> 再 `systemctl stop ui3344` → 替换二进制 → `systemctl start ui3344`。数据在 `/etc/ui3344/`，不受影响。

---

## 4. 验证清单（发布前必须过）

在**编译机**上：

```powershell
# Go：编译 + 全量测试（约 5-10 分钟）
go build ./...
gofmt -l internal/            # 必须为空
go test ./internal/... -count=1

# 前端
cd frontend
npm run typecheck
npx vitest run src/test/factory-defaults-contract.test.ts
```

在**验证服务器**上：确实装上、服务起来、订阅能拉、节点能连。

`tools/verify-linux.sh` 可以在 Linux 侧一次性跑完 Go 侧检查。

---

## 5. Windows 上的常见坑

| 现象 | 原因 / 处理 |
|---|---|
| `go-sqlite3 requires cgo` | 忘了 `CGO_ENABLED=1` 与 `CC="zig cc -target x86_64-linux-gnu"` |
| `proxy.golang.org` 超时 | 设 `GOPROXY=https://goproxy.cn,direct` |
| `ziglang.org` 下载卡住 | 让 Linux 机器代下再 `scp` 回来（§1.2） |
| `.ps1` 报语法错误、中文变乱码 | **Windows PowerShell 5.1 按 ANSI 解析 .ps1**。脚本要用 UTF-8 **带 BOM** 保存，或干脆只写 ASCII。`Set-Content -Encoding UTF8` 在 5.1 下会带 BOM，`[IO.File]::WriteAllText($p,$s,(New-Object Text.UTF8Encoding $true))` 可精确控制 |
| xray 报 `invalid character '茂'` | 配置文件被写成了带 BOM 的 UTF-8。生成 JSON 配置时用 ASCII 或 UTF-8 **无 BOM** |
| `tar.exe` 解压大 zip 报 `decompression failed` | 下载不完整（先校验 sha256 再解压） |

---

## 6. 备选：在 Linux 上编译

如果手上就有 Linux 机器，也可以直接编（与 CI 一致）：

```bash
apt-get install -y gcc          # cgo 需要
export PATH=/usr/local/go/bin:$PATH
cd frontend && npm ci && npm run build && cd ..
UI3344_TAG=v1.5 bash build-release.sh
```

但**验证服务器不要兼作编译机**：编译会吃掉 2 核机器的全部 CPU，把面板与验证过程拖到不可用。
