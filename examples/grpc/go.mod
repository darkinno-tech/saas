module github.com/darkinno-tech/saas/examples/grpc

go 1.25.0

require (
	github.com/darkinno-tech/saas v0.3.3
	github.com/darkinno-tech/saas/rpc/grpc v0.3.2
	google.golang.org/grpc v1.82.1
)

require (
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/darkinno-tech/saas => ../..

replace github.com/darkinno-tech/saas/rpc/grpc => ../../rpc/grpc
