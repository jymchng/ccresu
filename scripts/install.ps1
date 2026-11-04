# ccresu installer for Windows.
#
# Downloads a released ccresu binary for this machine's architecture, verifies it
# against the release checksums *before* anything is extracted, installs
# ccresu.exe into a per-user prefix, adds that prefix to the user PATH and
# prepares the configuration directory with the example configuration that ships
# inside the archive.
#
# It is the Windows counterpart of scripts/install.sh and shares its release
# contract: assets are named ccresu_<version>_windows_<arch>.zip and
# checksums.txt holds `<sha256>  <asset>` lines. Both names come from
# .github/workflows/release.yml.
#
# Usage (from a checkout):
#   pwsh -File scripts/install.ps1
#   pwsh -File scripts/install.ps1 -Version 0.1.0
#   pwsh -File scripts/install.ps1 -DryRun
#
# Piped, which is the documented one-liner:
#   irm https://raw.githubusercontent.com/jymchng/ccresu/main/scripts/install.ps1 | iex
#
# A piped script has no arguments, so everything can also be set from the
# environment: CCRESU_VERSION, CCRESU_BASE_URL, CCRESU_PREFIX.
#
# Re-running the script is the supported upgrade path; see docs/upgrade.md.
param(
    [string]$Version = $env:CCRESU_VERSION,
    [string]$BaseUrl = $(if ($env:CCRESU_BASE_URL) { $env:CCRESU_BASE_URL } else { 'https://github.com' }),
    [string]$Prefix,
    [string]$Arch,
    [switch]$Force,
    [switch]$DryRun,
    [switch]$NoPathUpdate,
    [switch]$Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
# Invoke-WebRequest is only used for downloads; keep it quiet and unparsed.
$ProgressPreference = 'SilentlyContinue'

$script:Program = 'ccresu'
$script:ExeName = 'ccresu.exe'
$script:Repo = 'jymchng/ccresu'

# Exit codes: 0 success, 1 failure, 2 usage.
function Write-Info([string]$Message) { Write-Host $Message }
function Write-Warning2([string]$Message) { Write-Host "warning: $Message" -ForegroundColor Yellow }
function Stop-Failure([string]$Message) {
    [Console]::Error.WriteLine("error: $Message")
    exit 1
}
function Stop-Usage([string]$Message) {
    [Console]::Error.WriteLine("error: $Message")
    exit 2
}

function Show-Usage {
    Write-Info @'
Install or upgrade ccresu from GitHub releases (Windows).

Usage: install.ps1 [OPTIONS]

Options:
  -Version VER    Install a specific release (e.g. 0.1.0 or v0.1.0).
                  Default: the newest published release.
  -Prefix DIR     Directory to install ccresu.exe into.
                  Default: %LOCALAPPDATA%\Programs\ccresu\bin
  -Arch ARCH      Override the architecture (amd64 or arm64).
  -BaseUrl URL    Base URL for downloads (default https://github.com).
                  Useful for a mirror or an air-gapped test.
  -Force          Reinstall even when the same version is already present.
  -DryRun         Print the actions without downloading or changing anything.
  -NoPathUpdate   Install the binary but do not touch the user PATH.
  -Help           Show this help.

Environment:
  CCRESU_VERSION      Same as -Version.
  CCRESU_PREFIX       Same as -Prefix.
  CCRESU_BASE_URL     Same as -BaseUrl.

The piped one-liner passes no arguments, so use the environment variables there:
  $env:CCRESU_VERSION = '0.1.0'
  irm https://raw.githubusercontent.com/jymchng/ccresu/main/scripts/install.ps1 | iex
'@
}

# ---------------------------------------------------------------------------
# Argument handling
# ---------------------------------------------------------------------------

if ($Help) {
    Show-Usage
    exit 0
}

if (-not $Prefix -and $env:CCRESU_PREFIX) { $Prefix = $env:CCRESU_PREFIX }
if (-not $Prefix) {
    if ($env:LOCALAPPDATA) {
        $Prefix = Join-Path $env:LOCALAPPDATA 'Programs\ccresu\bin'
    }
    else {
        # pwsh on a non-Windows host, or a stripped environment: still per user.
        $Prefix = Join-Path $HOME '.local/bin'
    }
}
if ([string]::IsNullOrWhiteSpace($Prefix)) { Stop-Usage '-Prefix must not be empty' }
if (-not [System.IO.Path]::IsPathRooted($Prefix)) {
    Stop-Usage "-Prefix must be an absolute path, got: $Prefix"
}

$script:IsWindowsHost = $true
if (Test-Path variable:global:IsWindows) { $script:IsWindowsHost = [bool]$IsWindows }
else { $script:IsWindowsHost = ($env:OS -eq 'Windows_NT') }

function Get-Architecture {
    if ($Arch) {
        switch ($Arch.ToLowerInvariant()) {
            'amd64' { return 'amd64' }
            'x86_64' { return 'amd64' }
            'arm64' { return 'arm64' }
            'aarch64' { return 'arm64' }
            default { Stop-Usage "unsupported architecture: $Arch. Releases cover amd64 and arm64" }
        }
    }
    $raw = ''
    if ($env:PROCESSOR_ARCHITECTURE) { $raw = $env:PROCESSOR_ARCHITECTURE }
    if (-not $raw) {
        $raw = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    }
    switch ($raw.ToUpperInvariant()) {
        'AMD64' { return 'amd64' }
        'X86_64' { return 'amd64' }
        # .NET reports the enum name X64 rather than the vendor string.
        'X64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        'AARCH64' { return 'arm64' }
        default { Stop-Failure "unsupported architecture: $raw. Releases cover amd64 and arm64" }
    }
}

function ConvertTo-BareVersion([string]$Raw) {
    $trimmed = $Raw.Trim()
    if ($trimmed.StartsWith('v') -or $trimmed.StartsWith('V')) { $trimmed = $trimmed.Substring(1) }
    if (-not $trimmed) { Stop-Failure 'empty version' }
    if ($trimmed -notmatch '^[0-9A-Za-z.+-]+$') { Stop-Failure "suspicious version string: $trimmed" }
    return $trimmed
}

# Resolve-LatestVersion asks GitHub for /releases/latest and reads the tag from
# the redirect, which is the same signal install.sh uses. -BaseUrl is honoured so
# a mirror (or the test suite) can answer instead.
function Resolve-LatestVersion {
    $uri = "$BaseUrl/$script:Repo/releases/latest"
    try {
        $handler = [System.Net.Http.HttpClientHandler]::new()
        $handler.AllowAutoRedirect = $false
        $client = [System.Net.Http.HttpClient]::new($handler)
        $client.Timeout = [TimeSpan]::FromSeconds(20)
        try {
            $response = $client.GetAsync($uri).GetAwaiter().GetResult()
            $location = $null
            if ($response.Headers.Location) { $location = $response.Headers.Location.ToString() }
            if (-not $location) { return $null }
            $tag = $location.TrimEnd('/').Split('/')[-1]
            if (-not $tag -or $tag -eq 'latest') { return $null }
            return $tag
        }
        finally {
            $client.Dispose()
            $handler.Dispose()
        }
    }
    catch {
        return $null
    }
}

$script:Architecture = Get-Architecture

# ---------------------------------------------------------------------------
# Version resolution
# ---------------------------------------------------------------------------

$pinned = $false
if ($Version) {
    $Version = ConvertTo-BareVersion $Version
    $pinned = $true
}
else {
    $resolved = Resolve-LatestVersion
    if ($resolved) {
        $Version = ConvertTo-BareVersion $resolved
    }
    elseif ($DryRun) {
        $Version = 'LATEST'
    }
    else {
        Stop-Failure "could not determine the latest release; pass -Version VER (or set CCRESU_VERSION)"
    }
}

$asset = "$($script:Program)_${Version}_windows_$($script:Architecture).zip"
$checksumsAsset = 'checksums.txt'
# Always download from the tag resolved above: the asset name embeds the
# version, so pulling it from the tag keeps the bytes and the reported version in
# lockstep, and GitHub does not serve /releases/latest/download/<tag>/<asset>.
$downloadBase = "$BaseUrl/$($script:Repo)/releases/download/v$Version"
$assetUrl = "$downloadBase/$asset"
$checksumsUrl = "$downloadBase/$checksumsAsset"

$target = Join-Path $Prefix $script:ExeName

# Go resolves the user config directory to %AppData% on Windows, so that is where
# ccresu expects config.yaml (and the API token beside it). The data and state
# directories mirror config.DefaultDataDir / DefaultStateDir under the home dir.
$configRoot = if ($env:APPDATA) { $env:APPDATA } else { Join-Path $HOME '.config' }
$configDir = Join-Path $configRoot $script:Program
$configFile = Join-Path $configDir 'config.yaml'
$dataDir = Join-Path (Join-Path (Join-Path $HOME '.local') 'share') 'ccresu'
$stateDir = Join-Path (Join-Path (Join-Path $HOME '.local') 'state') 'ccresu'

Write-Info "$($script:Program) installer (Windows)"
Write-Info "  platform:  windows/$($script:Architecture)"
if ($pinned) { Write-Info "  release:   v$Version (pinned)" }
else { Write-Info "  release:   v$Version (latest)" }
Write-Info "  asset:     $asset"
Write-Info "  prefix:    $Prefix"
if ($DryRun) { Write-Info '  mode:      dry-run (nothing will be changed)' }

# ---------------------------------------------------------------------------
# Already-installed check
# ---------------------------------------------------------------------------

# Get-InstalledVersion reports what is installed at TARGET, or $null.
#
# The binary is asked first, which is the honest answer. When it cannot run here
# (a Windows .exe inspected from another OS, for instance) the sidecar written by
# the previous install is used instead, so a repeat run is still a no-op rather
# than a re-download.
function Get-InstalledVersion([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    try {
        $output = & $Path version 2>$null
        if ($LASTEXITCODE -eq 0 -and $output) {
            $line = ($output | Select-Object -First 1).ToString().Trim()
            if ($line -match ' (\S+)$') { return $Matches[1] }
            return $line
        }
    }
    catch {
        # Not executable on this host; fall through to the sidecar.
    }
    $sidecar = "$Path.version"
    if (Test-Path -LiteralPath $sidecar) {
        $recorded = (Get-Content -LiteralPath $sidecar -TotalCount 1).Trim()
        if ($recorded) { return $recorded }
    }
    return $null
}

if ((Test-Path -LiteralPath $target) -and -not $Force -and -not $DryRun) {
    $current = Get-InstalledVersion $target
    if ($current -eq $Version) {
        Write-Info ''
        Write-Info "$target is already at v$Version; nothing to do (use -Force to reinstall)"
        exit 0
    }
}

# ---------------------------------------------------------------------------
# Download and verify
# ---------------------------------------------------------------------------

$workdir = Join-Path ([System.IO.Path]::GetTempPath()) ("ccresu-install-" + [System.Guid]::NewGuid().ToString('N'))
if (-not $DryRun) {
    New-Item -ItemType Directory -Path $workdir -Force | Out-Null
}

try {
    Write-Info ''
    Write-Info "Downloading v$Version"
    Write-Info "  $assetUrl"
    Write-Info "  $checksumsUrl"

    if ($DryRun) {
        Write-Info "  would download into $workdir"
    }
    else {
        $checksumsPath = Join-Path $workdir $checksumsAsset
        $assetPath = Join-Path $workdir $asset
        try {
            Invoke-WebRequest -Uri $checksumsUrl -OutFile $checksumsPath -UseBasicParsing
            Invoke-WebRequest -Uri $assetUrl -OutFile $assetPath -UseBasicParsing
        }
        catch {
            Stop-Failure "download failed: $($_.Exception.Message); the release or asset may not exist"
        }

        $expected = $null
        foreach ($line in Get-Content -LiteralPath $checksumsPath) {
            $fields = $line -split '\s+', 2
            if ($fields.Count -lt 2) { continue }
            $name = $fields[1].Trim().TrimStart('*')
            if ($name -eq $asset) { $expected = $fields[0].Trim().ToLowerInvariant(); break }
        }
        if (-not $expected) {
            Stop-Failure "no checksum entry for $asset in $checksumsAsset; refusing to install an unverified binary"
        }

        # Verify BEFORE extracting: an archive is untrusted input until it matches.
        $actual = (Get-FileHash -LiteralPath $assetPath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($expected -ne $actual) {
            Stop-Failure "checksum mismatch for $asset (expected $expected, got $actual); the download was rejected"
        }
        Write-Info "  checksum:  ok ($expected)"

        $staging = Join-Path $workdir 'staging'
        try {
            Expand-Archive -LiteralPath $assetPath -DestinationPath $staging -Force
        }
        catch {
            Stop-Failure "the archive $asset could not be extracted: $($_.Exception.Message)"
        }

        $binary = Get-ChildItem -LiteralPath $staging -Recurse -File -Filter $script:ExeName |
            Select-Object -First 1
        if (-not $binary) {
            Stop-Failure "the archive $asset does not contain a $($script:ExeName) binary"
        }

        # -------------------------------------------------------------------
        # Install
        # -------------------------------------------------------------------

        Write-Info ''
        Write-Info 'Installing'
        New-Item -ItemType Directory -Path $Prefix -Force | Out-Null
        Copy-Item -LiteralPath $binary.FullName -Destination $target -Force
        Set-Content -LiteralPath "$target.version" -Value $Version -NoNewline -Encoding ascii
        Write-Info "  binary:    $target"

        foreach ($dir in @($configDir, $dataDir, $stateDir)) {
            New-Item -ItemType Directory -Path $dir -Force | Out-Null
        }

        if (Test-Path -LiteralPath $configFile) {
            Write-Info "  config:    $configFile (kept)"
        }
        else {
            $example = Join-Path $staging 'configs\config.example.yaml'
            if (-not (Test-Path -LiteralPath $example)) {
                $example = Join-Path $staging 'configs/config.example.yaml'
            }
            if (Test-Path -LiteralPath $example) {
                Copy-Item -LiteralPath $example -Destination $configFile -Force
                Write-Info "  config:    $configFile (new)"
            }
            else {
                # The archive should always carry it; keep working if it does not.
                @'
# ccresu configuration. Every field is documented in
# https://github.com/jymchng/ccresu/blob/main/docs/configuration.md
# Any field can be overridden with a CCRESU_ environment variable.
app:
  log_level: info
  log_format: json

recovery:
  enabled: true
  max_attempts: 10

telegram:
  enabled: false
  bot_token: ""
  chat_id: ""

server:
  enabled: true
  host: 127.0.0.1
  port: 8787

storage:
  driver: sqlite
'@ | Set-Content -LiteralPath $configFile -Encoding utf8
                Write-Info "  config:    $configFile (new, built-in example)"
            }
        }
        Write-Info "  data:      $dataDir"
        Write-Info "  state:     $stateDir"

        # -------------------------------------------------------------------
        # PATH
        # -------------------------------------------------------------------

        $pathNote = $null
        if ($NoPathUpdate) {
            $pathNote = "-NoPathUpdate was given; add $Prefix to PATH yourself"
        }
        elseif (-not $script:IsWindowsHost) {
            $pathNote = "not a Windows host; add $Prefix to PATH yourself"
        }
        else {
            $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
            if (-not $userPath) { $userPath = '' }
            $entries = @($userPath.Split(';', [System.StringSplitOptions]::RemoveEmptyEntries) |
                ForEach-Object { $_.Trim().TrimEnd('\') })
            $wanted = $Prefix.Trim().TrimEnd('\')
            if ($entries -contains $wanted) {
                Write-Info "  path:      $Prefix (already on the user PATH)"
            }
            else {
                $joined = if ($userPath -and -not $userPath.EndsWith(';')) { "$userPath;$Prefix" } else { "$userPath$Prefix" }
                [Environment]::SetEnvironmentVariable('Path', $joined, 'User')
                Write-Info "  path:      $Prefix (added to the user PATH)"
                $pathNote = 'open a new terminal so the PATH change is picked up'
            }
        }

        # -------------------------------------------------------------------
        # Post-install verification
        # -------------------------------------------------------------------

        Write-Info ''
        Write-Info 'Verifying'
        $installedVersion = Get-InstalledVersion $target
        if ($installedVersion) {
            Write-Info "  binary:    $($script:Program) $installedVersion"
            if ($installedVersion -ne $Version) {
                Stop-Failure "the installed binary reports v$installedVersion, expected v$Version"
            }
        }
        elseif ($script:IsWindowsHost) {
            Stop-Failure "the installed binary cannot run: $target version failed"
        }
        else {
            Write-Warning2 "cannot execute a Windows binary on this host; the version check was skipped"
        }

        if ($script:IsWindowsHost) {
            & $target config validate $configFile 2>$null | Out-Null
            if ($LASTEXITCODE -ne 0) {
                Stop-Failure "the installed configuration is invalid: $configFile"
            }
            Write-Info '  config:    valid'
        }
        else {
            Write-Info "  config:    written (not validated: the Windows binary cannot run here)"
        }
        Write-Info ''
        Write-Info 'Done. Next steps:'
        Write-Info "  1. edit $configFile"
        Write-Info "  2. $($script:Program) doctor"
        Write-Info "  3. $($script:Program) daemon   # or run it as a service; see docs\deployment.md"
        if ($pathNote) {
            Write-Info ''
            Write-Info "note: $pathNote"
        }
        exit 0
    }

    # Dry run: describe what a real run would do, download and write nothing.
    Write-Info "  would verify $asset against $checksumsAsset with SHA-256"
    Write-Info "  would run: Expand-Archive $asset"
    Write-Info "  would install <extracted $($script:ExeName)> to $target"
    Write-Info "  would create $configFile from the example shipped in the archive"
    Write-Info '  would add the prefix to the user PATH'
    Write-Info ''
    Write-Info 'Would verify: ccresu version && ccresu config validate <config>'
    Write-Info ''
    Write-Info 'Dry run complete; nothing was changed.'
    exit 0
}
finally {
    if (-not $DryRun -and (Test-Path -LiteralPath $workdir)) {
        Remove-Item -LiteralPath $workdir -Recurse -Force -ErrorAction SilentlyContinue
    }
}
