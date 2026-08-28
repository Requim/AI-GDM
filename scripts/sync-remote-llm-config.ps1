$ErrorActionPreference = "Stop"
$OutputEncoding = [Console]::OutputEncoding = [Text.UTF8Encoding]::new()

$root = Split-Path -Parent $PSScriptRoot
$configPath = Join-Path $root ".env.remote-validation"
$toolsDir = Join-Path $root ".tools"
$temporaryConfig = Join-Path $toolsDir "remote-llm-runtime.env"
$llmNames = @(
    "LLM_ENABLED",
    "LLM_PROVIDER_NAME",
    "LLM_BASE_URL",
    "LLM_API_KEY",
    "LLM_MODEL",
    "LLM_MAX_COMPLETION_TOKENS",
    "LLM_OUTPUT_ATTEMPTS"
)

function Read-PrivateConfig {
    if (-not (Test-Path -LiteralPath $configPath)) {
        throw "缺少本机远端验证配置：$configPath"
    }
    $values = @{}
    foreach ($line in Get-Content -LiteralPath $configPath -Encoding utf8) {
        if ($line -match '^\s*(?:#|$)') { continue }
        $name, $value = $line -split '=', 2
        $values[$name.Trim()] = $value.Trim()
    }
    return $values
}

function Assert-PrivateConfig($values) {
    foreach ($name in @("REMOTE_VALIDATION_HOST", "REMOTE_VALIDATION_KEY", "REMOTE_LLM_ENV") + $llmNames) {
        if (-not $values[$name]) { throw "本机私密配置缺少 $name" }
    }
    if (-not (Test-Path -LiteralPath $values.REMOTE_VALIDATION_KEY)) {
        throw "远端验证私钥不存在"
    }
    if ($values.REMOTE_VALIDATION_HOST -notmatch '^[A-Za-z0-9._-]+@[A-Za-z0-9.:-]+$') {
        throw "远端验证主机格式无效"
    }
    if ($values.REMOTE_LLM_ENV -notmatch '^/home/[A-Za-z0-9._-]+/\.config/ai-gdm/[A-Za-z0-9._-]+$') {
        throw "远端 LLM 配置不在允许的用户配置目录"
    }
}

function ConvertTo-ShellAssignment([string]$name, [string]$value) {
    if ($value.IndexOfAny([char[]]@("`r", "`n", [char]0)) -ge 0) {
        throw "$name 包含不允许的换行或空字符"
    }
    $escaped = $value.Replace('\', '\\').Replace('"', '\"').Replace('$', '\$').Replace('`', '\`')
    return "$name=`"$escaped`""
}

function Write-TemporaryConfig($values) {
    New-Item -ItemType Directory -Force -Path $toolsDir | Out-Null
    $lines = foreach ($name in $llmNames) {
        ConvertTo-ShellAssignment $name $values[$name]
    }
    [IO.File]::WriteAllText(
        $temporaryConfig,
        ($lines -join "`n") + "`n",
        [Text.UTF8Encoding]::new($false)
    )
}

function Sync-PrivateConfig($values) {
    $sshOptions = @(
        "-i", $values.REMOTE_VALIDATION_KEY,
        "-o", "BatchMode=yes",
        "-o", "IdentitiesOnly=yes",
        "-o", "StrictHostKeyChecking=yes"
    )
    $remoteFile = $values.REMOTE_LLM_ENV
    $remoteDirectory = $remoteFile.Substring(0, $remoteFile.LastIndexOf('/'))
    $nextFile = "$remoteFile.next"
    $prepare = "set -eu; umask 077; install -d -m 700 '$remoteDirectory'; rm -f '$nextFile'"
    & ssh @sshOptions $values.REMOTE_VALIDATION_HOST $prepare
    if ($LASTEXITCODE -ne 0) { throw "准备远端 LLM 配置目录失败" }

    & scp @sshOptions $temporaryConfig "$($values.REMOTE_VALIDATION_HOST):$nextFile"
    if ($LASTEXITCODE -ne 0) { throw "上传远端 LLM 配置失败" }

    $commit = "set -eu; chmod 600 '$nextFile'; mv -f '$nextFile' '$remoteFile'; test `"`$(stat -c %a '$remoteFile')`" = 600"
    & ssh @sshOptions $values.REMOTE_VALIDATION_HOST $commit
    if ($LASTEXITCODE -ne 0) { throw "提交远端 LLM 配置失败" }
}

$config = Read-PrivateConfig
Assert-PrivateConfig $config
try {
    Write-TemporaryConfig $config
    Sync-PrivateConfig $config
} finally {
    Remove-Item -LiteralPath $temporaryConfig -Force -ErrorAction SilentlyContinue
}
Write-Host "远端 LLM 运行配置已同步，密钥内容未输出"
