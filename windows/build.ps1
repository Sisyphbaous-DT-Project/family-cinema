param(
    [Parameter(Mandatory = $true)][string]$Settings,
    [Parameter(Mandatory = $true)][string]$Output,
    [string]$UiFile,
    [string]$DependencyCache
)
$ErrorActionPreference = 'Stop'
$compiler = Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'
if (!(Test-Path -LiteralPath $compiler)) { throw '需要 Windows 的 .NET Framework 4.x C# 编译器。' }
$settingsPath = (Resolve-Path -LiteralPath $Settings).ProviderPath
if (!$UiFile) { $UiFile = Join-Path $PSScriptRoot 'ui\dist\index.html' }
if (!(Test-Path -LiteralPath $UiFile)) { throw '尚未构建安装界面。请先在 windows/ui 运行 npm ci 和 npm run build，或用 -UiFile 指定单文件 HTML。' }
$uiPath = (Resolve-Path -LiteralPath $UiFile).ProviderPath
$ui = Get-Content -LiteralPath $uiPath -Raw -Encoding UTF8
if ($ui -notmatch '<head[\s>]' -or [Text.Encoding]::UTF8.GetByteCount($ui) -gt 1900 * 1024) { throw '安装界面必须是小于 1900KB 的完整单文件 HTML。' }
if ($ui -match '<script[^>]+\bsrc\s*=' -or $ui -match '<link[^>]+rel\s*=\s*["'']stylesheet["''][^>]+\bhref\s*=') { throw '安装界面仍引用外部脚本或样式，请生成完全内联的单文件 HTML。' }
if (!$DependencyCache) { $DependencyCache = Join-Path $env:LOCALAPPDATA 'FamilyCinemaBuild\WebView2' }
$dependencyPath = [IO.Path]::GetFullPath($DependencyCache)
[IO.Directory]::CreateDirectory($dependencyPath) | Out-Null
$sdkVersion = '1.0.2903.40'
$packageHash = 'ef128016dd1e51c59178c827ed5b8aa3322c57afa8675d930f8109505542ad74'
$package = Join-Path $dependencyPath "microsoft.web.webview2.$sdkVersion.nupkg"
$packageUrl = "https://api.nuget.org/v3-flatcontainer/microsoft.web.webview2/$sdkVersion/microsoft.web.webview2.$sdkVersion.nupkg"
if (!(Test-Path -LiteralPath $package) -or (Get-FileHash -LiteralPath $package -Algorithm SHA256).Hash.ToLowerInvariant() -ne $packageHash) {
    $bash = Join-Path $env:ProgramFiles 'Git\bin\bash.exe'
    if (!(Test-Path -LiteralPath $bash)) { throw '维护者构建需要 Git Bash 下载固定版本的 WebView2 SDK。' }
    $temporaryPackage = $package + '.download'
    try {
        & $bash -lc 'output=$(cygpath -u "$1"); exec curl -fSL --retry 2 --connect-timeout 10 --max-time 240 --proxy http://127.0.0.1:7890 -o "$output" "$2"' -- $temporaryPackage $packageUrl
        if ($LASTEXITCODE -ne 0) { throw 'WebView2 SDK 下载失败，请稍后重试。' }
        if ((Get-FileHash -LiteralPath $temporaryPackage -Algorithm SHA256).Hash.ToLowerInvariant() -ne $packageHash) { throw 'WebView2 SDK 固定哈希不匹配，已停止构建。' }
        if (Test-Path -LiteralPath $package) { Remove-Item -LiteralPath $package }
        Move-Item -LiteralPath $temporaryPackage -Destination $package
    } finally { if (Test-Path -LiteralPath $temporaryPackage) { Remove-Item -LiteralPath $temporaryPackage } }
}
Add-Type -AssemblyName System.IO.Compression.FileSystem
$sdkDirectory = Join-Path $dependencyPath "sdk-$sdkVersion"
[IO.Directory]::CreateDirectory($sdkDirectory) | Out-Null
$archive = [IO.Compression.ZipFile]::OpenRead($package)
try {
    foreach ($item in @(
        @('lib/net462/Microsoft.Web.WebView2.Core.dll', 'Microsoft.Web.WebView2.Core.dll'),
        @('lib/net462/Microsoft.Web.WebView2.WinForms.dll', 'Microsoft.Web.WebView2.WinForms.dll'),
        @('runtimes/win-x64/native/WebView2Loader.dll', 'WebView2Loader.dll'),
        @('LICENSE.txt', 'WebView2-LICENSE.txt'),
        @('NOTICE.txt', 'WebView2-NOTICE.txt')
    )) {
        $entry = $archive.GetEntry($item[0])
        if (!$entry) { throw "WebView2 SDK 缺少组件：$($item[0])" }
        [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, (Join-Path $sdkDirectory $item[1]), $true)
    }
} finally { $archive.Dispose() }
$coreLibrary = Join-Path $sdkDirectory 'Microsoft.Web.WebView2.Core.dll'
$formsLibrary = Join-Path $sdkDirectory 'Microsoft.Web.WebView2.WinForms.dll'
$config = Get-Content -LiteralPath $settingsPath -Raw -Encoding UTF8 | ConvertFrom-Json
foreach ($field in @('Version','SourceCommit','SourceUrl','ApkUrl','ApkVersionName','GoVersion')) {
    if (![string]$config.$field) { throw "缺少发布字段：$field" }
}
foreach ($field in @('SourceSha256','ApkSha256','GoSha256','PansouSha256')) {
    if ($config.$field -notmatch '^[a-fA-F0-9]{64}$') { throw "发布校验值无效：$field" }
}
$outputPath = [IO.Path]::GetFullPath($Output)
[IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($outputPath)) | Out-Null
$iconPath = Join-Path ([IO.Path]::GetTempPath()) ("family-cinema-" + [Guid]::NewGuid().ToString('N') + '.ico')
try {
    Add-Type -AssemblyName System.Drawing
    $bitmap = New-Object Drawing.Bitmap 64,64
    $graphics = [Drawing.Graphics]::FromImage($bitmap)
    $graphics.Clear([Drawing.Color]::FromArgb(36,111,85))
    $brush = New-Object Drawing.SolidBrush ([Drawing.Color]::White)
    $font = New-Object Drawing.Font 'Segoe UI',32,([Drawing.FontStyle]::Bold)
    $graphics.DrawString('F',$font,$brush,12,6)
    $icon = [Drawing.Icon]::FromHandle($bitmap.GetHicon())
    $stream = [IO.File]::Create($iconPath)
    $icon.Save($stream)
    $stream.Dispose()
    $icon.Dispose()
    $graphics.Dispose()
    $font.Dispose()
    $brush.Dispose()
    $bitmap.Dispose()
    $sources = @(Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*.cs' | ForEach-Object FullName)
    & $compiler /nologo /target:winexe /platform:x64 /langversion:5 /optimize+ /utf8output "/out:$outputPath" "/win32manifest:$PSScriptRoot\app.manifest" "/win32icon:$iconPath" "/resource:$settingsPath,release-settings.json" "/resource:$uiPath,setup-ui.html" "/resource:$coreLibrary,webview.Microsoft.Web.WebView2.Core.dll" "/resource:$formsLibrary,webview.Microsoft.Web.WebView2.WinForms.dll" "/resource:$sdkDirectory\WebView2Loader.dll,webview.WebView2Loader.dll" "/resource:$sdkDirectory\WebView2-LICENSE.txt,webview.LICENSE.txt" "/resource:$sdkDirectory\WebView2-NOTICE.txt,webview.NOTICE.txt" "/reference:$coreLibrary" "/reference:$formsLibrary" /reference:System.dll /reference:System.Core.dll /reference:System.Drawing.dll /reference:System.Windows.Forms.dll /reference:System.Web.Extensions.dll /reference:System.IO.Compression.dll /reference:System.IO.Compression.FileSystem.dll /reference:Microsoft.CSharp.dll @sources
    if ($LASTEXITCODE -ne 0) { throw 'Windows 安装器编译失败。' }
    Get-Item -LiteralPath $outputPath | Select-Object FullName,Length
} finally {
    if (Test-Path -LiteralPath $iconPath) { Remove-Item -LiteralPath $iconPath }
}
