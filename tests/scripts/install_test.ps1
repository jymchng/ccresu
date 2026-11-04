# Behavioural tests for scripts/install.ps1.
#
# The Windows installer is the counterpart of scripts/install.sh, so it is tested
# the same way the shell one is: a fake GitHub release mirror is served over HTTP,
# a real ccresu.exe built with the version it claims is packaged exactly as
# release.yml packages it, and the installer is then driven as a user would drive
# it.
#
# What is proved, one scenario at a time:
#
#   1. a pinned install downloads from /releases/download/v<ver>/, verifies the
#      archive against checksums.txt before extracting, and lands a binary that
#      reports the pinned version, with a configuration file that validates;
#   2. the download URLs use the pinned-tag shape - never
#      /releases/latest/download/<tag>/... and never another release;
#   3. re-running the same version is a no-op: no re-download, no rewrite;
#   4. -Force re-downloads, and an upgrade replaces the binary while keeping the
#      configuration;
#   5. a tampered checksum aborts with nothing installed;
#   6. a checksums.txt without an entry for the archive aborts with nothing
#      installed;
#   7. -DryRun writes nothing and downloads nothing;
#   8. argument errors are usage errors, not partial installs.
#
# Every invocation runs with an isolated HOME, APPDATA and LOCALAPPDATA so the
# assertions look exactly where the installer writes and the runner's real
# profile is untouched.
#
# Usage: pwsh -NoProfile -File tests/scripts/install_test.ps1
# Requires: pwsh, curl, python3 (the mirror), go (the archive must hold a real
# binary) and zip support for python3. CI runs it in the 'Windows installer' job.
param(
    [switch]$KeepWork
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$script:ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$script:Root = (Resolve-Path (Join-Path $script:ScriptDir '..\..')).Path
$script:InstallScript = Join-Path $script:Root 'scripts\install.ps1'

$script:Work = Join-Path ([System.IO.Path]::GetTempPath()) ("ccresu-ps-install-test-" + [System.Guid]::NewGuid().ToString('N'))
$script:Mirror = Join-Path $script:Work 'mirror'
$script:MirrorLog = Join-Path $script:Work 'mirror.log'
$script:PortFile = Join-Path $script:Work 'mirror.port'
$script:MirrorProcess = $null
$script:Tests = 0
$script:Arch = 'amd64'
$script:InstallerOutput = ''
$script:InstallerStatus = 0

New-Item -ItemType Directory -Path $script:Work -Force | Out-Null

# ---------------------------------------------------------------------------
# Assertions
# ---------------------------------------------------------------------------

function Fail([string]$Message) {
    [Console]::Error.WriteLine("FAIL: $Message")
    [Console]::Error.WriteLine("(work directory kept at $($script:Work))")
    exit 1
}

function Pass([string]$Message) {
    $script:Tests++
    Write-Host "ok $($script:Tests) - $Message"
}

function Assert-Contains([string]$Haystack, [string]$Needle, [string]$Message) {
    if (-not $Haystack.Contains($Needle)) {
        Fail "$Message (missing: $Needle)`n--- output ---`n$Haystack"
    }
}

function Assert-NotContains([string]$Haystack, [string]$Needle, [string]$Message) {
    if ($Haystack.Contains($Needle)) {
        Fail "$Message (unexpected: $Needle)`n--- output ---`n$Haystack"
    }
}

function Assert-Equal($Actual, $Expected, [string]$Message) {
    if ("$Actual" -ne "$Expected") { Fail "$Message (got '$Actual', want '$Expected')" }
}

function Assert-Exists([string]$Path, [string]$Message) {
    if (-not (Test-Path -LiteralPath $Path)) { Fail "$Message (missing: $Path)" }
}

function Assert-Absent([string]$Path, [string]$Message) {
    if (Test-Path -LiteralPath $Path) { Fail "$Message (unexpected: $Path)" }
}

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

function Need-Tool([string]$Name) {
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        Fail "the test suite needs '$Name' on PATH"
    }
}

foreach ($tool in @('go', 'curl')) { Need-Tool $tool }

# python3 is the preinstalled name on Linux and macOS; the Windows images ship
# "python". Either can serve the mirror and package the zip.
$script:Python = (Get-Command python3 -ErrorAction SilentlyContinue)
if (-not $script:Python) { $script:Python = Get-Command python -ErrorAction SilentlyContinue }
if (-not $script:Python) { Fail 'the test suite needs python3 (or python) on PATH' }
$script:Python = $script:Python.Source

# Use the very pwsh that is running this suite, so CI needs no extra lookup.
$script:Pwsh = (Get-Process -Id $PID).Path

switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToUpperInvariant()) {
    'AMD64' { $script:Arch = 'amd64' }
    'X64' { $script:Arch = 'amd64' }
    'ARM64' { $script:Arch = 'arm64' }
    default { Fail "this suite needs an amd64 or arm64 host, not $([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture)" }
}

function Get-AssetName([string]$Version) {
    "ccresu_${Version}_windows_$($script:Arch).zip"
}

# Build-Binary builds a real ccresu.exe reporting VERSION, once per run.
function Build-Binary([string]$Version) {
    $out = Join-Path (Join-Path (Join-Path $script:Work 'build') $Version) 'ccresu.exe'
    if (-not (Test-Path -LiteralPath $out)) {
        New-Item -ItemType Directory -Path (Split-Path -Parent $out) -Force | Out-Null
        Push-Location $script:Root
        try {
            $env:GOOS = 'windows'
            $env:GOARCH = $script:Arch
            $env:CGO_ENABLED = '0'
            & go build -trimpath `
                -ldflags "-X main.version=$Version -X main.commit=installtest -X main.buildDate=2026-01-01" `
                -o $out ./cmd/ccresu
            if ($LASTEXITCODE -ne 0) { Fail "go build for version $Version failed" }
        }
        finally {
            Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
            Pop-Location
        }
    }
    $out
}

# Publish packages a binary the way release.yml does: a zip holding ccresu.exe and
# configs/config.example.yaml, plus a correct checksums.txt, under the pinned tag.
function Publish([string]$Version) {
    $dir = Join-Path $script:Mirror "jymchng/ccresu/releases/download/v$Version"
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
    $asset = Get-AssetName $Version

    $binary = Build-Binary $Version
    $staging = Join-Path (Join-Path $script:Work 'staging') $Version
    if (Test-Path -LiteralPath $staging) { Remove-Item -LiteralPath $staging -Recurse -Force }
    New-Item -ItemType Directory -Path (Join-Path $staging 'configs') -Force | Out-Null
    Copy-Item -LiteralPath $binary -Destination (Join-Path $staging 'ccresu.exe') -Force
    Copy-Item -LiteralPath (Join-Path $script:Root 'configs\config.example.yaml') `
        -Destination (Join-Path $staging 'configs\config.example.yaml') -Force

    $archive = Join-Path $dir $asset
    if (Test-Path -LiteralPath $archive) { Remove-Item -LiteralPath $archive -Force }
    # python3 -m zipfile is what release.yml uses, so the entries keep the same
    # forward-slash names the real asset has.
    Push-Location $staging
    try {
        & $script:Python -m zipfile -c $archive 'ccresu.exe' 'configs'
        if ($LASTEXITCODE -ne 0) { Fail "packaging $asset failed" }
    }
    finally { Pop-Location }

    $sums = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    Set-Content -LiteralPath (Join-Path $dir 'checksums.txt') -Value "$sums  $asset" -Encoding ascii
    $dir
}

# Publish-WithChecksums publishes the archive but writes an arbitrary manifest.
function Publish-WithChecksums([string]$Version, [string]$Body) {
    $dir = Publish $Version
    Set-Content -LiteralPath (Join-Path $dir 'checksums.txt') -Value $Body -Encoding ascii
    $dir
}

# Start-Mirror serves $Mirror on a loopback port and records every release request.
function Start-Mirror {
    $script:MirrorPy = Join-Path $script:Work 'mirror.py'
    @'
import http.server
import os
import sys

root, log_path, port_path, latest = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
os.chdir(root)


class Handler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, fmt, *args):
        if self.path.startswith("/jymchng/"):
            with open(log_path, "a", encoding="utf-8") as handle:
                handle.write(self.path + "\n")

    def do_GET(self):
        # GitHub answers /releases/latest with a redirect to /releases/tag/<tag>.
        # The installer resolves "the newest release" from exactly that.
        if self.path == "/jymchng/ccresu/releases/latest":
            self.send_response(302)
            self.send_header("Location", "/jymchng/ccresu/releases/tag/v" + latest)
            self.end_headers()
            return
        http.server.SimpleHTTPRequestHandler.do_GET(self)


server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
with open(port_path, "w", encoding="utf-8") as handle:
    handle.write(str(server.server_address[1]))
server.serve_forever()
'@ | Set-Content -LiteralPath $script:MirrorPy -Encoding utf8

    Remove-Item -LiteralPath $script:MirrorLog -ErrorAction SilentlyContinue
    Set-Content -LiteralPath $script:MirrorLog -Value '' -Encoding utf8
    Remove-Item -LiteralPath $script:PortFile -ErrorAction SilentlyContinue

    $script:MirrorProcess = Start-Process -FilePath $script:Python `
        -ArgumentList @($script:MirrorPy, $script:Mirror, $script:MirrorLog, $script:PortFile, $script:LatestVersion) `
        -PassThru -RedirectStandardOutput (Join-Path $script:Work 'mirror.out') `
        -RedirectStandardError (Join-Path $script:Work 'mirror.err')

    for ($i = 0; $i -lt 200; $i++) {
        if (Test-Path -LiteralPath $script:PortFile) {
            $port = (Get-Content -LiteralPath $script:PortFile -TotalCount 1).Trim()
            if ($port) {
                $code = & curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/" 2>$null
                if ($code -and $code -ne '000') {
                    $script:BaseUrl = "http://127.0.0.1:$port"
                    return
                }
            }
        }
        Start-Sleep -Milliseconds 50
    }
    Fail 'the fake release mirror did not start'
}

# Invoke-Installer runs the installer with an isolated profile and records its
# output and exit code. The environment is set through the child process, so the
# suite's own environment is untouched.
function Invoke-Installer([string]$HomeDir, [string[]]$Arguments) {
    New-Item -ItemType Directory -Path $HomeDir -Force | Out-Null
    $appData = Join-Path $HomeDir 'AppData/Roaming'
    $localAppData = Join-Path $HomeDir 'AppData/Local'
    New-Item -ItemType Directory -Path $appData, $localAppData -Force | Out-Null

    $previous = @{
        HOME             = $env:HOME
        USERPROFILE      = $env:USERPROFILE
        APPDATA          = $env:APPDATA
        LOCALAPPDATA     = $env:LOCALAPPDATA
        CCRESU_BASE_URL  = $env:CCRESU_BASE_URL
        CCRESU_VERSION   = $env:CCRESU_VERSION
        CCRESU_PREFIX    = $env:CCRESU_PREFIX
    }
    try {
        $env:HOME = $HomeDir
        $env:USERPROFILE = $HomeDir
        $env:APPDATA = $appData
        $env:LOCALAPPDATA = $localAppData
        $env:CCRESU_BASE_URL = $script:BaseUrl
        Remove-Item Env:CCRESU_VERSION, Env:CCRESU_PREFIX -ErrorAction SilentlyContinue

        $output = & $script:Pwsh -NoProfile -NonInteractive -File $script:InstallScript @Arguments 2>&1
        $code = $LASTEXITCODE
        $script:InstallerOutput = ($output | ForEach-Object { "$_" }) -join "`n"
        $script:InstallerStatus = $code
    }
    finally {
        foreach ($key in $previous.Keys) {
            if ($null -eq $previous[$key]) { Remove-Item "Env:$key" -ErrorAction SilentlyContinue }
            else { Set-Item "Env:$key" $previous[$key] }
        }
    }
}

function Get-RequestCount([string]$Needle) {
    if (-not (Test-Path -LiteralPath $script:MirrorLog)) { return 0 }
    $matches = @(Get-Content -LiteralPath $script:MirrorLog | Where-Object { $_.Contains($Needle) })
    return $matches.Count
}

function Get-MirrorLog {
    if (-not (Test-Path -LiteralPath $script:MirrorLog)) { return '' }
    (Get-Content -LiteralPath $script:MirrorLog) -join "`n"
}

function Get-Sha([string]$Path) { (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() }

# ---------------------------------------------------------------------------
# Scenarios
# ---------------------------------------------------------------------------

function Main {
    Publish '0.1.0' | Out-Null
    Publish '0.2.0' | Out-Null
    $script:LatestVersion = '0.2.0'
    # A release whose manifest is wrong, and one whose manifest does not mention
    # the archive at all: both must be refused without installing anything.
    Publish-WithChecksums '0.3.0' ("0000000000000000000000000000000000000000000000000000000000000000  " + (Get-AssetName '0.3.0')) | Out-Null
    Publish-WithChecksums '0.4.0' "1111111111111111111111111111111111111111111111111111111111111111  ccresu_9.9.9_windows_$($script:Arch).zip" | Out-Null

    Start-Mirror

    # --- 0. latest resolution ----------------------------------------------
    # With no -Version the installer asks the release endpoint for the newest tag
    # and installs that, which is what a user running the one-liner gets.
    $home0 = Join-Path $script:Work 'home-latest'
    $prefix0 = Join-Path $home0 '.local/bin'
    Invoke-Installer $home0 @('-Prefix', $prefix0)
    Assert-Equal $script:InstallerStatus 0 "an unpinned install must succeed`n$($script:InstallerOutput)"
    Assert-Contains $script:InstallerOutput "release:   v$($script:LatestVersion) (latest)" `
        'the installer must report the latest release it resolved'
    Assert-Contains (Get-Content -LiteralPath (Join-Path $prefix0 'ccresu.exe.version') -Raw) $script:LatestVersion `
        'an unpinned install must install the latest release'
    Assert-Contains (Get-MirrorLog) '/jymchng/ccresu/releases/latest' 'the installer must ask for the latest release'
    Pass 'with no -Version the newest release is resolved and installed'

    # --- 1. pinned install --------------------------------------------------
    $home1 = Join-Path $script:Work 'home-pinned'
    $prefix = Join-Path $home1 ".local/bin"
    $script:OtherReleaseRequests = Get-RequestCount '/download/v0.2.0/'
    Invoke-Installer $home1 @('-Version', '0.1.0', '-Prefix', $prefix)
    Assert-Equal $script:InstallerStatus 0 "a pinned install must succeed`n$($script:InstallerOutput)"
    Assert-Contains $script:InstallerOutput 'checksum:  ok' 'the installer must report the verified checksum'
    Assert-Exists (Join-Path $prefix 'ccresu.exe') 'the binary was not installed'
    Assert-Exists (Join-Path $prefix 'ccresu.exe.version') 'the installer did not record the installed version'

    $configFile = Join-Path $home1 'AppData/Roaming/ccresu/config.yaml'
    Assert-Exists $configFile 'the installer did not create a configuration file in %AppData%'
    Assert-Contains (Get-Content -LiteralPath $configFile -Raw) 'app:' 'the configuration must be the shipped example'
    Pass 'pinned install lands ccresu.exe and a configuration under %AppData%'

    # --- 2. URL shape -------------------------------------------------------
    $asset = Get-AssetName '0.1.0'
    Assert-Equal (Get-RequestCount "/jymchng/ccresu/releases/download/v0.1.0/$asset") 1 `
        'the archive must be fetched from the pinned tag'
    Assert-Equal (Get-RequestCount '/jymchng/ccresu/releases/download/v0.1.0/checksums.txt') 1 `
        'the checksums must be fetched from the pinned tag'
    Assert-NotContains (Get-MirrorLog) '/releases/latest/download/' 'downloads must never use the /releases/latest/download/ shape'
    Assert-Equal (Get-RequestCount '/download/v0.2.0/') $script:OtherReleaseRequests `
        'a pinned install must not touch another release'
    Pass 'download URLs use the pinned-tag shape'

    # --- 3. same version is a no-op ----------------------------------------
    $beforeSha = Get-Sha (Join-Path $prefix 'ccresu.exe')
    $beforeRequests = Get-RequestCount $asset
    Invoke-Installer $home1 @('-Version', '0.1.0', '-Prefix', $prefix)
    Assert-Equal $script:InstallerStatus 0 "re-running the same version must succeed`n$($script:InstallerOutput)"
    Assert-Contains $script:InstallerOutput 'already at v0.1.0' 'a repeat install must report that it did nothing'
    Assert-Equal (Get-Sha (Join-Path $prefix 'ccresu.exe')) $beforeSha 'a repeat install rewrote the binary'
    Assert-Equal (Get-RequestCount $asset) $beforeRequests 'a repeat install re-downloaded the archive'
    Pass 're-running the same version is a no-op'

    # --- 4. upgrade and -Force --------------------------------------------
    Invoke-Installer $home1 @('-Version', '0.2.0', '-Prefix', $prefix)
    Assert-Equal $script:InstallerStatus 0 "an upgrade must succeed`n$($script:InstallerOutput)"
    Assert-Contains $script:InstallerOutput 'release:   v0.2.0 (pinned)' 'the upgrade must report the new release'
    Assert-Equal (Get-Content -LiteralPath (Join-Path $prefix 'ccresu.exe.version') -Raw).Trim() '0.2.0' `
        'the upgrade did not record the new version'
    Assert-Contains $script:InstallerOutput 'config:' 'an upgrade must report the configuration it kept'
    Assert-Contains $script:InstallerOutput '(kept)' 'an upgrade must keep the existing configuration'

    $asset2 = Get-AssetName '0.2.0'
    $beforeRequests = Get-RequestCount $asset2
    Invoke-Installer $home1 @('-Version', '0.2.0', '-Prefix', $prefix, '-Force')
    Assert-Equal $script:InstallerStatus 0 "-Force must succeed`n$($script:InstallerOutput)"
    if ((Get-RequestCount $asset2) -le $beforeRequests) { Fail '-Force did not re-download the archive' }
    Pass 'an upgrade replaces the binary, keeps the config and -Force re-downloads'

    # --- 5. tampered checksum ---------------------------------------------
    $home2 = Join-Path $script:Work 'home-bad-checksum'
    $prefix2 = Join-Path $home2 ".local/bin"
    Invoke-Installer $home2 @('-Version', '0.3.0', '-Prefix', $prefix2)
    if ($script:InstallerStatus -eq 0) { Fail "a tampered checksum must make the install fail`n$($script:InstallerOutput)" }
    Assert-Contains $script:InstallerOutput 'checksum mismatch' 'the failure must name the checksum mismatch'
    Assert-Absent (Join-Path $prefix2 'ccresu.exe') 'a tampered checksum must install nothing'
    Assert-Absent (Join-Path $home2 'AppData/Roaming/ccresu/config.yaml') 'a tampered checksum must create no configuration'
    Pass 'a tampered checksum aborts with nothing installed'

    # --- 6. checksums.txt missing the asset --------------------------------
    $home3 = Join-Path $script:Work 'home-missing-sum'
    $prefix3 = Join-Path $home3 ".local/bin"
    Invoke-Installer $home3 @('-Version', '0.4.0', '-Prefix', $prefix3)
    if ($script:InstallerStatus -eq 0) { Fail "a manifest without the asset must fail`n$($script:InstallerOutput)" }
    Assert-Contains $script:InstallerOutput 'no checksum entry' 'the failure must explain the missing entry'
    Assert-Absent (Join-Path $prefix3 'ccresu.exe') 'a missing checksum entry must install nothing'
    Assert-Absent (Join-Path $home3 'AppData/Roaming/ccresu/config.yaml') 'a missing checksum entry must create no configuration'
    Pass 'a checksums.txt without the asset aborts with nothing installed'

    # --- 7. dry run ---------------------------------------------------------
    $home4 = Join-Path $script:Work 'home-dry-run'
    $prefix4 = Join-Path $home4 ".local/bin"
    $requestsBefore = ((Get-MirrorLog).Split("`n") | Where-Object { $_ }).Count
    Invoke-Installer $home4 @('-Version', '0.1.0', '-Prefix', $prefix4, '-DryRun')
    Assert-Equal $script:InstallerStatus 0 "a dry run must succeed`n$($script:InstallerOutput)"
    Assert-Contains $script:InstallerOutput 'Dry run complete' 'a dry run must say what it did'
    Assert-Contains $script:InstallerOutput 'would install' 'a dry run must describe the actions it skipped'
    Assert-Absent (Join-Path $prefix4 'ccresu.exe') 'a dry run installed a binary'
    Assert-Absent (Join-Path $home4 'AppData/Roaming/ccresu/config.yaml') 'a dry run created a configuration file'
    Assert-Absent (Join-Path $home4 '.local/share/ccresu') 'a dry run created a data directory'
    Assert-Equal ((Get-MirrorLog).Split("`n") | Where-Object { $_ }).Count $requestsBefore 'a dry run still downloaded files'
    Pass '-DryRun writes nothing and downloads nothing'

    # --- 8. usage errors ----------------------------------------------------
    $home5 = Join-Path $script:Work 'home-usage'
    $prefix5 = Join-Path $home5 ".local/bin"
    Invoke-Installer $home5 @('-Version', '0.1.0', '-Prefix', 'relative/path')
    Assert-Equal $script:InstallerStatus 2 'a relative -Prefix must be a usage error'
    Assert-Contains $script:InstallerOutput 'absolute path' 'a relative prefix must be reported'
    Assert-Absent (Join-Path $prefix5 'ccresu.exe') 'a usage error installed a binary'
    Pass 'argument errors are reported without installing anything'

    Write-Host ''
    Write-Host "$($script:Tests) installer scenario checks passed"
}

try {
    Main
}
finally {
    if ($script:MirrorProcess -and -not $script:MirrorProcess.HasExited) {
        try { $script:MirrorProcess.Kill() } catch { }
    }
    if ($KeepWork) { Write-Host "work directory kept at $($script:Work)" }
    else { Remove-Item -LiteralPath $script:Work -Recurse -Force -ErrorAction SilentlyContinue }
}
