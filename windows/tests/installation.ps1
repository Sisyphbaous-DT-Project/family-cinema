$ErrorActionPreference = 'Stop'
$temporary = Join-Path ([IO.Path]::GetTempPath()) ("FamilyCinema-install-test-" + [Guid]::NewGuid().ToString('N'))
$root = Join-Path $temporary '程序 空格'
$media = Join-Path $temporary '影片 空格'
$packages = Join-Path $temporary 'packages'
$cache = Join-Path $root 'cache'
$savedLog = $env:FAMILY_CINEMA_SETUP_LOG
$savedPort = $env:FIXTURE_MEDIA_PORT
$lastRootKey = 'HKCU:\Software\FamilyCinema'
$lastRootExisted = Test-Path -LiteralPath $lastRootKey
$lastRootValue = (Get-ItemProperty -LiteralPath $lastRootKey -Name InstallRoot -ErrorAction SilentlyContinue).InstallRoot
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$runValue = (Get-ItemProperty -LiteralPath $runKey -Name FamilyCinema -ErrorAction SilentlyContinue).FamilyCinema
$shortcutPath = Join-Path ([Environment]::GetFolderPath('DesktopDirectory')) '家庭影院.lnk'
$shortcutBytes = if (Test-Path -LiteralPath $shortcutPath) { [IO.File]::ReadAllBytes($shortcutPath) } else { $null }
function Write-Json([string]$Path, $Value) {
    [IO.File]::WriteAllText($Path, ($Value | ConvertTo-Json -Depth 10), [Text.UTF8Encoding]::new($false))
}
function Run-Setup([string]$Exe, [string[]]$Arguments, [int]$Expected) {
    $process = Start-Process -FilePath $Exe -ArgumentList $Arguments -WindowStyle Hidden -PassThru
    if (!$process.WaitForExit(45000)) { $process.Kill(); throw '安装测试超时。' }
    if ($process.ExitCode -ne $Expected) { throw "安装测试退出码不符：$($process.ExitCode)，应为 $Expected" }
}
try {
    [IO.Directory]::CreateDirectory($packages) | Out-Null
    [IO.Directory]::CreateDirectory($root) | Out-Null
    [IO.Directory]::CreateDirectory($cache) | Out-Null
    [IO.File]::WriteAllText((Join-Path $root '.family-cinema'),'FamilyCinema Windows installation')
    $compiler = Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'
    $goSource = Join-Path $packages 'go-files\go\bin'
    [IO.Directory]::CreateDirectory($goSource) | Out-Null
    & $compiler /nologo /target:exe /platform:x64 ("/out:" + (Join-Path $goSource 'go.exe')) (Join-Path $PSScriptRoot 'Fixture.cs')
    if ($LASTEXITCODE -ne 0) { throw '编译测试夹具失败。' }
    $source = Join-Path $packages 'source-files\family-cinema-fixture\media-server'
    [IO.Directory]::CreateDirectory($source) | Out-Null
    [IO.File]::WriteAllText((Join-Path (Split-Path $source) 'LICENSE'), 'fixture')
    [IO.File]::WriteAllText((Join-Path $source 'main.go'), 'fixture')
    $panSource = Join-Path $packages 'pan-files\pansou-fixture'
    [IO.Directory]::CreateDirectory($panSource) | Out-Null
    [IO.File]::WriteAllText((Join-Path $panSource 'main.go'), 'Addr:         ":" + port,')
    [IO.File]::WriteAllText((Join-Path $panSource 'LICENSE'), 'fixture')
    $settings = @{
        Version='fixture'; SourceCommit='fixture'; SourceUrl='https://github.com/example/source/archive/fixture.zip'
        ApkUrl='https://github.com/example/source/releases/download/fixture/app.apk'; ApkVersionCode=12; ApkVersionName='fixture'; GoVersion='1.25.5'
    }
    foreach ($package in @(@('Go','go-files'), @('Source','source-files'), @('Pansou','pan-files'))) {
        $zip = Join-Path $packages ($package[0] + '.zip')
        Compress-Archive -Path (Join-Path $packages ($package[1] + '\*')) -DestinationPath $zip
        $hash = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
        $settings[$package[0] + 'Sha256'] = $hash
        Copy-Item -LiteralPath $zip -Destination (Join-Path $cache ($hash + '.zip'))
    }
    $apk = Join-Path $packages 'fixture.apk'
    [IO.File]::WriteAllText($apk, 'fixture APK for installation workflow only')
    $settings.ApkSha256 = (Get-FileHash -LiteralPath $apk -Algorithm SHA256).Hash.ToLowerInvariant()
    Copy-Item -LiteralPath $apk -Destination (Join-Path $cache ($settings.ApkSha256 + '.zip'))
    $settingsPath = Join-Path $temporary 'release-settings.json'
    Write-Json $settingsPath $settings
    $exe = Join-Path $temporary 'setup.exe'
    & (Join-Path $PSScriptRoot '..\build.ps1') -Settings $settingsPath -Output $exe | Out-Null
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,0)
    $listener.Start(); $port=$listener.LocalEndpoint.Port; $listener.Stop()
    $env:FIXTURE_MEDIA_PORT = [string]$port
    $env:FAMILY_CINEMA_SETUP_LOG = Join-Path $temporary 'setup.log'
    $installArgs = @('--install','--root',('"'+$root+'"'),'--media',('"'+$media+'"'),'--port',[string]$port,'--lan','false','--pansou','true')
    Run-Setup $exe $installArgs 0
    $statePath = Join-Path $root 'installation.json'
    $first = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
    $configPath = Join-Path $root 'config.json'
    $config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json -AsHashtable
    if (!$config.pansou.enabled -or $config.listenAddr -ne '127.0.0.1') { throw '组件或局域网配置错误。' }
    $config['custom_preserved']='keep'
    $config.anime.ruleSync=$false
    Write-Json $configPath $config
    [IO.File]::WriteAllText((Join-Path $media 'Movies\do-not-delete.txt'),'media remains')
    Run-Setup $exe $installArgs 0
    $second = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
    $config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json -AsHashtable
    if ($config.custom_preserved -ne 'keep' -or $config.anime.ruleSync -ne $false) { throw '更新覆盖了已有配置。' }
    if ($first.ActiveDirectory -eq $second.ActiveDirectory) { throw '更新没有切换程序版本目录。' }
    if (!(Test-Path -LiteralPath (Join-Path $media 'Movies\do-not-delete.txt'))) { throw '更新影响媒体文件。' }
    $settings.ApkSha256 = '0' * 64
    $settings.ApkUrl = 'http://invalid.test/app.apk'
    Write-Json $settingsPath $settings
    $badExe = Join-Path $temporary 'bad-setup.exe'
    & (Join-Path $PSScriptRoot '..\build.ps1') -Settings $settingsPath -Output $badExe | Out-Null
    Run-Setup $badExe $installArgs 1
    $afterFailure = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
    if ($afterFailure.ActiveDirectory -ne $second.ActiveDirectory) { throw '下载失败改变了活动安装。' }
    if (!(Invoke-RestMethod -Uri "http://127.0.0.1:$port/api/web-manifest" -TimeoutSec 3)) { throw '更新失败后旧服务没有继续运行。' }
    if (Get-ChildItem -LiteralPath $root -Directory -Filter 'work-*') { throw '临时工作目录未清理。' }
    'PASS: Complete cached installation, source builds, APK hosting, optional PanSou, update preservation, pre-publication failure, active process preservation, work cleanup.'
} finally {
    if (Test-Path -LiteralPath (Join-Path $root 'installation.json')) {
        try { Run-Setup $exe @('--stop','--root',('"'+$root+'"')) 0 } catch { Write-Warning $_ }
    }
    $env:FAMILY_CINEMA_SETUP_LOG=$savedLog
    $env:FIXTURE_MEDIA_PORT=$savedPort
    if ($lastRootValue) { Set-ItemProperty -LiteralPath $lastRootKey -Name InstallRoot -Value $lastRootValue }
    elseif (Test-Path -LiteralPath $lastRootKey) { Remove-ItemProperty -LiteralPath $lastRootKey -Name InstallRoot -ErrorAction SilentlyContinue }
    if (!$lastRootExisted -and (Test-Path -LiteralPath $lastRootKey)) { Remove-Item -LiteralPath $lastRootKey }
    if ($runValue) { Set-ItemProperty -LiteralPath $runKey -Name FamilyCinema -Value $runValue }
    else { Remove-ItemProperty -LiteralPath $runKey -Name FamilyCinema -ErrorAction SilentlyContinue }
    if ($shortcutBytes) { [IO.File]::WriteAllBytes($shortcutPath,$shortcutBytes) }
    elseif (Test-Path -LiteralPath $shortcutPath) { Remove-Item -LiteralPath $shortcutPath }
    Start-Sleep -Milliseconds 500
    $resolved=[IO.Path]::GetFullPath($temporary)
    $scope=[IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')+'\FamilyCinema-install-test-'
    if (!$resolved.StartsWith($scope,[StringComparison]::OrdinalIgnoreCase)) { throw '测试目录清理越界。' }
    Remove-Item -LiteralPath $resolved -Recurse
}
