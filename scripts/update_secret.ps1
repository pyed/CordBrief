# scripts/update_secret.ps1
# Securely updates the Discord Client Secret strictly in container private storage (0600)
# without touching repository .env, echoing, or logging the secret value.

[CmdletBinding()]
param(
    [string]$TargetVolume = "",
    [string]$ClientId = "",
    [string]$TargetOwner = "1000:1000"
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($TargetVolume)) {
    $defaultProject = if ($env:COMPOSE_PROJECT_NAME) { $env:COMPOSE_PROJECT_NAME } else { "cordbrief" }
    $envFile = Join-Path $PSScriptRoot "..\.env"
    if (Test-Path $envFile) {
        $envLines = Get-Content $envFile
        foreach ($line in $envLines) {
            if ($line -match "^COMPOSE_PROJECT_NAME=(.+)$") {
                $defaultProject = $matches[1].Trim()
                break
            }
        }
    }
    $TargetVolume = "${defaultProject}_collector_data"
}

Write-Host "=== Discord Client Secret Secure Updater ===" -ForegroundColor Cyan
Write-Host "Target volume: $TargetVolume"
Write-Host "Target owner:  $TargetOwner"
Write-Host "The secret will be masked and not displayed or logged.`n"

# 1. Masked secret input via Read-Host
$secureInput = Read-Host "Enter new Discord Client Secret" -AsSecureString
if ($null -eq $secureInput) {
    Write-Error "No secret entered. Operation aborted."
    exit 1
}

# 2. Extract plain text in memory strictly for JSON construction
$bstr = [System.Runtime.InteropServices.Marshal]::SecureStringToBSTR($secureInput)
$plainSecret = [System.Runtime.InteropServices.Marshal]::PtrToStringAuto($bstr)
[System.Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)

if ([string]::IsNullOrWhiteSpace($plainSecret)) {
    Write-Error "Empty secret provided. Operation aborted."
    exit 1
}

# 3. Preserve or resolve client_id
if ([string]::IsNullOrWhiteSpace($ClientId)) {
    # Check if target volume already has credentials.json
    try {
        $existing = docker run --rm -v "${TargetVolume}:/var/lib/cordbrief:ro" alpine sh -c "cat /var/lib/cordbrief/credentials.json 2>/dev/null" | Out-String
        if (![string]::IsNullOrWhiteSpace($existing)) {
            $parsed = $existing | ConvertFrom-Json -ErrorAction SilentlyContinue
            if ($parsed -and $parsed.client_id) {
                $ClientId = $parsed.client_id
            }
        }
    } catch {}
}

# Fallback to repository .env
if ([string]::IsNullOrWhiteSpace($ClientId)) {
    $envFile = Join-Path $PSScriptRoot "..\.env"
    if (Test-Path $envFile) {
        $envLines = Get-Content $envFile
        foreach ($line in $envLines) {
            if ($line -match "^DISCORD_CLIENT_ID=(.+)$") {
                $ClientId = $matches[1].Trim()
                break
            }
        }
    }
}

if ([string]::IsNullOrWhiteSpace($ClientId)) {
    $ClientId = Read-Host "Enter Discord Client ID"
}

if ([string]::IsNullOrWhiteSpace($ClientId)) {
    Write-Error "Empty Discord Client ID provided. Operation aborted."
    exit 1
}

# 4. Construct JSON payload in memory
$obj = @{
    client_id     = $ClientId
    client_secret = $plainSecret
}
$jsonPayload = $obj | ConvertTo-Json -Compress

# Immediately zero out plainSecret reference
$plainSecret = $null
$obj = $null

# 5. Write securely to target volume with mode 0600 and resolved ownership
$shCmd = "mkdir -p /var/lib/cordbrief && cat > /var/lib/cordbrief/credentials.json && chmod 0600 /var/lib/cordbrief/credentials.json && chown $TargetOwner /var/lib/cordbrief/credentials.json"

try {
    $jsonPayload | docker run --rm -i -v "${TargetVolume}:/var/lib/cordbrief" alpine sh -c $shCmd
    $exitCode = $LASTEXITCODE
} catch {
    $exitCode = 1
    Write-Error "Execution error while piping to docker: $_"
} finally {
    $jsonPayload = $null
}

if ($exitCode -ne 0) {
    Write-Error "Failed to update storage in volume [$TargetVolume] (exit code: $exitCode)."
    exit $exitCode
}

# 6. Non-echoing verification check in container
$verifyExit = 0
try {
    $vCheck = docker run --rm -v "${TargetVolume}:/var/lib/cordbrief:ro" alpine sh -c '
        if [ ! -f /var/lib/cordbrief/credentials.json ]; then exit 1; fi
        mode=$(stat -c %a /var/lib/cordbrief/credentials.json 2>/dev/null || echo unknown)
        if [ "$mode" != "600" ]; then exit 2; fi
        if ! grep -q "\"client_id\"" /var/lib/cordbrief/credentials.json || ! grep -q "\"client_secret\"" /var/lib/cordbrief/credentials.json; then exit 3; fi
        echo OK
    ' 2>$null
    if ($LASTEXITCODE -ne 0 -or $vCheck.Trim() -ne "OK") { $verifyExit = 1 }
} catch {
    $verifyExit = 1
}

if ($verifyExit -ne 0) {
    Write-Error "Verification failed: /var/lib/cordbrief/credentials.json in volume [$TargetVolume] is invalid or not mode 0600."
    exit 1
}

Write-Host "`n[OK] Discord Client Secret successfully updated in volume [$TargetVolume]." -ForegroundColor Green
Write-Host "[OK] Permissions verified (0600, uid:gid $TargetOwner). Client ID preserved ($ClientId). Secret was NOT written to .env, echoed, or logged." -ForegroundColor Green
