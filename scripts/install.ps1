<#
.SYNOPSIS
  Install Cairn from a GitHub release.

.EXAMPLE
  irm https://raw.githubusercontent.com/dotnetemmanuel/Cairn/main/scripts/install.ps1 | iex

.EXAMPLE
  # a specific version, or a different directory
  & ([scriptblock]::Create((irm https://raw.githubusercontent.com/dotnetemmanuel/Cairn/main/scripts/install.ps1))) -Version v0.1.1
  .\install.ps1 -InstallDir C:\tools\cairn

.DESCRIPTION
  Installs into $env:LOCALAPPDATA\Programs\cairn (override with -InstallDir or
  $env:CAIRN_INSTALL_DIR) and adds that directory to your USER PATH, not the
  system one. Never installs anything else: it checks for the tools Cairn
  shells out to and tells you what to run.
#>
param(
    [string]$Version,
    [string]$InstallDir
)

$ErrorActionPreference = "Stop"

$Repo = "dotnetemmanuel/Cairn"
if (-not $InstallDir) {
    if ($env:CAIRN_INSTALL_DIR) { $InstallDir = $env:CAIRN_INSTALL_DIR }
    else { $InstallDir = Join-Path $env:LOCALAPPDATA "Programs\cairn" }
}

function Die($msg) {
    Write-Error "cairn install: $msg"
    exit 1
}
function Say($msg) { Write-Host $msg }

# PROCESSOR_ARCHITECTURE reports the emulated architecture under WOW64 (e.g. a
# 32-bit PowerShell on an ARM64 machine reports x86); PROCESSOR_ARCHITEW6432
# carries the real one when it is set.
$arch = $env:PROCESSOR_ARCHITECTURE
if ($env:PROCESSOR_ARCHITEW6432) { $arch = $env:PROCESSOR_ARCHITEW6432 }
switch ($arch) {
    "AMD64" { $arch = "amd64" }
    "ARM64" { $arch = "arm64" }
    default { Die "unsupported architecture: $arch" }
}

if (-not $Version) {
    Say "Looking up the latest release..."
    try {
        $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers @{ "User-Agent" = "cairn-install.ps1" }
    } catch {
        Die "could not find the latest release. Pass one with -Version, or check https://github.com/$Repo/releases"
    }
    $Version = $release.tag_name
}

# Release archives are named without the leading v; see .goreleaser.yaml.
$bare = $Version.TrimStart("v")
$archive = "cairn_${bare}_windows_${arch}.zip"
$base = "https://github.com/$Repo/releases/download/$Version"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    $archivePath = Join-Path $tmp $archive
    $checksumsPath = Join-Path $tmp "checksums.txt"

    Say "Downloading Cairn $Version for windows/$arch..."
    try {
        Invoke-WebRequest -Uri "$base/$archive" -OutFile $archivePath
    } catch {
        Die "could not download $archive. Check that $Version has a build for windows/${arch}: https://github.com/$Repo/releases"
    }
    try {
        Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile $checksumsPath
    } catch {
        Die "could not download checksums.txt for $Version"
    }

    # Verify before unpacking: a truncated or tampered download must never
    # reach the PATH.
    $checksumLine = Select-String -Path $checksumsPath -Pattern " $([regex]::Escape($archive))$"
    if (-not $checksumLine) { Die "$archive is not listed in checksums.txt" }
    $expected = ($checksumLine.Line -split "\s+")[0]
    $actual = (Get-FileHash -Path $archivePath -Algorithm SHA256).Hash
    if ($actual.ToLower() -ne $expected.ToLower()) {
        Die "checksum mismatch for $archive. Do not use this download."
    }

    $extractDir = Join-Path $tmp "extracted"
    Expand-Archive -Path $archivePath -DestinationPath $extractDir -Force
    $exe = Join-Path $extractDir "cairn.exe"
    if (-not (Test-Path $exe)) { Die "the archive did not contain cairn.exe" }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item -Path $exe -Destination (Join-Path $InstallDir "cairn.exe") -Force

    $installedVersion = & (Join-Path $InstallDir "cairn.exe") version
    Say "Installed $installedVersion to $InstallDir\cairn.exe"
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$pathEntries = @()
if ($userPath) { $pathEntries = $userPath -split ";" }
if ($pathEntries -notcontains $InstallDir) {
    $newPath = if ($userPath) { "$userPath;$InstallDir" } else { $InstallDir }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    $env:Path = "$env:Path;$InstallDir"
    Say ""
    Say "Added $InstallDir to your user PATH. Restart your terminal to pick it up."
    Say "To undo: remove that entry from Environment Variables > Path (User), or run:"
    Say "  [Environment]::SetEnvironmentVariable('Path', ((Get-Content Env:Path) -split ';' | Where-Object { `$_ -ne '$InstallDir' }) -join ';', 'User')"
}

# Mirrors internal/doctor/hints.go's installHint for the windows case: keep the
# two in sync, since this is what a fresh install prints and doctor is what
# every later run prints.
$missing = @()
foreach ($tool in @("git", "git-town", "gh")) {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) { $missing += $tool }
}
if ($missing.Count -gt 0) {
    Say ""
    Say "Cairn shells out to these, and they are not on your PATH: $($missing -join ' ')"
    foreach ($tool in $missing) {
        switch ($tool) {
            "git" { Say "  winget install --id Git.Git" }
            "gh" { Say "  winget install --id GitHub.cli" }
            "git-town" { Say "  run the installer git-town_windows_intel_64.msi from https://github.com/git-town/git-town/releases/latest" }
        }
    }
}

Say ""
Say "Next: gh auth login    (Cairn reuses your gh token, it never stores one)"
Say "      gh auth refresh -s read:org,workflow   (org repos need read:org, Actions need workflow)"
Say "      cairn doctor     (checks your setup and says what is missing)"
Say "      cairn            (launch)"
