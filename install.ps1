# install.ps1 — One-line Windows PowerShell installer for a2dlc
# Usage:
#   irm https://raw.githubusercontent.com/falsable/a2dlc/main/install.ps1 | iex
$ErrorActionPreference = "Stop"

$Repo = "falsable/a2dlc"
$Version = if ($env:A2DLC_VERSION) { $env:A2DLC_VERSION } else { "v0.4.1" }
$InstallDir = if ($env:A2DLC_INSTALL_DIR) { $env:A2DLC_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "a2dlc\bin" }

$BinName = "a2dlc-windows-amd64.exe"
$BaseUrl = "https://github.com/$Repo/releases/download/$Version"
$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("a2dlc-install-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $TmpDir -Force | Out-Null

try {
    $BinPath = Join-Path $TmpDir $BinName
    $SumsPath = Join-Path $TmpDir "SHA256SUMS"

    Write-Host "Downloading $BinName ($Version) from $Repo..."
    Invoke-WebRequest -Uri "$BaseUrl/$BinName" -OutFile $BinPath -UseBasicParsing
    Invoke-WebRequest -Uri "$BaseUrl/SHA256SUMS" -OutFile $SumsPath -UseBasicParsing

    Write-Host "Verifying SHA-256 checksum..."
    $ExpectedLine = Get-Content $SumsPath | Where-Object { $_ -match "\s+$BinName$" } | Select-Object -First 1
    if (-not $ExpectedLine) {
        throw "Could not find $BinName in SHA256SUMS"
    }
    $ExpectedHash = ($ExpectedLine -split "\s+")[0].ToLowerInvariant()
    $ActualHash = (Get-FileHash -Path $BinPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($ExpectedHash -ne $ActualHash) {
        throw "SHA-256 mismatch! Expected $ExpectedHash, got $ActualHash"
    }

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    $DestExe = Join-Path $InstallDir "a2dlc.exe"
    Move-Item -Path $BinPath -Destination $DestExe -Force
    Write-Host "Installed a2dlc to $DestExe"

    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if (-not ($UserPath -split ";" | Where-Object { $_ -eq $InstallDir })) {
        [Environment]::SetEnvironmentVariable("Path", "$UserPath;$InstallDir", "User")
        $env:Path = "$env:Path;$InstallDir"
        Write-Host "Added $InstallDir to User PATH."
    }

    if (Test-Path ".git") {
        New-Item -ItemType Directory -Path ".claude/skills/a2dlc", ".agents/skills/a2dlc" -Force | Out-Null
        $SkillUrl = "https://raw.githubusercontent.com/$Repo/main/skills/a2dlc/SKILL.md"
        Invoke-WebRequest -Uri $SkillUrl -OutFile ".claude/skills/a2dlc/SKILL.md" -UseBasicParsing
        Copy-Item ".claude/skills/a2dlc/SKILL.md" ".agents/skills/a2dlc/SKILL.md" -Force
        Write-Host "Installed agent skill to .claude/skills/a2dlc/SKILL.md and .agents/skills/a2dlc/SKILL.md"
    }

    Write-Host "Done! Run 'a2dlc install --agent claude --agent gemini' inside your repository."
}
finally {
    if (Test-Path $TmpDir) {
        Remove-Item -Path $TmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}
