param([Parameter(Mandatory = $true)][string]$Exe)
$ErrorActionPreference = 'Stop'
$originalMediaPort = $env:FIXTURE_MEDIA_PORT
$originalLog = $env:FAMILY_CINEMA_SETUP_LOG
$temporary = Join-Path ([IO.Path]::GetTempPath()) ("FamilyCinema-process-test-" + [Guid]::NewGuid().ToString('N'))
$media = Join-Path $temporary 'media'
$root = Join-Path $temporary '安装 空格'
$version = Join-Path $root 'versions\fixture'
[IO.Directory]::CreateDirectory($version) | Out-Null
$manager = Join-Path $root 'FamilyCinema.exe'
Copy-Item -LiteralPath $Exe -Destination $manager
function Get-FreePort {
    $socket = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $socket.Start()
    try { return $socket.LocalEndpoint.Port } finally { $socket.Stop() }
}
function Run-Manager([string]$Operation) {
    $process = Start-Process -FilePath $manager -ArgumentList @($Operation, '--root', ('"' + $root + '"')) -WindowStyle Hidden -PassThru
    if (!$process.WaitForExit(30000)) { $process.Kill(); throw '后台生命周期测试超时。' }
    if ($process.ExitCode -ne 0) { throw "管理器操作失败：$Operation" }
}
try {
    $compiler = Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'
    & $compiler /nologo /target:exe /platform:x64 ("/out:" + (Join-Path $version 'media-server.exe')) (Join-Path $PSScriptRoot 'Fixture.cs')
    if ($LASTEXITCODE -ne 0) { throw '测试服务编译失败。' }
    Copy-Item -LiteralPath (Join-Path $version 'media-server.exe') -Destination (Join-Path $version 'pansou.exe')
    $mainPort = Get-FreePort
    $panPort = Get-FreePort
    $env:FIXTURE_MEDIA_PORT = [string]$mainPort
    $env:FAMILY_CINEMA_SETUP_LOG = Join-Path $temporary 'manager.log'
    $state = @{
        Version = 'fixture'; ActiveDirectory = $version; ManagerPath = $manager
        Options = @{ Root=$root; Media=$media; Pansou=$true; FFmpeg=$false; Lan=$false; AutoStart=$false; Port=$mainPort; PansouPort=$panPort }
    }
    [IO.File]::WriteAllText((Join-Path $root 'installation.json'), ($state | ConvertTo-Json -Depth 5), [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText((Join-Path $root 'config.json'), '{}', [Text.UTF8Encoding]::new($false))
    Run-Manager '--self-test'
    Run-Manager '--start'
    $main = Invoke-RestMethod -Uri "http://127.0.0.1:$mainPort/api/web-manifest" -TimeoutSec 3
    $pan = Invoke-RestMethod -Uri "http://127.0.0.1:$panPort/api/health" -TimeoutSec 3
    if ($main.status -ne 'ok' -or $pan.status -ne 'ok') { throw '健康检查失败。' }
    $firstPid = (Get-Content -LiteralPath (Join-Path $root 'run\media-server.json') -Raw | ConvertFrom-Json).Pid
    Run-Manager '--start'
    if ((Get-Content -LiteralPath (Join-Path $root 'run\media-server.json') -Raw | ConvertFrom-Json).Pid -ne $firstPid) { throw '重复启动创建了新进程。' }
    $panPid = (Get-Content -LiteralPath (Join-Path $root 'run\pansou.json') -Raw | ConvertFrom-Json).Pid
    Stop-Process -Id $panPid
    Start-Sleep -Milliseconds 300
    Run-Manager '--start'
    if ((Get-Content -LiteralPath (Join-Path $root 'run\media-server.json') -Raw | ConvertFrom-Json).Pid -ne $firstPid) { throw '补启动盘搜影响了主服务。' }
    if (!(Invoke-RestMethod -Uri "http://127.0.0.1:$panPort/api/health" -TimeoutSec 3)) { throw '盘搜补启动失败。' }
    Run-Manager '--stop'
    Start-Sleep -Milliseconds 500
    if (Get-NetTCPConnection -State Listen -LocalPort $mainPort,$panPort -ErrorAction SilentlyContinue) { throw '停止后端口仍在监听。' }
    if (!(Select-String -LiteralPath (Join-Path $root 'logs\media-server.log') -Pattern 'fixture started' -Quiet)) { throw '后台日志未保存。' }
    'PASS: Chinese/spaced paths, independent log hosts, health checks, repeat start, stop, process identities, logs, self-tests.'
} finally {
    if (Test-Path -LiteralPath (Join-Path $root 'installation.json')) { try { Run-Manager '--stop' } catch { Write-Warning $_ } }
    $env:FIXTURE_MEDIA_PORT = $originalMediaPort
    $env:FAMILY_CINEMA_SETUP_LOG = $originalLog
    Start-Sleep -Milliseconds 300
    $resolved = [IO.Path]::GetFullPath($temporary)
    $scope = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\FamilyCinema-process-test-'
    if (!$resolved.StartsWith($scope, [StringComparison]::OrdinalIgnoreCase)) { throw '测试清理目录越界。' }
    Remove-Item -LiteralPath $resolved -Recurse
}
