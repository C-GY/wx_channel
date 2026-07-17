[CmdletBinding()]
param(
    [ValidateSet('run', 'build', 'test')]
    [string]$Action = 'run',

    [string]$Output = '.tmp_runtime\wx_channel.exe',

    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$AppArgs
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot

function Assert-Command {
    param([Parameter(Mandatory = $true)][string]$Name)

    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Required command not found on PATH: $Name"
    }
}

function Ensure-SunnyNetRuntimeFiles {
    $files = @(
        [pscustomobject]@{
            RelativePath = 'Resource\nfapi\dll\win32\nfapi.dll'
            TargetPath = 'pkg\sunnynet\Resource\nfapi\dll\win32\nfapi.dll'
        },
        [pscustomobject]@{
            RelativePath = 'Resource\nfapi\dll\x64\nfapi.dll'
            TargetPath = 'pkg\sunnynet\Resource\nfapi\dll\x64\nfapi.dll'
        }
    )

    $missing = @($files | Where-Object { -not (Test-Path -LiteralPath (Join-Path $repoRoot $_.TargetPath)) })
    if ($missing.Count -eq 0) {
        return
    }

    Write-Host '==> Restoring pinned SunnyNet v1.0.3 runtime files' -ForegroundColor Cyan
    $module = go mod download -json github.com/qtgolang/SunnyNet@v1.0.3 | ConvertFrom-Json
    if (-not $module.Dir) {
        throw 'Could not resolve SunnyNet v1.0.3 module directory.'
    }

    foreach ($item in $missing) {
        $source = Join-Path $module.Dir $item.RelativePath
        $target = Join-Path $repoRoot $item.TargetPath
        if (-not (Test-Path -LiteralPath $source)) {
            throw "SunnyNet runtime file is missing from the module: $source"
        }
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $target) | Out-Null
        Copy-Item -LiteralPath $source -Destination $target -Force
    }
}

Assert-Command -Name 'go'
Assert-Command -Name 'gcc'

Push-Location $repoRoot
try {
    $env:CGO_ENABLED = '1'
    $env:CGO_CFLAGS = '-std=gnu17 -D_WIN32_WINNT=0x0501'
    $env:CGO_CXXFLAGS = '-std=gnu++17 -D_WIN32_WINNT=0x0501'
    $env:CGO_LDFLAGS = '-Wl,--allow-multiple-definition -lwinpthread'

    Ensure-SunnyNetRuntimeFiles

    switch ($Action) {
        'test' {
            go test ./...
        }
        'build' {
            $outputPath = if ([System.IO.Path]::IsPathRooted($Output)) {
                $Output
            } else {
                Join-Path $repoRoot $Output
            }
            New-Item -ItemType Directory -Force -Path (Split-Path -Parent $outputPath) | Out-Null
            go build "-ldflags=-w -s -extldflags '-static'" -o $outputPath .
            if ($LASTEXITCODE -eq 0) {
                & $outputPath version
            }
        }
        'run' {
            go run . @AppArgs
        }
    }

    if ($LASTEXITCODE -ne 0) {
        throw "$Action failed with exit code $LASTEXITCODE."
    }
}
finally {
    Pop-Location
}
