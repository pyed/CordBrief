# scripts/update_secret.ps1
# Securely updates the Discord Client Secret strictly in container private storage (0600)
# without touching repository .env, echoing, or logging the secret value.

[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"

Write-Host "=== Discord Client Secret Secure Updater ===" -ForegroundColor Cyan
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

# 3. Preserve existing client_id
$clientId = ""

# Try to extract existing client_id from container credentials.json if available
try {
    $existingId = docker exec cordbrief-collector python3 -c "import json; print(json.load(open('/var/lib/cordbrief/credentials.json')).get('client_id', ''))" 2>$null
    if ($LASTEXITCODE -eq 0 -and !([string]::IsNullOrWhiteSpace($existingId))) {
        $clientId = $existingId.Trim()
    }
} catch {}

# Fallback to repository .env or default ID if container file was missing
if ([string]::IsNullOrWhiteSpace($clientId)) {
    $envFile = Join-Path $PSScriptRoot "..\.env"
    if (Test-Path $envFile) {
        $envLines = Get-Content $envFile
        foreach ($line in $envLines) {
            if ($line -match "^DISCORD_CLIENT_ID=(.+)$") {
                $clientId = $matches[1].Trim()
                break
            }
        }
    }
}

if ([string]::IsNullOrWhiteSpace($clientId)) {
    $clientId = "1547744191122247772"
}

# 4. Construct JSON payload in memory with proper escaping
$obj = @{
    client_id     = $clientId
    client_secret = $plainSecret
}
$jsonPayload = $obj | ConvertTo-Json -Compress

# Immediately zero out plainSecret reference
$plainSecret = $null
$obj = $null

# 5. Send JSON payload over stdin to docker exec -i
# sh -c inside the container writes to /var/lib/cordbrief/credentials.json and chmods to 0600
# Single quotes for sh -c prevent PowerShell from interpreting Linux paths or redirects
$shCmd = 'cat > /var/lib/cordbrief/credentials.json && chmod 0600 /var/lib/cordbrief/credentials.json'

try {
    $jsonPayload | docker exec -i cordbrief-collector sh -c $shCmd
    $exitCode = $LASTEXITCODE
} catch {
    $exitCode = 1
    Write-Error "Execution error while piping to docker: $_"
} finally {
    $jsonPayload = $null
}

if ($exitCode -ne 0) {
    Write-Error "Failed to update container storage at /var/lib/cordbrief/credentials.json (exit code: $exitCode)."
    exit $exitCode
}

# 6. Non-echoing verification check in container
$verifyExit = 0
try {
    docker exec cordbrief-collector python3 -c "
import json, os, stat
path = '/var/lib/cordbrief/credentials.json'
assert os.path.exists(path), 'File does not exist'
st = os.stat(path)
assert stat.S_IMODE(st.st_mode) == 0o600, 'Incorrect mode'
data = json.load(open(path, 'r'))
assert 'client_id' in data and len(data['client_id']) > 0, 'Invalid client_id'
assert 'client_secret' in data and len(data['client_secret']) > 0, 'Invalid client_secret'
" 2>$null
    if ($LASTEXITCODE -ne 0) { $verifyExit = 1 }
} catch {
    $verifyExit = 1
}

if ($verifyExit -ne 0) {
    Write-Error "Verification failed: /var/lib/cordbrief/credentials.json is invalid or not mode 0600 in container."
    exit 1
}

Write-Host "`n[OK] Discord Client Secret successfully updated in container private storage (/var/lib/cordbrief/credentials.json)." -ForegroundColor Green
Write-Host "[OK] Permissions verified (0600). Client ID preserved ($clientId). Secret was NOT written to .env, echoed, or logged." -ForegroundColor Green
