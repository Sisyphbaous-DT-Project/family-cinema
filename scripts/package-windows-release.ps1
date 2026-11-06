[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidatePattern('^v\d+\.\d+\.\d+$')][string]$Tag,
    [Parameter(Mandatory = $true)][ValidatePattern('^[a-fA-F0-9]{40}$')][string]$SourceCommit,
    [Parameter(Mandatory = $true)][string]$SourceArchive,
    [Parameter(Mandatory = $true)][string]$Apk,
    [Parameter(Mandatory = $true)][int]$ApkVersionCode,
    [Parameter(Mandatory = $true)][string]$ApkVersionName,
    [Parameter(Mandatory = $true)][string]$OutputDirectory,
    [ValidatePattern('^[a-fA-F0-9]{64}$')]
    [string]$GoSha256 = 'ae756cce1cb80c819b4fe01b0353807178f532211b47f72d7fa77949de054ebb',
    [string]$ComponentSettings
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
if (!$ComponentSettings) { $ComponentSettings = Join-Path $repo 'windows\component-versions.json' }
$archivePath = (Resolve-Path -LiteralPath $SourceArchive).ProviderPath
$apkPath = (Resolve-Path -LiteralPath $Apk).ProviderPath
$output = [IO.Path]::GetFullPath($OutputDirectory)
if ($ApkVersionCode -lt 1 -or $ApkVersionName -notmatch '^\d+\.\d+(\.\d+)?$') {
    throw 'APK 版本信息无效。'
}

# 源码包必须来自将要发布的提交，不能把工作区草稿和固定提交混用。
Add-Type -AssemblyName System.IO.Compression.FileSystem
$zip = [IO.Compression.ZipFile]::OpenRead($archivePath)
try {
    $prefix = "family-cinema-$SourceCommit/"
    if (!$zip.GetEntry($prefix + 'media-server/main.go') -or
        !$zip.GetEntry($prefix + 'android/app/build.gradle')) {
        throw '源码压缩包目录与指定的提交不符。请下载该提交对应的 GitHub ZIP。'
    }
} finally {
    $zip.Dispose()
}

[IO.Directory]::CreateDirectory($output) | Out-Null
$version = $Tag.Substring(1)
$apkName = "family-cinema-android-$ApkVersionName.apk"
$destinationApk = Join-Path $output $apkName
if (![string]::Equals($apkPath, $destinationApk, [StringComparison]::OrdinalIgnoreCase)) {
    Copy-Item -LiteralPath $apkPath -Destination $destinationApk
}
$apkHash = (Get-FileHash -LiteralPath $destinationApk -Algorithm SHA256).Hash.ToLowerInvariant()
$repository = 'https://github.com/Sisyphbaous-DT-Project/family-cinema'
$settings = [ordered]@{
    Version = $version
    SourceCommit = $SourceCommit.ToLowerInvariant()
    SourceSha256 = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    SourceUrl = "$repository/archive/$SourceCommit.zip"
    ApkUrl = "$repository/releases/download/$Tag/$apkName"
    ApkSha256 = $apkHash
    ApkVersionCode = $ApkVersionCode
    ApkVersionName = $ApkVersionName
    GoVersion = '1.25.5'
    GoSha256 = $GoSha256.ToLowerInvariant()
}
if ($ComponentSettings) {
    $extra = Get-Content -LiteralPath $ComponentSettings -Raw -Encoding UTF8 | ConvertFrom-Json
    foreach ($property in $extra.PSObject.Properties) {
        if ($settings.Contains($property.Name)) {
            throw "组件配置不能覆盖发行信息：$($property.Name)"
        }
        $settings[$property.Name] = $property.Value
    }
}

$utf8 = New-Object Text.UTF8Encoding($false)
$settingsPath = Join-Path $output 'release-settings.json'
[IO.File]::WriteAllText($settingsPath, ($settings | ConvertTo-Json -Depth 8) + "`n", $utf8)
$exeName = "family-cinema-windows-setup-$version.exe"
$exePath = Join-Path $output $exeName
& (Join-Path $repo 'windows\build.ps1') -Settings $settingsPath -Output $exePath
if (!$? -or !(Test-Path -LiteralPath $exePath)) {
    throw 'Windows 安装器构建失败。'
}

$appVersion = [ordered]@{
    applicationId = 'com.family.cinema'
    versionCode = $ApkVersionCode
    versionName = $ApkVersionName
    fileName = $apkName
    sizeBytes = (Get-Item -LiteralPath $destinationApk).Length
    sha256 = $apkHash
}
$appVersionPath = Join-Path $output 'app-version.json'
if (Test-Path -LiteralPath $appVersionPath) {
    $verifiedVersion = Get-Content -LiteralPath $appVersionPath -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($verifiedVersion.sha256 -eq $apkHash -and $verifiedVersion.certificateSha256 -match '^[a-fA-F0-9]{64}$') {
        $appVersion['certificateSha256'] = $verifiedVersion.certificateSha256
    }
}
[IO.File]::WriteAllText(
    $appVersionPath,
    ($appVersion | ConvertTo-Json) + "`n",
    $utf8
)
$files = @($exePath, $destinationApk, $settingsPath, (Join-Path $output 'app-version.json'))
$checksums = foreach ($file in $files) {
    (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLowerInvariant() +
        '  ' + [IO.Path]::GetFileName($file)
}
[IO.File]::WriteAllText((Join-Path $output 'SHA256SUMS.txt'), ($checksums -join "`n") + "`n", $utf8)
Write-Output "发行包已生成：$output"
