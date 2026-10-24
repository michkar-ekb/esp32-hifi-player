# Downloads an LGPL build of ffmpeg (BtbN/FFmpeg-Builds, has the SoX resampler) and puts
# ffmpeg.exe, ffprobe.exe and their DLLs into the given folder. Run by the installer.
# Everything it does goes to ffmpeg-install.log in that folder, so a failure can be read afterwards.
param([Parameter(Mandatory)][string]$Dest)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'   # the progress bar makes Invoke-WebRequest many times slower
New-Item -ItemType Directory $Dest -Force | Out-Null
$log = Join-Path $Dest 'ffmpeg-install.log'
function Log($m) { "$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') $m" | Out-File $log -Append -Encoding utf8; $m }

$name = 'ffmpeg-n8.1-latest-win64-lgpl-shared-8.1'
$url = "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/$name.zip"
$tmp = Join-Path $env:TEMP ('s3hifi-ffmpeg-' + [guid]::NewGuid())
New-Item -ItemType Directory $tmp | Out-Null
$zip = Join-Path $tmp 'ff.zip'
try {
  Log "download $url"
  # curl.exe is part of Windows 10/11; Invoke-WebRequest is the fallback
  $curl = Join-Path $env:WINDIR 'System32\curl.exe'
  $ok = $false
  if (Test-Path $curl) {
    & $curl -fsSL --retry 2 -o $zip $url 2>&1 | ForEach-Object { Log "curl: $_" }
    $ok = ($LASTEXITCODE -eq 0) -and (Test-Path $zip)
    if (-not $ok) { Log "curl.exe failed (exit $LASTEXITCODE), trying Invoke-WebRequest" }
  }
  if (-not $ok) {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing
  }
  Log "downloaded $((Get-Item $zip).Length) bytes, unpacking"
  Expand-Archive $zip $tmp
  $bin = Join-Path $tmp "$name\bin"
  Copy-Item "$bin\ffmpeg.exe", "$bin\ffprobe.exe" $Dest -Force
  Copy-Item "$bin\*.dll" $Dest -Force
  Copy-Item (Join-Path $tmp "$name\LICENSE.txt") (Join-Path $Dest 'ffmpeg-LICENSE.txt') -Force
  Log "done"
} catch {
  Log "FAILED: $($_.Exception.Message)"
  exit 1
} finally {
  Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
