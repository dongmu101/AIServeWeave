// Package tunnelv1 holds the generated Go bindings for the Agent tunnel wire
// contract. Agent, Gateway and Registry all import this one package so the
// three sides cannot drift apart.
//
// Regenerate with `go generate ./api/...` after editing tunnel.proto. The
// generators come from google.golang.org/protobuf and google.golang.org/grpc:
//
//	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
//	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
package tunnelv1

//go:generate protoc --proto_path=../../../.. --go_out=../../../.. --go_opt=module=AIServeWeave --go-grpc_out=../../../.. --go-grpc_opt=module=AIServeWeave api/proto/tunnel/v1/tunnel.proto
