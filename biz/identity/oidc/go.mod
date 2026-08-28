module github.com/darkinno-tech/saas/biz/identity/oidc

go 1.24.0

require (
	github.com/darkinno-tech/saas v0.3.0
	github.com/coreos/go-oidc/v3 v3.15.0
	golang.org/x/oauth2 v0.30.0
)

require github.com/go-jose/go-jose/v4 v4.1.4 // indirect

replace github.com/darkinno-tech/saas => ../../..
