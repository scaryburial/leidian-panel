#requires -Version 5.1
<#
  leidian-panel local build for Windows.

  Produces the Linux release package ON THIS MACHINE, then you ship the package
  to the verification server. Do NOT compile on the verification server: that box
  is the verification environment, and a build saturates its CPU.

  Why zig: this repo depends on github.com/mattn/go-sqlite3 (cgo). CGO_ENABLED=0
  builds a binary that dies at runtime with "go-sqlite3 requires cgo to work", so
  we need a C compiler that targets Linux. Zig ships one; `zig cc
  -target x86_64-linux-gnu` replaces a whole cross toolchain, no WSL/Docker needed.

  Usage:
    pwsh -File tools\build-local.ps1 -SetupOnly          # prepare toolchain + deps ONLY
    pwsh -File tools\build-local.ps1 -Tag v1.5           # prepare (idempotent) + build
    pwsh -File tools\build-local.ps1 -NoFrontend         # Go-only change, faster

  NOTE: kept ASCII-only on purpose. Windows PowerShell 5.1 parses .ps1 files as
  ANSI unless they carry a UTF-8 BOM, so non-ASCII text here breaks the parser.
#>
[CmdletBinding()]
param(
  [string]$Tag = "v1.5",
  [string]$Token = "",
  [string]$OtpSecret = "",
  [switch]$SetupOnly,
  [switch]$NoFrontend,
  [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

# ---------------------------------------------------------------- configuration
$RepoRoot = Split-Path -Parent $PSScriptRoot
$ToolsDir = "C:\Tools"
$GoRoot   = Join-Path $ToolsDir "go"
$GoExe    = Join-Path $GoRoot "bin\go.exe"
$ZigExe   = $null
$NodeDir  = "C:\Program Files\nodejs"
$ModCache = Join-Path $ToolsDir "gomodcache"
$BuildCache = Join-Path $ToolsDir "gocache"
$DistDir  = Join-Path $RepoRoot "dist"
$GoVersion = "1.27.1"
$ZigVersion = "0.16.0"
$Goproxy  = "https://goproxy.cn,direct"
$NpmRegistry = "https://registry.npmmirror.com"

function Step($msg) { Write-Host ""; Write-Host ("=" * 70); Write-Host "  $msg"; Write-Host ("=" * 70) }
function Ok($msg)   { Write-Host "  [ok]   $msg" }
function Warn($msg) { Write-Host "  [warn] $msg" -ForegroundColor Yellow }
function Die($msg)  { Write-Host "  [fail] $msg" -ForegroundColor Red; exit 1 }

function Find-Zig {
  $candidates = @()
  if (Test-Path (Join-Path $ToolsDir "zig")) {
    $candidates += (Get-ChildItem (Join-Path $ToolsDir "zig") -Recurse -Filter zig.exe -ErrorAction SilentlyContinue | Select-Object -ExpandProperty FullName)
  }
  $candidates += (Get-ChildItem $ToolsDir -Directory -ErrorAction SilentlyContinue | Where-Object { $_.Name -like "zig-*" } | ForEach-Object { Join-Path $_.FullName "zig.exe" })
  foreach ($c in $candidates) { if ($c -and (Test-Path $c)) { return $c } }
  $cmd = Get-Command zig.exe -ErrorAction SilentlyContinue
  if ($cmd) { return $cmd.Source }
  return $null
}

# =========================================================== 1. toolchain setup
Step "1) Toolchain"

if (-not (Test-Path $GoExe)) {
  Warn "Go not found at $GoExe -- downloading Go $GoVersion"
  New-Item -ItemType Directory -Force -Path $ToolsDir | Out-Null
  $zip = Join-Path $ToolsDir "go.zip"
  & curl.exe -L --max-time 900 -o $zip "https://dl.google.com/go/go$GoVersion.windows-amd64.zip"
  if (-not (Test-Path $zip)) { Die "Go download failed" }
  if (Test-Path $GoRoot) { Remove-Item $GoRoot -Recurse -Force }
  & tar.exe -xf $zip -C $ToolsDir
  if (-not (Test-Path $GoExe)) { Die "Go extraction failed" }
}
Ok ("go  " + (& $GoExe version))

$ZigExe = Find-Zig
if (-not $ZigExe) {
  Warn "Zig not found. ziglang.org is usually too slow to download directly."
  Write-Host "       Fetch it on a fast Linux host, then copy it over:"
  Write-Host "         curl -L -o /root/zig-windows.zip https://ziglang.org/download/$ZigVersion/zig-x86_64-windows-$ZigVersion.zip"
  Write-Host "         sha256sum /root/zig-windows.zip"
  Write-Host "         scp root@<host>:/root/zig-windows.zip $ToolsDir\zig.zip"
  Write-Host "         tar.exe -xf $ToolsDir\zig.zip -C $ToolsDir\zig"
  Die "Zig is required (cgo cross-compilation). Install it and re-run."
}
Ok ("zig " + (& $ZigExe version) + "  ($ZigExe)")

if (-not (Test-Path (Join-Path $NodeDir "node.exe"))) {
  $cmd = Get-Command node.exe -ErrorAction SilentlyContinue
  if ($cmd) { $NodeDir = Split-Path -Parent $cmd.Source } else { Die "Node.js not found (need 20+)" }
}
Ok ("node " + (& (Join-Path $NodeDir "node.exe") -v))

$env:PATH = "$NodeDir;$GoRoot\bin;$env:PATH"

# ======================================================== 2. dependency warm-up
Step "2) Dependencies (warm the caches -- do this BEFORE you need to build)"

New-Item -ItemType Directory -Force -Path $ModCache, $BuildCache | Out-Null
$env:GOPROXY = $Goproxy
$env:GOSUMDB = "sum.golang.google.cn"
$env:GOMODCACHE = $ModCache
$env:GOCACHE = $BuildCache
$env:GOFLAGS = "-mod=mod"

Push-Location $RepoRoot
Write-Host "  > go mod download all  (prefers Go proxy, may take a few minutes the first time)"
& $GoExe mod download all
if ($LASTEXITCODE -ne 0) { Warn "go mod download reported a non-zero exit; continuing" }
Pop-Location
$modSize = [math]::Round(((Get-ChildItem $ModCache -Recurse -File -ErrorAction SilentlyContinue | Measure-Object Length -Sum).Sum / 1MB), 1)
Ok "Go module cache: $ModCache ($modSize MB)"

if (-not (Test-Path (Join-Path $RepoRoot "frontend\node_modules"))) {
  Push-Location (Join-Path $RepoRoot "frontend")
  Write-Host "  > npm ci --registry=$NpmRegistry"
  & (Join-Path $NodeDir "npm.cmd") ci --no-audit --no-fund --registry=$NpmRegistry
  Pop-Location
}
Ok "frontend node_modules present"

if ($SetupOnly) { Step "SetupOnly: done. Toolchain and caches are ready."; exit 0 }

# ============================================================== 3. frontend
Step "3) Frontend build (its output is embedded into the Go binary)"
if ($NoFrontend) {
  Warn "-NoFrontend: skipped. Only do this when you changed Go code alone."
} else {
  Push-Location (Join-Path $RepoRoot "frontend")
  & (Join-Path $NodeDir "npm.cmd") run build
  if ($LASTEXITCODE -ne 0) { Pop-Location; Die "frontend build failed" }
  Pop-Location
  $distFiles = (Get-ChildItem (Join-Path $RepoRoot "internal\web\dist") -Recurse -File -ErrorAction SilentlyContinue | Measure-Object).Count
  Ok "internal/web/dist: $distFiles files"
}

# ========================================================= 4. cross compile
Step "4) Cross-compile Linux binary (cgo via zig)"
$env:GOOS = "linux"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "1"
$env:CC = "$ZigExe cc -target x86_64-linux-gnu"
$env:CXX = "$ZigExe c++ -target x86_64-linux-gnu"

$ldflags = "-X github.com/mhsanaei/3x-ui/v3/internal/config.uiVersion=$Tag -X github.com/mhsanaei/3x-ui/v3/internal/config.version=3.7.0"
if ($Token)     { $ldflags += " -X github.com/mhsanaei/3x-ui/v3/internal/web/service.defaultCFApiToken=$Token" }
if ($OtpSecret) { $ldflags += " -X github.com/mhsanaei/3x-ui/v3/internal/web/service.defaultDomainOtpSecret=$OtpSecret" }
if ($Token -or $OtpSecret) {
  Warn "Injected credentials land in .rodata and are recoverable with 'strings' on the release package."
}

New-Item -ItemType Directory -Force -Path $DistDir | Out-Null
$binTmp = Join-Path $DistDir "ui3344-linux-amd64.bin"
Push-Location $RepoRoot
& $GoExe build -ldflags $ldflags -o $binTmp .
$rc = $LASTEXITCODE
Pop-Location
if ($rc -ne 0 -or -not (Test-Path $binTmp)) { Die "go build failed (rc=$rc)" }

$magic = ([IO.File]::ReadAllBytes($binTmp)[0..3] | ForEach-Object { $_.ToString("x2") }) -join " "
if ($magic -ne "7f 45 4c 46") { Die "output is not an ELF binary (magic: $magic) -- check CGO/CC settings" }
Ok ("ELF ok, " + [math]::Round((Get-Item $binTmp).Length / 1MB, 1) + " MB")

# ============================================================== 5. package
Step "5) Assemble release package"
$pkg = Join-Path $DistDir "ui3344"
if (Test-Path $pkg) { Remove-Item $pkg -Recurse -Force }
New-Item -ItemType Directory -Force -Path (Join-Path $pkg "bin") | Out-Null
Move-Item -Force $binTmp (Join-Path $pkg "ui3344")

foreach ($f in @("ui3344.sh", "ui3344.service.debian", "ui3344.service.arch", "ui3344.service.rhel", "ui3344.rc")) {
  Copy-Item (Join-Path $RepoRoot $f) $pkg -Force -ErrorAction SilentlyContinue
}
foreach ($f in @("install.sh", "README.md", "create-inbounds.py", "configure-subscription.py", "domain-setup.py")) {
  $src = Join-Path $RepoRoot "packaging\$f"
  if (Test-Path $src) { Copy-Item $src $pkg -Force }
}

# default xray core + geo data, taken from the bundled core zip
$coreZip = Join-Path $RepoRoot "internal\web\service\cores\Xray-linux-64-v26.9.9.zip"
if (Test-Path $coreZip) {
  $tmpCore = Join-Path $env:TEMP ("ui3344-core-" + [guid]::NewGuid().ToString("N"))
  New-Item -ItemType Directory -Force -Path $tmpCore | Out-Null
  & tar.exe -xf $coreZip -C $tmpCore
  $xray = Get-ChildItem $tmpCore -Recurse -Filter "xray" -File | Select-Object -First 1
  if ($xray) { Copy-Item $xray.FullName (Join-Path $pkg "bin\xray-linux-amd64") -Force }
  foreach ($g in @("geoip.dat", "geosite.dat")) {
    $gf = Get-ChildItem $tmpCore -Recurse -Filter $g -File | Select-Object -First 1
    if ($gf) { Copy-Item $gf.FullName (Join-Path $pkg "bin\$g") -Force }
  }
  Remove-Item $tmpCore -Recurse -Force -ErrorAction SilentlyContinue
  Ok "bundled xray core + geo data"
} else {
  Warn "core zip not found; package will have no default xray binary"
}

$tarName = "ui3344-linux-amd64.tar.gz"
$tarPath = Join-Path $DistDir $tarName
if (Test-Path $tarPath) { Remove-Item $tarPath -Force }

# Windows' tar.exe cannot store Unix modes (every entry lands as rw-rw-rw-) and
# has no --mode flag, so the package would extract without the executable bit and
# `./ui3344` would fail with "Permission denied". Pack it ourselves instead, so a
# Windows-built artifact carries the same modes as a Linux-built one.
Write-Host "  > tarpack (writes 0755 for the binary and scripts)"
Push-Location $RepoRoot
$env:GOOS = "windows"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
& $GoExe run ./tools/tarpack $pkg $tarPath
$tarRc = $LASTEXITCODE
Pop-Location
if ($tarRc -ne 0 -or -not (Test-Path $tarPath)) { Die "tarpack failed (rc=$tarRc)" }

$hash = (Get-FileHash $tarPath -Algorithm SHA256).Hash.ToLower()
"$hash  $tarName" | Set-Content (Join-Path $DistDir "$tarName.sha256") -Encoding ASCII
Ok "$tarName  $([math]::Round((Get-Item $tarPath).Length / 1MB, 1)) MB"
Ok "sha256 $hash"

Step "Done -- ship it to the verification server"
Write-Host @"
  scp -o StrictHostKeyChecking=no -o PreferredAuthentications=password ``
      -o PubkeyAuthentication=no -o NumberOfPasswordPrompts=1 ``
      dist\$tarName dist\$tarName.sha256 root@<server>:/root/

  then on the server:
      cd /root && sha256sum -c $tarName.sha256
      tar xzf $tarName && cd ui3344 && bash install.sh
"@
