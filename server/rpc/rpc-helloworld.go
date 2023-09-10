package rpc

import (
	"context"

	"github.com/bishopfox/sliver/protobuf/commonpb"
	"github.com/bishopfox/sliver/protobuf/sliverpb"
)

// HelloWorld - Hello World
func (rpc *Server) HelloWorld(ctx context.Context, req *sliverpb.HelloWorldReq) (*sliverpb.HelloWorld, error) {
	resp := &sliverpb.HelloWorld{Response: &commonpb.Response{}}
	err := rpc.GenericHandler(req, resp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}
