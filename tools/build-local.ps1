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
    pwsh -File tools\build-local.ps1 -Public -Tag v2.2   # RELEASE build: no credentials, hard-gated
    pwsh -File tools\build-local.ps1 -Tag v2.2           # no creds given -> same as -Public
    pwsh -File tools\build-local.ps1 -Tag v2.2 -NoFrontend
                                                         # Go-only change, faster
    pwsh -File tools\build-local.ps1 -Tag v2.2 -Token <cfat_...> -OtpSecret <base32>
                                                         # PRIVATE build: embeds credentials.
                                                         # MUST NOT BE PUBLISHED. See below.

  CREDENTIAL POLICY -- read before you even think about adding -Token back:
    Release artifacts are distributed publicly. Whatever -ldflags injects lands in
    .rodata and is recoverable with `strings`. A published binary carrying the
    Cloudflare API token means every person who downloads it can take over the
    domain's DNS. That is not hypothetical: it already happened once
    (see the repo-root security audit record, and the v1.7..v2.1 releases).

    So: public/release builds carry NO credentials and are HARD-GATED below --
    if the scan finds one, the build fails and no tarball is produced. Credentials
    are injected at INSTALL time instead, by packaging/install.sh, via
    UI3344_CF_TOKEN / UI3344_OTP_SECRET -> settings table.
    Full rationale + commands: the repo-root "credentials and release flow" doc
    (newest *.md in the repo root; this file is ASCII-only so it cannot spell the
    Chinese file name -- search for that phrase, the doc names itself).

  NOTE: kept ASCII-only on purpose. Windows PowerShell 5.1 parses .ps1 files as
  ANSI unless they carry a UTF-8 BOM, so non-ASCII text here breaks the parser.
#>
[CmdletBinding()]
param(
  [string]$Tag = "v2.2",
  [string]$Token = "",
  [string]$OtpSecret = "",
  # -Release is an alias: both mean "this artifact goes public".
  [Alias("Release")]
  [switch]$Public,
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
$LdPkg    = "github.com/mhsanaei/3x-ui/v3/internal/web/service"
$PkgName  = "ui3344"

function Step($msg) { Write-Host ""; Write-Host ("=" * 70); Write-Host "  $msg"; Write-Host ("=" * 70) }
function Ok($msg)   { Write-Host "  [ok]   $msg" }
function Warn($msg) { Write-Host "  [warn] $msg" -ForegroundColor Yellow }
function Bad($msg)  { Write-Host "  [BAD]  $msg" -ForegroundColor Red }
function Die($msg)  { Write-Host "  [fail] $msg" -ForegroundColor Red; exit 1 }

# ========================================================== credential scanning
# The scanner lives in its own file so it can be unit-tested and reused. It is
# strictly stronger than `strings <bin> | grep -c` (raw byte scan, ASCII + UTF-16,
# no printable/run-length requirement) which matters because a Windows-built
# release never has binutils strings.exe available.
. (Join-Path $PSScriptRoot "credential-gate.ps1")


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

# ====================================================== 0. credential policy
# Decided BEFORE anything is downloaded or compiled, so a contradictory
# invocation fails in one second instead of after a 10 minute build.
Step "0) Credential policy"

$IsPrivateBuild = $false
$CredentialPatterns = @()

if ($Public) {
  if ($Token -or $OtpSecret) {
    Die ("-Public (release) refuses -Token / -OtpSecret. A published artifact that " +
         "carries credentials hands the Cloudflare token to everyone who downloads it. " +
         "Drop the credentials (they go in via install.sh), or drop -Public if this " +
         "really is a private build you will never publish.")
  }
  Ok "release build: credentials will NOT be injected"
} elseif ($Token -or $OtpSecret) {
  $IsPrivateBuild = $true
  Write-Host ""
  Write-Host ("!" * 74) -ForegroundColor Red
  Write-Host "!!  PRIVATE BUILD -- CREDENTIALS WILL BE EMBEDDED IN THE BINARY" -ForegroundColor Red
  Write-Host "!!" -ForegroundColor Red
  Write-Host "!!  -ldflags values land in .rodata and anyone can read them back with:" -ForegroundColor Red
  Write-Host "!!      strings ui3344 | grep -o 'cfat_[A-Za-z0-9]*'" -ForegroundColor Red
  Write-Host "!!" -ForegroundColor Red
  Write-Host "!!  THIS ARTIFACT MUST NEVER BE PUBLISHED, UPLOADED OR SHARED." -ForegroundColor Red
  Write-Host "!!  Whoever gets the file gets the Cloudflare API token, and with it" -ForegroundColor Red
  Write-Host "!!  control over the domain's DNS records. This already happened once." -ForegroundColor Red
  Write-Host "!!" -ForegroundColor Red
  Write-Host "!!  For anything public use:  -Public   (credentials via install.sh)" -ForegroundColor Red
  Write-Host ("!" * 74) -ForegroundColor Red
  Write-Host ""
} else {
  Ok "no credentials given: credential-free build (identical policy to -Public)"
}

$CredentialPatterns = Get-CredentialPatterns -Token $Token -OtpSecret $OtpSecret
$CredentialNeedles = Build-CredentialNeedles -Patterns $CredentialPatterns
# Text files (install.sh, *.py, *.md) legitimately spell `cfat_` in help text and
# in the documented `strings | grep -c 'cfat_'` command, so for them the fatal
# rules are the concrete VALUES plus the token SHAPE -- never the bare prefix.
# The bare prefix is only fatal inside the compiled Go binary (-BarePrefixFatal).
# The shape/prefix logic lives in credential-gate.ps1; do not reintroduce a
# separate "text needle" selector here (an older build called Select-ValueNeedles
# / -BinaryNeedles / -TextNeedles, none of which exist any more -- that mismatch
# made the gate abort the build with a parameter-binding error).
$CredentialShapePatterns = Get-TokenShapePatterns
Ok ("value needles armed: " + (($CredentialPatterns | ForEach-Object { $_.Name }) -join " | "))
Ok ("shape patterns armed: " + (($CredentialShapePatterns | ForEach-Object { $_.Name }) -join " | "))

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

# Credentials are injected ONLY on an explicit private build. A public/release
# build never puts them on the command line at all -- not even as an empty -X,
# so there is no string to leak and nothing to forget to remove.
$ldflags = "-X github.com/mhsanaei/3x-ui/v3/internal/config.uiVersion=$Tag -X github.com/mhsanaei/3x-ui/v3/internal/config.version=3.7.0"
if ($IsPrivateBuild) {
  if ($Token)     { $ldflags += " -X $LdPkg.defaultCFApiToken=$Token" }
  if ($OtpSecret) { $ldflags += " -X $LdPkg.defaultDomainOtpSecret=$OtpSecret" }
  Warn "PRIVATE build: credentials are on the linker command line and will end up in .rodata."
} else {
  Ok "release-safe: no -X defaultCFApiToken / -X defaultDomainOtpSecret on the linker command line"
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

# --- GATE #1: the binary itself, before it is packaged -----------------------
# Hard stop on any hit for a release-safe build. This is the check that would
# have caught the v1.7..v2.1 leak, and it is not optional and not a warning.
Step "4b) Credential gate -- compiled binary"
try {
  $binHits = Invoke-CredentialGate -Paths @($binTmp) -ValueNeedles $CredentialNeedles `
    -ShapePatterns $CredentialShapePatterns -BarePrefixFatal -ReportBarePrefix `
    -MustBeClean:(-not $IsPrivateBuild) -Label "the compiled binary"
} catch {
  Die $_.Exception.Message
}

# ============================================================== 5. package
Step "5) Assemble release package"
$pkg = Join-Path $DistDir "ui3344"
if (Test-Path $pkg) { Remove-Item $pkg -Recurse -Force }
New-Item -ItemType Directory -Force -Path (Join-Path $pkg "bin") | Out-Null
Move-Item -Force $binTmp (Join-Path $pkg "ui3344")

foreach ($f in @("ui3344.sh", "ui3344.service.debian", "ui3344.service.arch", "ui3344.service.rhel", "ui3344.rc")) {
  Copy-Item (Join-Path $RepoRoot $f) $pkg -Force -ErrorAction SilentlyContinue
}
foreach ($f in @("install.sh", "README.md", "create-inbounds.py", "configure-subscription.py", "domain-setup.py", "set-credentials.py")) {
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

# A private build gets a loud sentinel next to install.sh and a file name that
# cannot be mistaken for a release, so an accidental upload is caught by whoever
# picks the file -- not only by whoever remembers this script exists.
if ($IsPrivateBuild) {
  $sentinel = Join-Path $pkg "DO-NOT-DISTRIBUTE.txt"
  @"
!! DO NOT DISTRIBUTE -- PRIVATE BUILD WITH EMBEDDED CREDENTIALS !!

This package was built with -Token / -OtpSecret, so the ui3344 binary contains a
Cloudflare API Token and/or a TOTP secret in .rodata. Anyone who gets this file
can extract them:

    strings ui3344 | grep -o 'cfat_[A-Za-z0-9]*'

With that token a third party can rewrite the domain's DNS records. Publishing or
sharing this artifact is a credential leak.

Rebuild without credentials for anything public:

    pwsh -File tools\build-local.ps1 -Public -Tag <version>

and inject the credentials at install time instead:

    UI3344_CF_TOKEN=... UI3344_OTP_SECRET=... bash install.sh

See the repo-root "credentials and release flow" doc.
"@ | Set-Content $sentinel -Encoding UTF8
  Ok "private-build sentinel written: DO-NOT-DISTRIBUTE.txt"
}

# --- GATE #2: the whole assembled tree, right before it is tarred -------------
# Covers the binary again plus every script that ships in the package.
Step "5b) Credential gate -- assembled package (pre-packaging)"
$pkgFiles = @(Get-ChildItem $pkg -Recurse -File | Select-Object -ExpandProperty FullName)
try {
  $pkgHits = Invoke-CredentialGate -Paths $pkgFiles -BinaryNeedles $CredentialNeedles `
    -TextNeedles $CredentialValueNeedles -ShapePatterns $CredentialShapePatterns `
    -MustBeClean:(-not $IsPrivateBuild) -Label "the assembled package"
} catch {
  Die $_.Exception.Message
}

if ($IsPrivateBuild) {
  $tarName = "ui3344-linux-amd64-PRIVATE-DO-NOT-DISTRIBUTE.tar.gz"
} else {
  $tarName = "ui3344-linux-amd64.tar.gz"
}
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

Step "Done"
if ($IsPrivateBuild) {
  Write-Host ("!" * 74) -ForegroundColor Red
  Write-Host "  PRIVATE ARTIFACT -- DO NOT UPLOAD IT ANYWHERE." -ForegroundColor Red
  Write-Host ("  credential hits baked into this package: " + $pkgHits) -ForegroundColor Red
  Write-Host ("!" * 74) -ForegroundColor Red
} else {
  Write-Host "  Release-safe artifact: the credential gate found 0 hits in every packaged file."
  Write-Host "  (binary: $binHits hit(s), package: $pkgHits hit(s))"
  Write-Host ""
  Write-Host "  On a Linux host you can reproduce the gate with the literal command:"
  Write-Host "      tar xzf dist\$tarName && strings ui3344/ui3344 | grep -c 'cfat_'   # must print 0"
}
Write-Host ""
Write-Host @"
  scp -o StrictHostKeyChecking=no -o PreferredAuthentications=password ``
      -o PubkeyAuthentication=no -o NumberOfPasswordPrompts=1 ``
      dist\$tarName dist\$tarName.sha256 root@<server>:/root/

  then on the server:
      cd /root && sha256sum -c $tarName.sha256
      tar xzf $tarName && cd ui3344 && bash install.sh

  credentials are injected at INSTALL time, never at build time:
      UI3344_CF_TOKEN=<cfat_...> UI3344_OTP_SECRET=<base32> bash install.sh
"@
