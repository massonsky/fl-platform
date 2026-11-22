[CmdletBinding()]
param([switch]$Prepare, [switch]$Stop)

$ErrorActionPreference = 'Stop'
if ($Prepare -and $Stop) { throw 'Choose either -Prepare or -Stop.' }
function Invoke-Docker {
    & docker @args
    if ($LASTEXITCODE -ne 0) { throw "Docker command failed (exit $LASTEXITCODE)." }
}

$state = Join-Path $env:LOCALAPPDATA 'FLPlatformDemo\16a7f3e'
$secretPath = Join-Path $state 'secrets'
$presetPath = Join-Path $state 'presets'
$readyMarker = Join-Path $state 'prepared'
$previousSecrets = $env:FL_SECRETS_DIR
$compose = @('compose', '-p', 'fl-platform-demo', '-f', 'compose.yaml',
             '-f', 'compose.runtime.yaml', '-f', 'compose.windows.yaml')
Push-Location -LiteralPath $PSScriptRoot
try {
    $engine = Invoke-Docker info --format '{{.OSType}}'
    if ($engine -ne 'linux') { throw 'Switch Docker Desktop to Linux containers.' }
    $env:FL_SECRETS_DIR = $secretPath.Replace('\','/')
    if ($Stop) {
        Invoke-Docker @compose stop
        return
    }

    if ($Prepare) {
        New-Item -ItemType Directory -Force -Path $secretPath,$presetPath | Out-Null
        $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
        try {
            foreach ($name in @('postgres_password','minio_user','minio_password')) {
                $path = Join-Path $secretPath $name
                if (Test-Path -LiteralPath $path) { continue }
                $bytes = New-Object byte[] 24
                $rng.GetBytes($bytes)
                [IO.File]::WriteAllText($path, [BitConverter]::ToString($bytes).Replace('-','').ToLowerInvariant())
            }
        } finally { $rng.Dispose() }

        Invoke-Docker build -t fl-platform/proto-tools:arch03 contracts/smoke
        Invoke-Docker build -t fl-platform/ml-base:mvp -f services/ml-client/Dockerfile.base .
        $infra = (Resolve-Path -LiteralPath 'infra/vps').Path
        $src = (Resolve-Path -LiteralPath 'services/ml-client/src').Path
        $migrations = (Resolve-Path -LiteralPath 'db/migrations').Path
        $infraMount = "type=bind,source=$infra,target=/infra,readonly"
        $presetMount = "type=bind,source=$presetPath,target=/presets"
        Invoke-Docker run --rm --mount $infraMount --mount "type=bind,source=$secretPath,target=/opt/fl-platform/secrets" fl-platform/proto-tools:arch03 python /infra/init_runtime.py
        Invoke-Docker @compose build minio
        Invoke-Docker build -t fl-platform/control-plane:mvp -f services/control-plane/Dockerfile .
        Invoke-Docker build -t fl-platform/ml-client:mvp -f services/ml-client/Dockerfile .
        Invoke-Docker build -t fl-platform/web:mvp -f web/Dockerfile .
        Invoke-Docker @compose pull postgres
        Invoke-Docker @compose up -d --no-build --wait --wait-timeout 240 postgres minio

        $network = 'fl-platform-demo_default'
        $bootstrapSecrets = "type=bind,source=$secretPath,target=/run/secrets,readonly"
        $s3Init = "import check_s3; check_s3.BASE='http://minio:9000'; check_s3.HOST='minio:9000'; check_s3.main()"
        Invoke-Docker run --rm --network $network --mount $infraMount --mount $bootstrapSecrets -e FL_SECRETS_DIR=/run/secrets -e PYTHONPATH=/infra fl-platform/proto-tools:arch03 python -c $s3Init
        Invoke-Docker run --rm --mount "type=bind,source=$src,target=/src,readonly" --mount $presetMount -e PYTHONPATH=/src fl-platform/ml-base:mvp python -m fl_client.datasets.provision /presets
        Invoke-Docker run --rm --network $network --mount $bootstrapSecrets --mount $presetMount --mount "type=bind,source=$migrations,target=/migrations,readonly" --entrypoint /usr/local/bin/bootstrap fl-platform/control-plane:mvp
        New-Item -ItemType File -Force -Path $readyMarker | Out-Null
    }
    if (!(Test-Path -LiteralPath $readyMarker)) { throw 'Run Start-Demo.ps1 -Prepare first.' }
    Invoke-Docker @compose up -d --no-build
    Invoke-Docker @compose ps
    for ($attempt=0; $attempt -lt 60; $attempt++) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:18081/readyz' -TimeoutSec 3 | Out-Null
            Write-Host 'Ready: http://localhost:18080'
            return
        } catch {
            Start-Sleep -Seconds 2
        }
    }
    throw 'API readiness timed out. Check the container status above.'
} finally {
    $env:FL_SECRETS_DIR = $previousSecrets
    Pop-Location
}
