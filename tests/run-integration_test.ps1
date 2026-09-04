$runnerPath = Join-Path $PSScriptRoot 'run-integration.ps1'

Describe 'run-integration.ps1 environment isolation' {
    It 'does not pass an inherited Redis password to the disposable cache test and restores it afterward' {
        $name = 'SAAS_REDIS_PASSWORD'
        $previous = [Environment]::GetEnvironmentVariable($name, 'Process')
        $previousDocker = Get-Command docker -CommandType Function -ErrorAction SilentlyContinue
        $previousGo = Get-Command go -CommandType Function -ErrorAction SilentlyContinue
        try {
            [Environment]::SetEnvironmentVariable($name, 'host-inherited-password', 'Process')
            $global:saasObservedRedisPassword = $null

            Set-Item -Path Function:\global:docker -Value { $global:LASTEXITCODE = 0 }
            Set-Item -Path Function:\global:go -Value {
                if (($args -contains './...') -and $PWD.Path.Replace('/', '\').EndsWith('cache\redis')) {
                    $global:saasObservedRedisPassword = [Environment]::GetEnvironmentVariable('SAAS_REDIS_PASSWORD', 'Process')
                }
                $global:LASTEXITCODE = 0
            }

            . $runnerPath -KeepServices

            $global:saasObservedRedisPassword | Should Be $null
            [Environment]::GetEnvironmentVariable($name, 'Process') | Should Be 'host-inherited-password'
        } finally {
            [Environment]::SetEnvironmentVariable($name, $previous, 'Process')
            Remove-Variable -Name saasObservedRedisPassword -Scope Global -ErrorAction SilentlyContinue
            if ($previousDocker) {
                Set-Item -Path Function:\global:docker -Value $previousDocker.ScriptBlock
            } else {
                Remove-Item -Path Function:\docker -ErrorAction SilentlyContinue
            }
            if ($previousGo) {
                Set-Item -Path Function:\global:go -Value $previousGo.ScriptBlock
            } else {
                Remove-Item -Path Function:\go -ErrorAction SilentlyContinue
            }
        }
    }

    It 'runs the added SQL store contracts for both disposable database dialects' {
        $previousDocker = Get-Command docker -CommandType Function -ErrorAction SilentlyContinue
        $previousGo = Get-Command go -CommandType Function -ErrorAction SilentlyContinue
        try {
            $global:saasDatabaseTestArguments = @()
            Set-Item -Path Function:\global:docker -Value { $global:LASTEXITCODE = 0 }
            Set-Item -Path Function:\global:go -Value {
                if (($args -contains './...') -and $PWD.Path.Replace('/', '\').EndsWith('tests\db')) {
                    $global:saasDatabaseTestArguments = @($args)
                }
                $global:LASTEXITCODE = 0
            }

            . $runnerPath -KeepServices

            ($global:saasDatabaseTestArguments -contains '^Test(AuditSQLStore|SQLStore|QuotaSQLStore|RBACAndUserSQLStore|IdentitySQLStore|OIDCSQLLoginStore|FeatureSQLStore|PlanSQLStore|SubscriptionSQLStore)(MySQL|Postgres)Integration$') | Should Be $true
        } finally {
            Remove-Variable -Name saasDatabaseTestArguments -Scope Global -ErrorAction SilentlyContinue
            if ($previousDocker) {
                Set-Item -Path Function:\global:docker -Value $previousDocker.ScriptBlock
            } else {
                Remove-Item -Path Function:\docker -ErrorAction SilentlyContinue
            }
            if ($previousGo) {
                Set-Item -Path Function:\global:go -Value $previousGo.ScriptBlock
            } else {
                Remove-Item -Path Function:\go -ErrorAction SilentlyContinue
            }
        }
    }

    It 'can emit target-package coverage for the database contracts' {
        $previousDocker = Get-Command docker -CommandType Function -ErrorAction SilentlyContinue
        $previousGo = Get-Command go -CommandType Function -ErrorAction SilentlyContinue
        try {
            $profile = Join-Path $TestDrive 'saas-db-coverage.out'
            $global:saasDatabaseCoverageArguments = @()
            Set-Item -Path Function:\global:docker -Value { $global:LASTEXITCODE = 0 }
            Set-Item -Path Function:\global:go -Value {
                if (($args -contains './...') -and $PWD.Path.Replace('/', '\').EndsWith('tests\db')) {
                    $global:saasDatabaseCoverageArguments = @($args)
                }
                $global:LASTEXITCODE = 0
            }

            . $runnerPath -KeepServices -CoverageProfile $profile

            $expectedCoveragePackages = @(
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
            ($global:saasDatabaseCoverageArguments -contains '-covermode=atomic') | Should Be $true
            ($global:saasDatabaseCoverageArguments -contains "-coverpkg=$expectedCoveragePackages") | Should Be $true
            ($global:saasDatabaseCoverageArguments -contains "-coverprofile=$profile") | Should Be $true
        } finally {
            Remove-Variable -Name saasDatabaseCoverageArguments -Scope Global -ErrorAction SilentlyContinue
            if ($previousDocker) {
                Set-Item -Path Function:\global:docker -Value $previousDocker.ScriptBlock
            } else {
                Remove-Item -Path Function:\docker -ErrorAction SilentlyContinue
            }
            if ($previousGo) {
                Set-Item -Path Function:\global:go -Value $previousGo.ScriptBlock
            } else {
                Remove-Item -Path Function:\go -ErrorAction SilentlyContinue
            }
        }
    }
}
