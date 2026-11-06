[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$OutputDirectory,
    [string]$SigningConfig = (Join-Path $env:USERPROFILE '.family-cinema\signing\release-signing.json'),
    [string]$JavaHome = $env:JAVA_HOME,
    [string]$AndroidHome = $env:ANDROID_HOME,
    [string]$Gradle = 'gradle',
    [switch]$CreateSigningKey,
    [switch]$Offline
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$project = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\android'))
$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$SigningConfig = [IO.Path]::GetFullPath($SigningConfig)
if ($SigningConfig.StartsWith($repoRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
    throw '签名配置必须保存在仓库以外。'
}
if (!$JavaHome -or !(Test-Path -LiteralPath (Join-Path $JavaHome 'bin\java.exe'))) {
    throw '请通过 -JavaHome 或 JAVA_HOME 指定 JDK 17。'
}
if (!$AndroidHome -or !(Test-Path -LiteralPath (Join-Path $AndroidHome 'platforms\android-34\android.jar'))) {
    throw '请通过 -AndroidHome 或 ANDROID_HOME 指定包含 Android SDK 34 的目录。'
}
$keytool = Join-Path $JavaHome 'bin\keytool.exe'
$buildTools = Join-Path $AndroidHome 'build-tools\34.0.0'
$apksigner = Join-Path $buildTools 'apksigner.bat'
if (!(Test-Path -LiteralPath $apksigner)) {
    throw 'Android SDK 缺少 build-tools 34.0.0。'
}
$null = Get-Command $Gradle -ErrorAction Stop
$savedEnvironment = @{}
$environmentNames = @('JAVA_HOME', 'ANDROID_HOME', 'JAVA_TOOL_OPTIONS', 'TEMP', 'TMP',
    'FAMILY_CINEMA_KEYSTORE', 'FAMILY_CINEMA_STORE_PASSWORD',
    'FAMILY_CINEMA_KEY_ALIAS', 'FAMILY_CINEMA_KEY_PASSWORD')
foreach ($name in $environmentNames) {
    $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}

function Protect-SigningDirectory([string]$Path) {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent().User
    $system = New-Object Security.Principal.SecurityIdentifier('S-1-5-18')
    $acl = New-Object Security.AccessControl.DirectorySecurity
    $acl.SetOwner($identity)
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($sid in @($identity, $system)) {
        $rule = New-Object Security.AccessControl.FileSystemAccessRule(
            $sid, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
        $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}

$temporaryDirectory = $null
try {
    $env:JAVA_HOME = $JavaHome
    $env:ANDROID_HOME = $AndroidHome
    if ($CreateSigningKey) {
        if (Test-Path -LiteralPath $SigningConfig) {
            throw '签名配置已存在，拒绝生成新钥匙。请沿用原配置。'
        }
        $signingDirectory = Split-Path -Parent $SigningConfig
        $null = New-Item -ItemType Directory -Path $signingDirectory -Force
        Protect-SigningDirectory $signingDirectory
        $keystore = Join-Path $signingDirectory 'family-cinema-release.jks'
        if (Test-Path -LiteralPath $keystore) {
            throw '签名密钥已存在，拒绝覆盖。'
        }
        $bytes = New-Object byte[] 32
        $random = [Security.Cryptography.RandomNumberGenerator]::Create()
        try { $random.GetBytes($bytes) } finally { $random.Dispose() }
        $password = [Convert]::ToBase64String($bytes)
        $env:FAMILY_CINEMA_STORE_PASSWORD = $password
        $env:FAMILY_CINEMA_KEY_PASSWORD = $password
        & $keytool -genkeypair -keystore $keystore -storetype JKS `
            -alias family-cinema-release -keyalg RSA -keysize 4096 -validity 10000 `
            -dname 'CN=Family Cinema, O=Family Cinema Project' `
            -storepass:env FAMILY_CINEMA_STORE_PASSWORD -keypass:env FAMILY_CINEMA_KEY_PASSWORD
        if ($LASTEXITCODE -ne 0) { throw '生成发布签名失败。' }
        $credentials = [ordered]@{
            keystore = $keystore
            storePassword = $password
            keyAlias = 'family-cinema-release'
            keyPassword = $password
        }
        [IO.File]::WriteAllText($SigningConfig, ($credentials | ConvertTo-Json),
            (New-Object Text.UTF8Encoding($false)))
        Write-Host '已建立固定发布签名。请将整个签名目录另行妥善保管，后续不要重新生成。'
    }
    if (!(Test-Path -LiteralPath $SigningConfig)) {
        throw '找不到外部签名配置。首次发布可以显式指定 -CreateSigningKey。'
    }
    $signing = Get-Content -LiteralPath $SigningConfig -Raw -Encoding UTF8 | ConvertFrom-Json
    foreach ($field in @('keystore', 'storePassword', 'keyAlias', 'keyPassword')) {
        if (!$signing.PSObject.Properties[$field] -or [string]::IsNullOrWhiteSpace($signing.$field)) {
            throw "签名配置缺少字段：$field"
        }
    }
    $keystore = [IO.Path]::GetFullPath($signing.keystore)
    if ($keystore.StartsWith($repoRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw '发布密钥必须保存在仓库以外。'
    }
    if (!(Test-Path -LiteralPath $keystore)) { throw '找不到签名配置指定的密钥文件。' }
    $env:FAMILY_CINEMA_KEYSTORE = $keystore
    $env:FAMILY_CINEMA_STORE_PASSWORD = $signing.storePassword
    $env:FAMILY_CINEMA_KEY_ALIAS = $signing.keyAlias
    $env:FAMILY_CINEMA_KEY_PASSWORD = $signing.keyPassword

    # 使用独立临时目录，不修改系统或用户的 Gradle 配置。
    $temporaryDirectory = Join-Path ([IO.Path]::GetTempPath()) ('fc-apk-' + [Guid]::NewGuid().ToString('N').Substring(0, 6))
    $null = New-Item -ItemType Directory -Path $temporaryDirectory
    $env:TEMP = $temporaryDirectory
    $env:TMP = $temporaryDirectory
    $javaTemp = $temporaryDirectory.Replace('\', '/')
    # 部分 Windows 环境的 AF_UNIX 连接失败，让 JDK 自动回退到本机 TCP。
    # 指向不存在的子目录只影响本次 JVM，不创建该目录，也不改变系统配置。
    $env:JAVA_TOOL_OPTIONS = ('{0} "-Djava.io.tmpdir={1}" "-Djdk.net.unixdomain.tmpdir={1}/tcp-fallback"' -f
        $savedEnvironment['JAVA_TOOL_OPTIONS'], $javaTemp).Trim()
    $gradleArguments = @('--no-daemon', '--console=plain',
        '-Pkotlin.compiler.execution.strategy=in-process', '-p', $project, 'assembleRelease')
    if ($Offline) { $gradleArguments = @('--offline') + $gradleArguments }
    & $Gradle @gradleArguments
    if ($LASTEXITCODE -ne 0) { throw 'APK 正式构建失败。未输出发布包。' }

    $apkDirectory = Join-Path $project 'app\build\outputs\apk\release'
    $metadata = Get-Content -LiteralPath (Join-Path $apkDirectory 'output-metadata.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($metadata.applicationId -ne 'com.family.cinema' -or $metadata.elements.Count -ne 1) {
        throw 'APK 构建元数据与预期不符。'
    }
    $artifact = $metadata.elements[0]
    $sourceApk = [IO.Path]::GetFullPath((Join-Path $apkDirectory $artifact.outputFile))
    if (!$sourceApk.StartsWith($apkDirectory + '\', [StringComparison]::OrdinalIgnoreCase)) {
        throw 'APK 输出路径不在构建目录内。'
    }
    $previousErrorPreference = $ErrorActionPreference
    try {
        # Windows PowerShell 5.1 将原生命令 stderr 包成错误记录，真正成功与否以退出码为准。
        $ErrorActionPreference = 'Continue'
        $verification = @(& $apksigner verify --verbose --print-certs $sourceApk 2>&1)
        $verificationExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorPreference
    }
    if ($verificationExit -ne 0) { throw 'APK 签名验证失败。' }
    if ($verification -match 'CN=Android Debug') { throw '拒绝发布使用调试签名的 APK。' }
    $certificate = ($verification | Select-String 'Signer #1 certificate SHA-256 digest:').Line
    if (!$certificate) { throw '没有取得 APK 签名证书摘要。' }
    $certificate = ($certificate -split ': ', 2)[1].Trim().ToLowerInvariant()
    if ($certificate -notmatch '^[0-9a-f]{64}$') { throw 'APK 签名证书摘要格式错误。' }
    $OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
    $null = New-Item -ItemType Directory -Path $OutputDirectory -Force
    $filename = 'family-cinema-android-' + $artifact.versionName + '.apk'
    $destination = Join-Path $OutputDirectory $filename
    Copy-Item -LiteralPath $sourceApk -Destination $destination -Force
    $hash = (Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash.ToLowerInvariant()
    $info = [ordered]@{
        applicationId = $metadata.applicationId
        versionCode = [int]$artifact.versionCode
        versionName = [string]$artifact.versionName
        fileName = $filename
        sizeBytes = (Get-Item -LiteralPath $destination).Length
        sha256 = $hash
        certificateSha256 = $certificate
    }
    [IO.File]::WriteAllText((Join-Path $OutputDirectory 'app-version.json'), ($info | ConvertTo-Json),
        (New-Object Text.UTF8Encoding($false)))
    Write-Host "已生成并验证：$destination"
    Write-Host "版本：$($info.versionName)（$($info.versionCode)）"
    Write-Host "SHA256：$hash"
    Write-Host "签名证书 SHA256：$certificate"
} finally {
    foreach ($name in $environmentNames) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], 'Process')
    }
    if ($temporaryDirectory -and (Test-Path -LiteralPath $temporaryDirectory)) {
        $resolved = (Resolve-Path -LiteralPath $temporaryDirectory).ProviderPath
        $tempParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')
        if ((Split-Path -Parent $resolved) -eq $tempParent -and (Split-Path -Leaf $resolved) -match '^fc-apk-[0-9a-f]{6}$') {
            # JVM 退出后原生 DLL 的文件句柄可能稍晚释放，避免清理错误掩盖构建结果。
            for ($attempt = 0; $attempt -lt 8; $attempt++) {
                try {
                    Remove-Item -LiteralPath $resolved -Recurse -ErrorAction Stop
                    break
                } catch {
                    if ($attempt -eq 7) {
                        Write-Warning "临时目录仍被构建进程占用，请在进程退出后清理：$resolved"
                    } else {
                        Start-Sleep -Milliseconds 500
                    }
                }
            }
        }
    }
}
