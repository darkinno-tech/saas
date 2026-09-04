module github.com/darkinno-tech/saas/examples/grpc

go 1.23.0

require (
	github.com/darkinno-tech/saas v0.3.3
	github.com/darkinno-tech/saas/rpc/grpc v0.3.2
	google.golang.org/grpc v1.75.1
)

require (
	golang.org/x/net v0.41.0 // indirect
	golang.org/x/sys v0.33.0 // indirect
	golang.org/x/text v0.26.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250707201910-8d1bb00bc6a7 // indirect
	google.golang.org/protobuf v1.36.6 // indirect
)

replace github.com/darkinno-tech/saas => ../..

replace github.com/darkinno-tech/saas/rpc/grpc => ../../rpc/grpc
