# 为未来 Windows 产品保留的安装器草案；v1 首个预览版不发布 Windows 产物。
$ErrorActionPreference = 'Stop'

function Main {
  param(
    [string]$Version = $(if ($env:TUNLITE_VERSION) { $env:TUNLITE_VERSION } else { 'v1.0.0-preview.1' }),
    [string]$InstallDir = $(if ($env:TUNLITE_INSTALL_DIR) { $env:TUNLITE_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\Tunlite' })
  )

  throw 'tunlite: v1 首个预览版尚未发布 Windows 版本；请等待 Task Scheduler 与真机验证完成'

  if ($Version -notmatch '^v1\.0\.0(?:-preview\.[1-9][0-9]*)?$') {
    throw "tunlite: 不支持的版本格式 $Version"
  }
  if (-not [Environment]::Is64BitOperatingSystem) {
    throw 'tunlite: Windows 预览版仅支持 amd64'
  }

  $plainVersion = $Version.Substring(1)
  $asset = "tunlite_${plainVersion}_windows_amd64.zip"
  $repository = 'https://github.com/tunlite/tunlite'
  $baseUrl = if ($env:TUNLITE_RELEASE_BASE_URL) { $env:TUNLITE_RELEASE_BASE_URL } else { "$repository/releases/download/$Version" }
  $temporary = Join-Path ([IO.Path]::GetTempPath()) ("tunlite-" + [guid]::NewGuid())
  $staged = $null
  New-Item -ItemType Directory -Path $temporary | Out-Null
  try {
    $archive = Join-Path $temporary $asset
    $checksums = Join-Path $temporary 'SHA256SUMS'
    Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/$asset" -OutFile $archive
    Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/SHA256SUMS" -OutFile $checksums
    $line = Get-Content $checksums | Where-Object { $_ -match "^[0-9a-fA-F]{64}\s+$([regex]::Escape($asset))$" } | Select-Object -First 1
    if (-not $line) { throw "tunlite: SHA256SUMS 中没有 $asset" }
    $expected = ($line -split '\s+')[0].ToLowerInvariant()
    $actual = (Get-FileHash -Algorithm SHA256 $archive).Hash.ToLowerInvariant()
    if ($actual -ne $expected) { throw 'tunlite: 下载文件 checksum 不匹配' }

    Expand-Archive -Path $archive -DestinationPath $temporary -Force
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $destination = Join-Path $InstallDir 'tunlite.exe'
    $staged = Join-Path $InstallDir ('.tunlite-install-' + [guid]::NewGuid() + '.exe')
    Copy-Item -Force (Join-Path $temporary 'tunlite.exe') $staged
    Move-Item -Force $staged $destination
    $staged = $null
    & $destination version
    Write-Host "tunlite: 已安装到 $destination"
    if (($env:PATH -split ';') -notcontains $InstallDir) {
      Write-Host "tunlite: 请把 $InstallDir 加入 PATH"
    }
  } finally {
    if ($staged) { Remove-Item -Force $staged -ErrorAction SilentlyContinue }
    Remove-Item -Recurse -Force $temporary -ErrorAction SilentlyContinue
  }
}

Main @args
