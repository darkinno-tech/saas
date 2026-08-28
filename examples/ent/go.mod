module github.com/im10furry/saas/examples/ent

go 1.23.0

require (
	entgo.io/ent v0.14.1
	github.com/im10furry/saas v0.3.0
	github.com/im10furry/saas/data/ent v0.3.0
)

require github.com/google/uuid v1.3.0 // indirect

replace github.com/im10furry/saas => ../..

replace github.com/im10furry/saas/data/ent => ../../data/ent
