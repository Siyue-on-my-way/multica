# Multica fork CLI one-click installer (Windows / PowerShell).
# Served from this deployment's own /downloads/ channel; installs the fork
# build (commit b2ebe15ec) and points future updates at the fork release repo.
# Run:  irm http://8.148.26.166:2080/downloads/install.ps1 | iex
# (Regenerate via scripts/publish-cli-downloads.sh, which bakes http://8.148.26.166:2080/downloads.)

$ErrorActionPreference = 'Stop'

$Base       = 'http://8.148.26.166:2080/downloads'
$UpdateRepo = 'Siyue-on-my-way/multica'
$Asset      = 'multica-windows-amd64.exe'

$Tmp = Join-Path $env:TEMP ("multica-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $Tmp | Out-Null

Write-Host "1/5 downloading $Asset ..."
$exe = Join-Path $Tmp $Asset
Invoke-WebRequest -Uri "$Base/$Asset" -OutFile $exe -UseBasicParsing

Write-Host '2/5 verifying SHA-256 ...'
$sums = ((Invoke-WebRequest -Uri "$Base/checksums.txt" -UseBasicParsing).Content.Trim()) -split "`n"
$want = (($sums | Where-Object { $_ -match [regex]::Escape($Asset) })[0].Trim()) -split '\s+' | Select-Object -First 1
$have = (Get-FileHash $exe -Algorithm SHA256).Hash
if ($have -ne $want.ToUpper()) { throw "SHA-256 mismatch: got $have want $want" }
Write-Host "   OK ($have)"

Write-Host '3/5 stopping old daemon and replacing the binary ...'
$dst = $null
try { $dst = (Get-Command multica -ErrorAction Stop).Source } catch {}
if (-not $dst) {
    $dir = Join-Path $env:LOCALAPPDATA 'multica'
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
    $dst = Join-Path $dir 'multica.exe'
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($userPath -notlike "*$dir*") {
        [Environment]::SetEnvironmentVariable('Path', "$userPath;$dir", 'User')
    }
    Write-Host "   installed to $dst (added to user PATH)"
}
try { multica daemon stop 2>$null } catch {}
Start-Sleep 1
try {
    Copy-Item $exe $dst -Force
} catch {
    throw "无法写入 $dst —— 请关闭当前窗口，改用『以管理员身份运行』的 PowerShell 重新执行本命令。原因: $($_.Exception.Message)"
}

Write-Host '4/5 setting MULTICA_UPDATE_REPO (user scope) ...'
[Environment]::SetEnvironmentVariable('MULTICA_UPDATE_REPO', $UpdateRepo, 'User')
$env:MULTICA_UPDATE_REPO = $UpdateRepo

Write-Host '5/5 verifying and starting daemon ...'
& $dst version
& $dst daemon start

Remove-Item $Tmp -Recurse -Force -ErrorAction SilentlyContinue
Write-Host ''
Write-Host '✅ 完成。multica version 显示的 commit 必须与 manifest.json 中的 commit 一致（当前发布见 '$Base'/manifest.json）。'
