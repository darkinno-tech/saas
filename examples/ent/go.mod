module github.com/darkinno-tech/saas/examples/ent

go 1.23.0

require (
	entgo.io/ent v0.14.1
	github.com/darkinno-tech/saas v0.3.0
	github.com/darkinno-tech/saas/data/ent v0.3.0
)

require github.com/google/uuid v1.3.0 // indirect

replace github.com/darkinno-tech/saas => ../..

replace github.com/darkinno-tech/saas/data/ent => ../../data/ent
