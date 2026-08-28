module github.com/im10furry/saas/tests/db

go 1.24.0

require (
	github.com/im10furry/saas v0.3.0
	github.com/im10furry/saas/biz/identity/oidc v0.3.0
	github.com/go-sql-driver/mysql v1.8.1
	github.com/lib/pq v1.10.9
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/coreos/go-oidc/v3 v3.15.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	golang.org/x/oauth2 v0.30.0 // indirect
)

replace github.com/im10furry/saas => ../..

replace github.com/im10furry/saas/biz/identity/oidc => ../../biz/identity/oidc
