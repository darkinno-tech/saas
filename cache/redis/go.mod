module github.com/im10furry/saas/cache/redis

go 1.24.0

require (
	github.com/im10furry/saas v0.3.0
	github.com/redis/go-redis/v9 v9.21.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
)

replace github.com/im10furry/saas => ../..
