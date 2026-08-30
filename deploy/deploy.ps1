[CmdletBinding()]
param(
    [string]$RuntimeEnvFile = $env:AI_GDM_RUNTIME_ENV_FILE,
    [int]$WaitSeconds = 300
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$Root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$ChecksumFile = Join-Path $Root 'SHA256SUMS'
$ManifestFile = Join-Path $Root 'manifest.json'
$ImageEnvFile = Join-Path $Root 'deploy/release-images.env'
$OfflineComposeFile = Join-Path $Root 'deploy/compose.offline.yaml'
$PostgresPasswordKey = 'POSTGRES_PASS' + 'WORD'
$RedisPasswordKey = 'REDIS_PASS' + 'WORD'
$DatabaseURLKey = 'DATABASE_' + 'URL'
$AdminTokenKey = 'APP_ADMIN_' + 'TOKEN'
$AmapKeyName = 'AMAP_API_' + 'KEY'
$BochaKeyName = 'BOCHA_API_' + 'KEY'
$LLMKeyName = 'LLM_API_' + 'KEY'
if ([string]::IsNullOrWhiteSpace($RuntimeEnvFile)) {
    $RuntimeEnvFile = Join-Path $Root 'deploy/runtime.env'
} elseif (-not [IO.Path]::IsPathRooted($RuntimeEnvFile)) {
    $RuntimeEnvFile = Join-Path $Root $RuntimeEnvFile
}
$RuntimeEnvFile = [IO.Path]::GetFullPath($RuntimeEnvFile)

function Invoke-Native {
    param([string]$Command, [string[]]$Arguments)
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Command 执行失败，退出码 $LASTEXITCODE"
    }
}

function Read-ExactEnv {
    param([string]$Path, [string[]]$ExpectedKeys)
    $values = [Collections.Generic.Dictionary[string,string]]::new([StringComparer]::Ordinal)
    $allowed = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $folded = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($expected in $ExpectedKeys) { $null = $allowed.Add($expected) }
    foreach ($line in [IO.File]::ReadAllLines($Path)) {
        if ([string]::IsNullOrWhiteSpace($line) -or $line.TrimStart().StartsWith('#')) { continue }
        $separator = $line.IndexOf('=')
        if ($separator -le 0) { throw "$Path 包含无效环境变量行" }
        $key = $line.Substring(0, $separator)
        if (-not $allowed.Contains($key) -or $values.ContainsKey($key) -or -not $folded.Add($key)) {
            throw "$Path 包含未知、重复或大小写别名键"
        }
        $values[$key] = $line.Substring($separator + 1)
    }
    foreach ($key in $ExpectedKeys) {
        if (-not $values.ContainsKey($key)) { throw "$Path 缺少 $key" }
    }
    return $values
}

function Test-PackageChecksums {
    if (-not (Test-Path -LiteralPath $ChecksumFile -PathType Leaf)) { throw '发布包缺少 SHA256SUMS' }
    $seen = @{}
    foreach ($line in [IO.File]::ReadAllLines($ChecksumFile)) {
        if ($line -notmatch '^([0-9a-f]{64})  (\./[A-Za-z0-9._/-]+)$') { throw 'SHA256SUMS 包含无效记录' }
        $relative = $Matches[2].Substring(2)
        if ($relative.Contains('..') -or $seen.ContainsKey($relative)) { throw 'SHA256SUMS 路径无效或重复' }
        $seen[$relative] = $true
        $path = Join-Path $Root ($relative.Replace('/', [IO.Path]::DirectorySeparatorChar))
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "发布包文件缺失: $relative" }
        $actual = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actual -ne $Matches[1]) { throw "发布包 SHA-256 不一致: $relative" }
    }
}

function New-HexSecret {
    $bytes = New-Object byte[] 32
    $generator = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $generator.GetBytes($bytes) } finally { $generator.Dispose() }
    return ([BitConverter]::ToString($bytes)).Replace('-', '').ToLowerInvariant()
}

function Test-SafeAtom {
    param([string]$Value, [string]$Name)
    if ($Value -notmatch '^[A-Za-z0-9._~:/@+=-]*$') { throw "$Name 包含不安全字符" }
}

function ConvertTo-RuntimePort {
    param([string]$Value)
    $parsed = 0
    if (-not [int]::TryParse($Value, [ref]$parsed) -or $parsed -lt 1 -or $parsed -gt 65535) {
        throw 'AI_GDM_HTTP_PORT 超出允许范围'
    }
    return $parsed
}

function Test-ProjectName {
    param([string]$Value)
    if ($Value -notmatch '^[a-z0-9][a-z0-9_-]{0,62}$') {
        throw 'AI_GDM_PROJECT_NAME 必须是小写字母、数字、连字符或下划线'
    }
}

function Read-ProcessEnvironment {
    param([string]$Name)
    $value = [Environment]::GetEnvironmentVariable($Name)
    if ($null -eq $value) { return '' }
    return [string]$value
}

function Protect-RuntimeFile {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { return }
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    $acl = New-Object Security.AccessControl.FileSecurity
    $rule = New-Object Security.AccessControl.FileSystemAccessRule($identity, 'FullControl', 'Allow')
    $acl.SetAccessRuleProtection($true, $false)
    $acl.AddAccessRule($rule)
    Set-Acl -LiteralPath $RuntimeEnvFile -AclObject $acl
}

function Write-RuntimeEnvironment {
    $postgres = New-HexSecret
    $redis = New-HexSecret
    $admin = New-HexSecret
    if (($postgres -eq $redis) -or ($postgres -eq $admin) -or ($redis -eq $admin)) { throw '生成的运行密钥不得重复' }
    $amap = Read-ProcessEnvironment -Name $AmapKeyName
    $bocha = Read-ProcessEnvironment -Name $BochaKeyName
    $llm = Read-ProcessEnvironment -Name $LLMKeyName
    $llmUrl = if ([string]::IsNullOrWhiteSpace($env:LLM_BASE_URL)) { 'https://jojocode.com/v1/chat/completions' } else { $env:LLM_BASE_URL }
    $llmModel = if ([string]::IsNullOrWhiteSpace($env:LLM_MODEL)) { 'gpt-5.6-terra' } else { $env:LLM_MODEL }
    foreach ($entry in @(@($amap, $AmapKeyName), @($bocha, $BochaKeyName), @($llm, $LLMKeyName), @($llmUrl, 'LLM_BASE_URL'), @($llmModel, 'LLM_MODEL'))) {
        Test-SafeAtom -Value $entry[0] -Name $entry[1]
    }
    if (-not $llmUrl.StartsWith('https://', [StringComparison]::Ordinal)) { throw 'LLM_BASE_URL 必须使用 HTTPS' }
    $project = if ([string]::IsNullOrWhiteSpace($env:AI_GDM_PROJECT_NAME)) { 'ai-gdm' } else { $env:AI_GDM_PROJECT_NAME }
    $bind = if ([string]::IsNullOrWhiteSpace($env:AI_GDM_BIND_ADDRESS)) { '0.0.0.0' } else { $env:AI_GDM_BIND_ADDRESS }
    $port = if ([string]::IsNullOrWhiteSpace($env:AI_GDM_HTTP_PORT)) { '8080' } else { $env:AI_GDM_HTTP_PORT }
    $refreshEnabled = if ([string]::IsNullOrWhiteSpace($env:REFRESH_ENABLED)) { 'true' } else { $env:REFRESH_ENABLED }
    Test-ProjectName -Value $project
    Test-SafeAtom -Value $bind -Name 'AI_GDM_BIND_ADDRESS'
    $null = ConvertTo-RuntimePort -Value $port
    if ($refreshEnabled -notin @('true', 'false')) { throw 'REFRESH_ENABLED 必须是 true 或 false' }
    $lines = @(
        "AI_GDM_PROJECT_NAME=$project", "AI_GDM_BIND_ADDRESS=$bind", "AI_GDM_HTTP_PORT=$port",
        "$PostgresPasswordKey=$postgres", "$RedisPasswordKey=$redis",
        "$DatabaseURLKey=postgresql://ai_gdm:$postgres@postgres:5432/ai_gdm?sslmode=disable",
        'APP_ENV=production', 'APP_LOG_LEVEL=info', 'APP_SHUTDOWN_TIMEOUT=30s', "$AdminTokenKey=$admin",
        'APP_RATE_LIMIT_PER_MINUTE=120', 'APP_RATE_LIMIT_BURST=30', "REFRESH_ENABLED=$refreshEnabled",
        'REFRESH_INTERVAL=30m', 'REFRESH_TIMEOUT=10m',
        'OPEN_METEO_POINTS=104.066500,30.572300;102.712300,25.040600',
        'OPEN_METEO_PAST_HOURS=72', 'OPEN_METEO_FORECAST_HOURS=24', 'OPEN_METEO_FALLBACK_MAX_AGE=6h',
        'LHASA_STALE_AFTER=12h', "AMAP_ENABLED=$((-not [string]::IsNullOrEmpty($amap)).ToString().ToLowerInvariant())",
        "$AmapKeyName=$amap", "BOCHA_ENABLED=$((-not [string]::IsNullOrEmpty($bocha)).ToString().ToLowerInvariant())",
        "$BochaKeyName=$bocha", "LLM_ENABLED=$((-not [string]::IsNullOrEmpty($llm)).ToString().ToLowerInvariant())",
        'LLM_PROVIDER_NAME=OpenAI-compatible', "LLM_BASE_URL=$llmUrl", "$LLMKeyName=$llm",
        "LLM_MODEL=$llmModel", 'LLM_MAX_COMPLETION_TOKENS=1200', 'LLM_OUTPUT_ATTEMPTS=2'
    )
    $directory = Split-Path -Parent $RuntimeEnvFile
    [IO.Directory]::CreateDirectory($directory) | Out-Null
    $temporary = "$RuntimeEnvFile.tmp.$PID"
    [IO.File]::WriteAllText($temporary, (($lines -join "`n") + "`n"), (New-Object Text.UTF8Encoding($false)))
    Move-Item -LiteralPath $temporary -Destination $RuntimeEnvFile -Force
    Protect-RuntimeFile
}

function Test-RuntimeEnvironment {
    if (-not (Test-Path -LiteralPath $RuntimeEnvFile -PathType Leaf)) { throw '运行配置必须是普通文件' }
    if ((Get-Item -LiteralPath $RuntimeEnvFile).Length -gt 65536) { throw '运行配置超过 64 KiB' }
    $required = @($PostgresPasswordKey, $RedisPasswordKey, $DatabaseURLKey, $AdminTokenKey, 'APP_ENV',
        'AI_GDM_PROJECT_NAME', 'AI_GDM_BIND_ADDRESS', 'AI_GDM_HTTP_PORT')
    $values = Read-ExactEnvSubset -Path $RuntimeEnvFile -RequiredKeys $required
    foreach ($key in $required[0..3]) { if ([string]::IsNullOrEmpty($values[$key])) { throw "$key 不得为空" } }
    if ($values['APP_ENV'] -ne 'production') { throw 'APP_ENV 必须是 production' }
    Test-ProjectName -Value $values['AI_GDM_PROJECT_NAME']
    Test-SafeAtom -Value $values['AI_GDM_BIND_ADDRESS'] -Name 'AI_GDM_BIND_ADDRESS'
    $null = ConvertTo-RuntimePort -Value $values['AI_GDM_HTTP_PORT']
    Protect-RuntimeFile
    return $values
}

function Get-RuntimePort {
    param([Collections.Generic.IDictionary[string,string]]$Runtime)
    return ConvertTo-RuntimePort -Value $Runtime['AI_GDM_HTTP_PORT']
}

function Read-ExactEnvSubset {
    param([string]$Path, [string[]]$RequiredKeys)
    $values = [Collections.Generic.Dictionary[string,string]]::new([StringComparer]::Ordinal)
    $folded = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($line in [IO.File]::ReadAllLines($Path)) {
        if ([string]::IsNullOrWhiteSpace($line) -or $line.TrimStart().StartsWith('#')) { continue }
        $separator = $line.IndexOf('=')
        if ($separator -le 0) { throw "$Path 包含无效环境变量行" }
        $key = $line.Substring(0, $separator)
        if ($values.ContainsKey($key) -or -not $folded.Add($key)) { throw "$Path 包含重复或大小写别名键 $key" }
        $values[$key] = $line.Substring($separator + 1)
    }
    foreach ($key in $RequiredKeys) { if (-not $values.ContainsKey($key)) { throw "$Path 缺少 $key" } }
    return $values
}

function Test-LoadedImages {
    param([Collections.Generic.IDictionary[string,string]]$Images, [object]$Manifest)
    foreach ($key in $Images.Keys) {
        $reference = $Images[$key]
        $entry = @($Manifest.images | Where-Object { $_.reference -eq $reference })
        if ($entry.Count -ne 1) { throw "manifest 缺少唯一镜像标识: $reference" }
        $id = (& docker image inspect -f '{{.Id}}' $reference 2>$null)
        if ($LASTEXITCODE -ne 0 -or $id -ne $entry[0].id) { throw "离线镜像标识不一致: $reference" }
        $platform = (& docker image inspect -f '{{.Os}}/{{.Architecture}}' $reference 2>$null)
        if ($LASTEXITCODE -ne 0 -or $platform -ne 'linux/amd64') { throw "离线镜像平台无效: $reference" }
    }
}

function Invoke-Compose {
    param([string]$ProjectName, [string[]]$Arguments)
    $base = @('compose', '--project-name', $ProjectName, '--project-directory', $Root,
        '--env-file', $RuntimeEnvFile, '--env-file', $ImageEnvFile,
        '-f', (Join-Path $Root 'compose.yaml'), '-f', $OfflineComposeFile)
    Invoke-Native -Command 'docker' -Arguments ($base + $Arguments)
}

function Test-ComposeImages {
    param([string]$ProjectName, [Collections.Generic.IDictionary[string,string]]$Images)
    $base = @('compose', '--project-name', $ProjectName, '--project-directory', $Root,
        '--env-file', $RuntimeEnvFile, '--env-file', $ImageEnvFile,
        '-f', (Join-Path $Root 'compose.yaml'), '-f', $OfflineComposeFile, 'config', '--images')
    $actual = @(& docker @base | Sort-Object)
    if ($LASTEXITCODE -ne 0) { throw '读取 Compose 镜像清单失败' }
    $expected = @($Images.Values | Sort-Object)
    if ($actual.Count -ne $expected.Count -or (Compare-Object $actual $expected)) {
        throw 'Compose 未精确绑定三个离线发布镜像'
    }
}

function Wait-Service {
    param([int]$Port)
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        try {
            Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 -Uri "http://127.0.0.1:$Port/healthz" | Out-Null
            Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 -Uri "http://127.0.0.1:$Port/readyz" | Out-Null
            $page = Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 -Uri "http://127.0.0.1:$Port/"
            if ($page.Content -notlike '*AI-GDM 地质灾害辅助研判控制台*') { throw '控制台页面内容无效' }
            Write-Output "AI-GDM 已部署：http://127.0.0.1:$Port/"
            return
        } catch {
            Start-Sleep -Seconds 2
        }
    }
    throw '服务启动后未在规定时间内通过 HTTP 探针'
}

if ($WaitSeconds -lt 30 -or $WaitSeconds -gt 1800) { throw 'WaitSeconds 超出允许范围' }
Get-Command docker -ErrorAction Stop | Out-Null
Test-PackageChecksums
$images = Read-ExactEnv -Path $ImageEnvFile -ExpectedKeys @('AI_GDM_IMAGE', 'AI_GDM_POSTGIS_IMAGE', 'AI_GDM_REDIS_IMAGE')
$manifest = Get-Content -LiteralPath $ManifestFile -Raw | ConvertFrom-Json
if (-not (Test-Path -LiteralPath $RuntimeEnvFile)) { Write-RuntimeEnvironment }
$runtime = Test-RuntimeEnvironment
$port = Get-RuntimePort -Runtime $runtime
$projectName = $runtime['AI_GDM_PROJECT_NAME']
Invoke-Native -Command 'docker' -Arguments @('compose', 'version')
$engine = (& docker info -f '{{.OSType}}' 2>$null)
if ($LASTEXITCODE -ne 0 -or $engine -ne 'linux') { throw 'Docker Desktop 必须切换为 Linux containers' }
Invoke-Native -Command 'docker' -Arguments @('load', '-i', (Join-Path $Root 'images/ai-gdm-images-linux-amd64.tar'))
Test-LoadedImages -Images $images -Manifest $manifest
$env:AI_GDM_RUNTIME_ENV_FILE = $RuntimeEnvFile
$env:AI_GDM_IMAGE = $images['AI_GDM_IMAGE']
$env:AI_GDM_POSTGIS_IMAGE = $images['AI_GDM_POSTGIS_IMAGE']
$env:AI_GDM_REDIS_IMAGE = $images['AI_GDM_REDIS_IMAGE']
$env:AI_GDM_PROJECT_NAME = $projectName
$env:AI_GDM_BIND_ADDRESS = $runtime['AI_GDM_BIND_ADDRESS']
$env:AI_GDM_HTTP_PORT = [string]$port
$env:COMPOSE_PROJECT_NAME = $projectName
[Environment]::SetEnvironmentVariable($PostgresPasswordKey, $runtime[$PostgresPasswordKey], 'Process')
[Environment]::SetEnvironmentVariable($RedisPasswordKey, $runtime[$RedisPasswordKey], 'Process')
Invoke-Compose -ProjectName $projectName -Arguments @('config', '--quiet')
Test-ComposeImages -ProjectName $projectName -Images $images
Invoke-Compose -ProjectName $projectName -Arguments @('up', '-d', '--wait', '--wait-timeout', [string]$WaitSeconds, '--pull', 'never', '--no-build', '--remove-orphans')
Wait-Service -Port $port
Write-Output "运行配置：$RuntimeEnvFile（部署脚本未输出密钥）"
