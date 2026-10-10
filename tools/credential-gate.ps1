#requires -Version 5.1
<#
  credential-gate.ps1 -- refuse to ship credentials inside a build artifact.

  A credential inside a published artifact IS a leak: -ldflags values land in
  .rodata, and anyone can read them back with `strings`. That is exactly how the
  Cloudflare API token of this project ended up in public releases v1.7..v2.1.
  This file is the check that makes that mistake impossible to repeat silently.

  ===================================================================
  HOW TO CALL THIS -- read this first, it is a dot-sourced FUNCTION LIBRARY
  ===================================================================
  This file is NOT a standalone script. It defines functions and runs nothing.
  Invoking it directly (`pwsh -File tools\credential-gate.ps1 -Path ...`) does
  NOTHING and exits 0 -- which looks exactly like "the gate passed". Dot-source
  it and call the functions:

      . .\tools\credential-gate.ps1
      $needles = Build-CredentialNeedles -Patterns (Get-CredentialPatterns -Token $t -OtpSecret $o)
      $shapes  = Get-TokenShapePatterns
      Invoke-CredentialGate -Paths @($exe) -ValueNeedles $needles -ShapePatterns $shapes `
                            -MustBeClean -BarePrefixFatal

  Invoke-CredentialGate prints one line per scanned file and returns the total hit
  count. With -MustBeClean a non-zero total THROWS, so the caller aborts the
  build -- the gate is a hard stop, never a warning.

  Self-test (proves both directions: clean passes, dirty throws):
      pwsh -File tools\credential-gate.selftest.ps1

  ===================================================================
  WHAT IS FATAL, AND WHY THE NEEDLE IS NOT THE BARE PREFIX
  ===================================================================
  The first version of this gate searched for the bare 5-byte prefix `cfat_` in
  every packaged file. Running it on a real package failed the build on SEVEN
  hits -- every one of them documentation, not a credential:

      install.sh       UI3344_CF_TOKEN=<cfat_...> bash install.sh
      README.md        UI3344_CF_TOKEN='cfat_...' ... / grep cfat_
      set-credentials.py   cfat_[A-Za-z0-9]{40,60} / "cfat_ 开头" / CF_TOKEN_PREFIX = "cfat_"

  A real scoped Cloudflare token is 53 chars (`cfat_` + 48 base62). A gate that
  fails on its own documentation blocks every release, and the first person who
  needs to ship will weaken it or add -SkipGate -- which is WORSE than no gate,
  because it also destroys trust in the check. So the fatal rule is the token
  SHAPE, not the prefix:

      cfat_  followed by 16+ of [A-Za-z0-9_-]      (real token: 48)
      ghp_   followed by 16+ of [A-Za-z0-9_-]      (GitHub PAT, same failure mode)

  Detection power is unchanged: any real token matches the shape. Placeholders
  (`cfat_...`, `<cfat_...>`, a `cfat_` string constant, a grep command) do not.
  A 21-byte needle also cannot collide by chance in a 200 MB binary, which the
  5-byte prefix could (that is why 1-7 byte value needles are refused below).

  Deliberately NOT done: matching a generic base32 alphabet
  (`ABCDEFGHIJKLMNOPQRSTUVWXYZ234567`) as a "TOTP secret" needle. That alphabet
  appears as a constant in legitimate code, so it would be another false-positive
  machine. TOTP secrets and panel passwords are only ever matched as the CONCRETE
  values handed to the build (-OtpSecret / $UI3344_OTP_SECRET).

  The one place the bare prefix stays fatal is the COMPILED GO BINARY
  (-BarePrefixFatal): no legitimate build puts `cfat_` into a compiled binary, so
  there it is a real signal, and it keeps parity with the documented command
  `strings ui3344 | grep -c 'cfat_'` == 0. In the package-level scan it is
  reported as information only, because the install script and the docs must be
  able to print that very command.

  Also note: for a public build no credential value is supplied at all, so the
  shape patterns are the whole defence. An unknown TOTP secret embedded by other
  means cannot be detected generically -- that is a documented limitation, not a
  silent one.

  NOTE: kept ASCII-only on purpose, same reason as build-local.ps1 (Windows
  PowerShell 5.1 parses .ps1 files as ANSI unless they carry a UTF-8 BOM).
#>

# Latin-1 maps every byte 1:1 onto a char, so a raw byte scan becomes a plain
# .NET String.IndexOf -- fast enough to scan a 200 MB Go binary in ~1s.
$script:GateLatin1 = [Text.Encoding]::GetEncoding(28591)

# Character class shared by every shape pattern: [A-Za-z0-9_-]
$script:GateShapeClass = New-Object 'bool[]' 128
foreach ($ch in ([char[]]"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-")) {
  $script:GateShapeClass[[int]$ch] = $true
}

function Get-CredentialPatterns {
  <#
    .SYNOPSIS
      The CONCRETE credential values handed to this build (exact byte needles).
    .DESCRIPTION
      No generic prefix patterns here -- see the header. Values shorter than 8
      bytes are refused loudly: they would match random bytes in a large binary.
  #>
  param(
    [string]$Token = "",
    [string]$OtpSecret = ""
  )

  $patterns = New-Object System.Collections.ArrayList
  $candidates = @(
    @{ Name = "-Token (CF API token)";           Value = $Token },
    @{ Name = "-OtpSecret (domain TOTP secret)"; Value = $OtpSecret },
    @{ Name = "env UI3344_CF_TOKEN";             Value = $env:UI3344_CF_TOKEN },
    @{ Name = "env UI3344_OTP_SECRET";           Value = $env:UI3344_OTP_SECRET }
  )
  foreach ($c in $candidates) {
    $v = ""
    if ($c.Value) { $v = ([string]$c.Value).Trim() }
    if ($v.Length -eq 0) { continue }
    if ($v.Length -lt 8) {
      Write-Host ("  [warn] gate value needle ignored (shorter than 8 bytes): " + $c.Name) -ForegroundColor Yellow
      continue
    }
    [void]$patterns.Add(@{ Name = $c.Name; Value = $v; Pinned = $false })
    $squashed = ($v -replace '\s', '')
    if ($squashed -ne $v -and $squashed.Length -ge 8) {
      [void]$patterns.Add(@{ Name = $c.Name + " (spaces stripped)"; Value = $squashed; Pinned = $false })
    }
  }
  return $patterns
}

function Get-TokenShapePatterns {
  <#
    .SYNOPSIS
      Credential SHAPES: <prefix> + 16 or more of [A-Za-z0-9_-].
    .DESCRIPTION
      Real values are far longer than 16 (a scoped Cloudflare token is 48 after
      the prefix), so detection power is unchanged, while documentation
      placeholders can never match.
  #>
  return @(
    @{ Name = "Cloudflare token shape cfat_<16+ base62>"; Prefix = "cfat_"; MinLength = 16 }
    @{ Name = "GitHub PAT shape ghp_<16+ base62>";        Prefix = "ghp_";  MinLength = 16 }
  )
}

# The bare prefix, kept only for the informational / binary-only checks above.
$script:GateBareTokenPrefix = "cfat_"

function Build-CredentialNeedles {
  <#
    .SYNOPSIS
      Pre-compute the UTF-16LE form of every value needle once, so a multi-file
      scan does not rebuild it per file.
  #>
  param([object[]]$Patterns)

  $needles = New-Object System.Collections.ArrayList
  foreach ($p in $Patterns) {
    [void]$needles.Add(@{
      Name  = $p.Name
      Value = $p.Value
      Utf16 = $script:GateLatin1.GetString([Text.Encoding]::Unicode.GetBytes($p.Value))
    })
  }
  return $needles
}

function Count-GateOccurrences {
  param([string]$Haystack, [string]$Needle)
  $count = 0
  $idx = 0
  while ($true) {
    $found = $Haystack.IndexOf($Needle, $idx, [StringComparison]::Ordinal)
    if ($found -lt 0) { break }
    $count++
    $idx = $found + 1
    if ($count -ge 1000) { break }
  }
  return $count
}

function Count-GateShapes {
  <#
    .SYNOPSIS
      Count <prefix><16+ class chars> occurrences without running the regex
      engine over a 200 MB string: IndexOf the prefix, then walk the class.
    .DESCRIPTION
      Equivalent to the regex `<prefix>[A-Za-z0-9_-]{16,}` (existence of at least
      one 16-char run is all that is needed, so a linear walk is exact).
      ASCII only, on purpose: Go stores string literals as UTF-8, and the exact
      value needles already cover the UTF-16LE form of known secrets.
  #>
  param([string]$Haystack, [object[]]$ShapePatterns)

  $hits = New-Object System.Collections.ArrayList
  foreach ($s in $ShapePatterns) {
    $prefix = $s.Prefix
    $minLen = [int]$s.MinLength
    $count = 0
    $idx = 0
    $limit = $Haystack.Length
    while ($true) {
      $found = $Haystack.IndexOf($prefix, $idx, [StringComparison]::Ordinal)
      if ($found -lt 0) { break }
      $p = $found + $prefix.Length
      $run = 0
      while (($p + $run) -lt $limit -and $run -lt $minLen) {
        $c = [int]$Haystack[$p + $run]
        if ($c -ge 128 -or -not $script:GateShapeClass[$c]) { break }
        $run++
      }
      if ($run -ge $minLen) { $count++ }
      $idx = $found + 1
      if ($count -ge 1000) { break }
    }
    if ($count -gt 0) { [void]$hits.Add(@{ Name = $s.Name; Count = $count }) }
  }
  return $hits
}

function Invoke-CredentialGate {
  <#
    .SYNOPSIS
      Scan files, print a report, return the total fatal hit count.
    .PARAMETER ValueNeedles
      Exact credential values (ASCII + UTF-16LE). Fatal on a hit, in any file.
    .PARAMETER ShapePatterns
      Token shapes (see Get-TokenShapePatterns). Fatal on a hit, in any file.
    .PARAMETER BarePrefixFatal
      Also treat the bare `cfat_` prefix as fatal. Use it for the compiled Go
      binary only; in a package full of docs/scripts it would fail on the
      documentation itself (which is the bug this parameter exists to contain).
    .PARAMETER ReportBarePrefix
      Print, but do not fail on, the bare prefix count. Makes the documented
      `strings <bin> | grep -c 'cfat_'` number visible in every run.
    .PARAMETER MustBeClean
      Throw when the fatal total is non-zero, so a release build stops instead of
      packaging a leak.
  #>
  param(
    [string[]]$Paths,
    [object[]]$ValueNeedles = @(),
    [object[]]$ShapePatterns = @(),
    [switch]$BarePrefixFatal,
    [switch]$ReportBarePrefix,
    [switch]$MustBeClean,
    [string]$Label = ""
  )

  $total = 0
  foreach ($p in $Paths) {
    if (-not (Test-Path -LiteralPath $p -PathType Leaf)) { continue }
    $len = (Get-Item -LiteralPath $p).Length
    $bytes = [IO.File]::ReadAllBytes($p)
    $hay = $script:GateLatin1.GetString($bytes)
    $bytes = $null

    $fatal = New-Object System.Collections.ArrayList
    foreach ($n in $ValueNeedles) {
      $c = Count-GateOccurrences -Haystack $hay -Needle $n.Value
      if ($n.Utf16) { $c += Count-GateOccurrences -Haystack $hay -Needle $n.Utf16 }
      if ($c -gt 0) { [void]$fatal.Add(@{ Name = $n.Name; Count = $c }) }
    }
    foreach ($h in (Count-GateShapes -Haystack $hay -ShapePatterns $ShapePatterns)) { [void]$fatal.Add($h) }

    $bare = Count-GateOccurrences -Haystack $hay -Needle $script:GateBareTokenPrefix
    if ($BarePrefixFatal -and $bare -gt 0) {
      [void]$fatal.Add(@{ Name = "bare Cloudflare token prefix 'cfat_' (compiled binary)"; Count = $bare })
    }
    $hay = $null

    $sum = 0
    foreach ($h in $fatal) { $sum += $h.Count }
    $total += $sum
    if ($sum -eq 0) {
      Write-Host ("  [ok]   scan {0} ({1:N1} MB): 0 hits" -f $p, ($len / 1MB))
    } else {
      Write-Host ("  [BAD]  scan {0} ({1:N1} MB): {2} hit(s)" -f $p, ($len / 1MB), $sum) -ForegroundColor Red
      foreach ($h in $fatal) {
        Write-Host ("           {0} x {1}" -f $h.Count, $h.Name) -ForegroundColor Red
      }
    }
    if ($ReportBarePrefix) {
      $note = "documentation placeholder, not fatal"
      if ($BarePrefixFatal) { $note = "fatal for compiled binaries" }
      Write-Host ("         info: bare 'cfat_' occurrences = {0} ({1})" -f $bare, $note)
    }
  }

  if ($MustBeClean -and $total -gt 0) {
    $what = "artifact"
    if ($Label) { $what = $Label }
    $msg = ("credential gate FAILED: {0} hit(s) in {1}. A published artifact carrying " +
            "credentials hands them to everyone who downloads it. Nothing was packaged. " +
            "If a hit is documentation rather than a credential, fix the text (write " +
            "'cfat_...' or 'Cloudflare Token') -- do NOT weaken this check.") -f $total, $what
    throw $msg
  }
  return $total
}
