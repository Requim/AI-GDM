$ErrorActionPreference = "Stop"
$OutputEncoding = [Console]::OutputEncoding = [Text.UTF8Encoding]::new()

$root = Split-Path -Parent $PSScriptRoot
$localGo = Join-Path $root ".tools\go\bin\go.exe"
$go = if (Test-Path -LiteralPath $localGo) { $localGo } else { "go" }
$gofmt = if (Test-Path -LiteralPath $localGo) {
    Join-Path (Split-Path -Parent $localGo) "gofmt.exe"
} else {
    "gofmt"
}

Push-Location $root
try {
    $goFiles = Get-ChildItem -Path $root -Recurse -Filter "*.go" -File |
        Where-Object {
            $_.FullName -notlike "$root\.tools\*" -and
            $_.FullName -notlike "$root\.git\*"
        }
    $unformatted = if ($goFiles) { & $gofmt -l $goFiles.FullName } else { @() }
    if ($unformatted) {
        throw "发现未格式化的 Go 文件：$($unformatted -join ', ')"
    }

    $buildDir = Join-Path $root ".tools\build"
    New-Item -ItemType Directory -Force -Path $buildDir | Out-Null
    & $go build -o "$buildDir\" ./...
    & $go test ./...
    & $go vet ./...
} finally {
    Pop-Location
}
