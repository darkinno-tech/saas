param(
    [switch]$KeepServices,
    [string]$CoverageProfile
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$composeFile = Join-Path $PSScriptRoot 'compose.yaml'
$managedEnvironmentVariables = @(
    'SAAS_MYSQL_DSN',
    'SAAS_POSTGRES_DSN',
    'SAAS_REDIS_ADDR',
    'SAAS_REDIS_DB',
    'SAAS_REDIS_PASSWORD'
)
$databaseCoveragePackages = @(
    'github.com/darkinno-tech/saas/biz/audit',
    'github.com/darkinno-tech/saas/biz/identity',
    'github.com/darkinno-tech/saas/biz/identity/oidc',
    'github.com/darkinno-tech/saas/biz/rbac',
    'github.com/darkinno-tech/saas/biz/user',
    'github.com/darkinno-tech/saas/core/store',
    'github.com/darkinno-tech/saas/feature',
    'github.com/darkinno-tech/saas/plan',
    'github.com/darkinno-tech/saas/quota',
    'github.com/darkinno-tech/saas/subscription'
) -join ','
$previousEnvironment = @{}
foreach ($name in $managedEnvironmentVariables) {
    $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}

function Invoke-Checked {
    param([string]$File, [string[]]$Arguments)
    & $File @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$File $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
    }
}

Push-Location $repoRoot
try {
    Invoke-Checked docker @('compose', '-f', $composeFile, 'up', '-d', '--wait')
    $env:SAAS_MYSQL_DSN = 'root:saas@tcp(127.0.0.1:33067)/saas_test?parseTime=true&timeout=3s&readTimeout=3s&writeTimeout=3s'
    $env:SAAS_POSTGRES_DSN = 'postgres://saas:saas@127.0.0.1:55432/saas_test?sslmode=disable'
    $env:SAAS_REDIS_ADDR = '127.0.0.1:56379'
    $env:SAAS_REDIS_DB = '15'
    Remove-Item -Path Env:SAAS_REDIS_PASSWORD -ErrorAction SilentlyContinue

    Push-Location (Join-Path $repoRoot 'data/gorm')
    try {
        Invoke-Checked go @('test', './...', '-run', '^TestMySQLIntegrationEnforcesTenantIsolation$', '-count=1')
    } finally {
        Pop-Location
    }
    Push-Location (Join-Path $repoRoot 'tests/db')
    try {
        $databaseTestArguments = @('test', './...', '-run', '^Test(AuditSQLStore|SQLStore|QuotaSQLStore|RBACAndUserSQLStore|IdentitySQLStore|OIDCSQLLoginStore|FeatureSQLStore|PlanSQLStore|SubscriptionSQLStore)(MySQL|Postgres)Integration$', '-count=1')
        if ($CoverageProfile) {
            $databaseTestArguments += @('-covermode=atomic', "-coverpkg=$databaseCoveragePackages", "-coverprofile=$CoverageProfile")
        }
        Invoke-Checked -File go -Arguments $databaseTestArguments
    } finally {
        Pop-Location
    }
    Push-Location (Join-Path $repoRoot 'cache/redis')
    try {
        Invoke-Checked go @('test', './...', '-run', '^TestRedisCacheIntegration$', '-count=1')
    } finally {
        Pop-Location
    }
} catch {
    & docker compose -f $composeFile logs --no-color
    throw
} finally {
    try {
        foreach ($name in $managedEnvironmentVariables) {
            [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], 'Process')
        }
        if (-not $KeepServices) {
            Invoke-Checked docker @('compose', '-f', $composeFile, 'down', '--volumes', '--remove-orphans')
        }
    } finally {
        Pop-Location
    }
}
