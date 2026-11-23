# Installs the latest quest-map release for Windows (amd64, arm64) into %USERPROFILE%\.local\bin.
#   powershell -ExecutionPolicy Bypass -c "irm https://github.com/RoyalNoob/quest-map/releases/latest/download/quest-map-installer.ps1 | iex"
# $env:QUEST_MAP_INSTALL_DIR overrides the folder; $env:QUEST_MAP_VERSION (e.g. v1.0.0) picks a release.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repo = 'RoyalNoob/quest-map'
$dir = if ($env:QUEST_MAP_INSTALL_DIR) { $env:QUEST_MAP_INSTALL_DIR } else { Join-Path $env:USERPROFILE '.local\bin' }
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "quest-map installer: $($env:PROCESSOR_ARCHITECTURE) is not supported (amd64 and arm64 only)" }
}
$base = if ($env:QUEST_MAP_VERSION) { "https://github.com/$repo/releases/download/$($env:QUEST_MAP_VERSION)" } else { "https://github.com/$repo/releases/latest/download" }
$name = "quest-map_windows_$arch.zip"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("quest-map-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Host "downloading $name"
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$name" -OutFile (Join-Path $tmp $name)
    Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')
    $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match "\s$([regex]::Escape($name))$" } | Select-Object -First 1
    $want = if ($line) { ($line -split '\s+')[0] } else { '' }
    $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $name)).Hash
    if (-not $want -or $got -ne $want.ToUpper()) { throw "quest-map installer: $name does not match checksums.txt; nothing was installed" }
    Expand-Archive -Path (Join-Path $tmp $name) -DestinationPath (Join-Path $tmp 'x')

    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $exe = Join-Path $dir 'quest-map.exe'
    # A running quest-map.exe cannot be overwritten, but it can be renamed out of the way.
    $hadOld = Test-Path $exe
    if ($hadOld) {
        Remove-Item "$exe.old" -Force -ErrorAction SilentlyContinue
        Rename-Item $exe "$exe.old"
    }
    try {
        Copy-Item (Join-Path $tmp 'x\quest-map.exe') $exe
    } catch {
        if ($hadOld) { Move-Item -Force "$exe.old" $exe }  # put the old binary back
        throw
    }
    Remove-Item "$exe.old" -Force -ErrorAction SilentlyContinue

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not (($userPath -split ';') -contains $dir)) {
        [Environment]::SetEnvironmentVariable('Path', ($(if ($userPath) { "$userPath;" } else { '' }) + $dir), 'User')
        Write-Host "added $dir to your PATH for terminals you open from now on"
    }
    Write-Host "installed $(& $exe version) to $exe"
    Write-Host 'next: quest-map install claude-code   (registers quest-map with Claude Code)'
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
