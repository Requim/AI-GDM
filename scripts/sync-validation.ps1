$ErrorActionPreference = "Stop"
$OutputEncoding = [Console]::OutputEncoding = [Text.UTF8Encoding]::new()

$root = Split-Path -Parent $PSScriptRoot
$configPath = Join-Path $root ".env.remote-validation"
$toolsDir = Join-Path $root ".tools"
$archive = Join-Path $toolsDir "validation-source.tar.gz"
$fileList = Join-Path $toolsDir "validation-files.txt"

function Read-ValidationConfig {
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

function Assert-ValidationConfig($values) {
    foreach ($name in "REMOTE_VALIDATION_HOST", "REMOTE_VALIDATION_KEY", "REMOTE_VALIDATION_DIR") {
        if (-not $values[$name]) { throw "远端验证配置缺少 $name" }
    }
    if (-not (Test-Path -LiteralPath $values.REMOTE_VALIDATION_KEY)) {
        throw "远端验证私钥不存在"
    }
    if ($values.REMOTE_VALIDATION_DIR -notmatch '^/home/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$') {
        throw "远端验证目录不在允许的 /home/<user>/<project> 范围"
    }
}

function New-SourceArchive {
    New-Item -ItemType Directory -Force -Path $toolsDir | Out-Null
    $files = & git -C $root ls-files --cached --others --exclude-standard
    if ($LASTEXITCODE -ne 0 -or -not $files) { throw "无法生成源码文件清单" }
    $files | Set-Content -LiteralPath $fileList -Encoding utf8NoBOM
    & tar.exe -czf $archive -C $root -T $fileList
    if ($LASTEXITCODE -ne 0) { throw "无法创建远端验证源码包" }
}

function Sync-Archive($values) {
    $sshOptions = @("-i", $values.REMOTE_VALIDATION_KEY, "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes")
    $remoteArchive = "/tmp/ai-gdm-validation.tar.gz"
    & scp @sshOptions $archive "$($values.REMOTE_VALIDATION_HOST):$remoteArchive"
    if ($LASTEXITCODE -ne 0) { throw "上传远端验证源码包失败" }

    $directory = $values.REMOTE_VALIDATION_DIR
    $command = "set -eu; base='$directory'; next='${directory}.next'; previous='${directory}.previous'; " +
        "rm -rf -- `"`$next`" `"`$previous`"; mkdir -p `"`$next`"; " +
        "tar -xzf '$remoteArchive' -C `"`$next`"; " +
        "if [ -d `"`$base`" ]; then mv `"`$base`" `"`$previous`"; fi; " +
        "mv `"`$next`" `"`$base`"; rm -rf -- `"`$previous`"; rm -f '$remoteArchive'"
    & ssh @sshOptions $values.REMOTE_VALIDATION_HOST $command
    if ($LASTEXITCODE -ne 0) { throw "替换远端验证源码失败" }
}

$config = Read-ValidationConfig
Assert-ValidationConfig $config
New-SourceArchive
Sync-Archive $config
Write-Host "远端验证源码已同步到 $($config.REMOTE_VALIDATION_DIR)"
