module github.com/darkinno-tech/saas/biz/notification/ses

go 1.23.0

require (
	github.com/aws/aws-sdk-go-v2/service/sesv2 v1.55.2
	github.com/aws/smithy-go v1.24.0
	github.com/darkinno-tech/saas v0.3.3
)

require (
	github.com/aws/aws-sdk-go-v2 v1.40.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.15 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.15 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.15 // indirect
)

replace github.com/darkinno-tech/saas => ../../..
