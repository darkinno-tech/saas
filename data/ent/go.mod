module github.com/darkinno-tech/saas/data/ent

go 1.23.0

require (
	entgo.io/ent v0.14.1
	github.com/darkinno-tech/saas v0.3.3
)

require github.com/google/uuid v1.3.0 // indirect

replace github.com/darkinno-tech/saas => ../..
