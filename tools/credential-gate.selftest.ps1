#requires -Version 5.1
# 自测 tools/credential-gate.ps1：合成"干净"与"含凭据"的文件，验证闸门该放行时放行、该拦时拦。
# 这是验证闸门本身的行为，不是验证构建产物；产物验证见 dist 上的真实扫描与 Linux 侧的 strings 检查。
#
# 2026-10-05 重写：上一版自测调用的是闸门**早已删除**的旧接口
# （Select-ValueNeedles / -BinaryNeedles / -TextNeedles / Test-GateTextArtifact），
# 一跑就报"术语 Select-ValueNeedles 不会被识别"——自测坏了等于没有自测。
# 现在只调用 credential-gate.ps1 里真实存在的函数与参数，并额外覆盖
# 「裸前缀只在编译产物里致命」这条设计约束。
$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "credential-gate.ps1")

# 环境变量会影响 Get-CredentialPatterns（它把 env 也当凭据来源），
# 为了让"公开构建"这一组用例可复现，这里显式清掉。
$env:UI3344_CF_TOKEN = $null
$env:UI3344_OTP_SECRET = $null

$tmp = Join-Path $env:TEMP ("gate-test-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

# 真实形态：cfat_ + 48 位 base62（这里用固定字符串，不是真凭据）
$fakeToken = "cfat_" + ("A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8S9t0U1v2W3x4" )
$fakeOtp   = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"

$fail = 0
function Expect-Throw($name, [scriptblock]$body) {
  try { & $body; Write-Host "  [FAIL] $name -- 应当抛错但没有" -ForegroundColor Red; $script:fail++ }
  catch { Write-Host ("  [PASS] $name -- 已拦截：" + $_.Exception.Message) -ForegroundColor Green }
}
function Expect-Pass($name, [scriptblock]$body) {
  try { & $body; Write-Host "  [PASS] $name" -ForegroundColor Green }
  catch { Write-Host ("  [FAIL] $name -- 不应拦截：" + $_.Exception.Message) -ForegroundColor Red; $script:fail++ }
}

try {
  $patterns = Get-CredentialPatterns -Token $fakeToken -OtpSecret $fakeOtp
  $needles = Build-CredentialNeedles -Patterns $patterns
  $shapes = Get-TokenShapePatterns
  Write-Host ("value needles : " + (($needles | ForEach-Object { $_.Name }) -join " | "))
  Write-Host ("shape patterns: " + (($shapes | ForEach-Object { $_.Name }) -join " | "))
  Write-Host ""

  if ($needles.Count -lt 2) { Write-Host "  [FAIL] 凭据 needle 数量异常" -ForegroundColor Red; $fail++ }

  # --- 1. 随机二进制、无凭据 -------------------------------------------------
  $clean = Join-Path $tmp "clean.bin"
  $bytes = New-Object System.Collections.Generic.List[byte]
  $rnd = New-Object System.Random(1234)
  for ($i = 0; $i -lt 200000; $i++) { $bytes.Add([byte]$rnd.Next(0, 256)) }
  $bytes.AddRange([Text.Encoding]::ASCII.GetBytes("v2.2 normal build, no credentials here"))
  [IO.File]::WriteAllBytes($clean, $bytes.ToArray())

  Write-Host "case 1: clean binary"
  Expect-Pass "clean binary passes MustBeClean" {
    [void](Invoke-CredentialGate -Paths @($clean) -ValueNeedles $needles `
      -ShapePatterns $shapes -MustBeClean)
  }

  # --- 2. 二进制里嵌了 ASCII 凭据（含裸前缀，编译产物该致命）------------------
  $dirtyBin = Join-Path $tmp "dirty.bin"
  [IO.File]::WriteAllBytes($dirtyBin, [Text.Encoding]::ASCII.GetBytes("junk " + $fakeToken + " junk " + $fakeOtp))
  Write-Host "case 2: binary with ASCII credentials"
  Expect-Throw "binary with credentials is rejected" {
    [void](Invoke-CredentialGate -Paths @($dirtyBin) -ValueNeedles $needles `
      -ShapePatterns $shapes -BarePrefixFatal -MustBeClean -Label "test binary")
  }
  $n = Invoke-CredentialGate -Paths @($dirtyBin) -ValueNeedles $needles -ShapePatterns $shapes
  if ($n -lt 3) { Write-Host "  [FAIL] advisory count = $n, 期望 >= 3" -ForegroundColor Red; $fail++ }
  else { Write-Host "  [PASS] advisory count = $n" -ForegroundColor Green }

  # --- 3. 只有 UTF-16LE 形态（strings 会漏，字节扫描必须抓到） ----------------
  $dirty16 = Join-Path $tmp "dirty-utf16.bin"
  [IO.File]::WriteAllBytes($dirty16, [Text.Encoding]::Unicode.GetBytes("prefix " + $fakeOtp + " suffix"))
  Write-Host "case 3: UTF-16LE only"
  Expect-Throw "UTF-16LE credential is rejected" {
    [void](Invoke-CredentialGate -Paths @($dirty16) -ValueNeedles $needles -MustBeClean)
  }

  # --- 4. 文本文件里的"文档性" cfat_ 必须放行 ---------------------------------
  # 这正是第一版闸门误伤自己的地方：install.sh / README.md / set-credentials.py
  # 里必须能写出 `strings ui3344 | grep -c 'cfat_'` 和 `<cfat_...>`。
  $doc = Join-Path $tmp "doc.md"
  @'
复核：strings ui3344 | grep -c 'cfat_'   必须为 0
用法：UI3344_CF_TOKEN='cfat_...' bash install.sh
正则：cfat_[A-Za-z0-9]{40,60}
历史：cfat_hf1N...f6a2
前缀常量：CF_TOKEN_PREFIX = "cfat_"
'@ | Set-Content $doc -Encoding UTF8
  Write-Host "case 4: text file documenting the cfat_ prefix"
  Expect-Pass "documentation mentioning cfat_ passes (package scope)" {
    [void](Invoke-CredentialGate -Paths @($doc) -ValueNeedles $needles `
      -ShapePatterns $shapes -ReportBarePrefix -MustBeClean)
  }

  # --- 5. 文本文件里粘了真 Token 必须拦下 -------------------------------------
  $leakPy = Join-Path $tmp "leak.py"
  "TOKEN = `"$fakeToken`"" | Set-Content $leakPy -Encoding UTF8
  Write-Host "case 5: text file with a real pasted token"
  Expect-Throw "pasted token in a script is rejected" {
    [void](Invoke-CredentialGate -Paths @($leakPy) -ValueNeedles $needles `
      -ShapePatterns $shapes -MustBeClean)
  }

  # --- 6. 文本文件里的 TOTP 密钥（精确值命中） --------------------------------
  $leakSh = Join-Path $tmp "leak.sh"
  "export UI3344_OTP_SECRET=$fakeOtp" | Set-Content $leakSh -Encoding UTF8
  Write-Host "case 6: text file with the TOTP secret"
  Expect-Throw "TOTP secret in a script is rejected" {
    [void](Invoke-CredentialGate -Paths @($leakSh) -ValueNeedles $needles `
      -ShapePatterns $shapes -MustBeClean)
  }

  # --- 7. 公开构建（无凭据参数）：只剩形态规则 ---------------------------------
  Write-Host "case 7: public build with no credential values"
  $pinnedOnly = Build-CredentialNeedles -Patterns (Get-CredentialPatterns)
  $n7 = Invoke-CredentialGate -Paths @($dirtyBin) -ValueNeedles $pinnedOnly -ShapePatterns $shapes
  if ($n7 -lt 1) { Write-Host "  [FAIL] 形态规则未命中旧版泄露形态" -ForegroundColor Red; $fail++ }
  else { Write-Host "  [PASS] 形态规则命中 $n7 处（仅凭 token 形态即可发现旧版泄露）" -ForegroundColor Green }
  Expect-Pass "public build does not flag the documentation" {
    [void](Invoke-CredentialGate -Paths @($doc) -ValueNeedles $pinnedOnly `
      -ShapePatterns $shapes -ReportBarePrefix -MustBeClean)
  }

  # --- 8. 裸前缀的两种身份：包里只是信息，编译产物里才是致命 ------------------
  Write-Host "case 8: bare prefix is informational in a package, fatal in a binary"
  $bareOnly = Join-Path $tmp "bare.bin"
  [IO.File]::WriteAllBytes($bareOnly, [Text.Encoding]::ASCII.GetBytes("prefix cfat_ and nothing after it"))
  $n8 = Invoke-CredentialGate -Paths @($bareOnly) -ValueNeedles $needles `
    -ShapePatterns $shapes -ReportBarePrefix
  if ($n8 -ne 0) { Write-Host "  [FAIL] 包级扫描里裸前缀不该致命（返回 $n8）" -ForegroundColor Red; $fail++ }
  else { Write-Host "  [PASS] 包级扫描放行裸前缀（仅打印信息）" -ForegroundColor Green }
  Expect-Throw "bare prefix in a compiled binary is fatal" {
    [void](Invoke-CredentialGate -Paths @($bareOnly) -ValueNeedles $needles `
      -ShapePatterns $shapes -BarePrefixFatal -MustBeClean -Label "test binary")
  }

  Write-Host ""
  if ($fail -eq 0) { Write-Host "ALL GATE SELF-TESTS PASSED" -ForegroundColor Green }
  else { Write-Host "$fail SELF-TEST(S) FAILED" -ForegroundColor Red; exit 1 }
} finally {
  Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}