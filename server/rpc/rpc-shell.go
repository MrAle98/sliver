package rpc

/*
	Sliver Implant Framework
	Copyright (C) 2019  Bishop Fox

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU General Public License as published by
	the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU General Public License for more details.

	You should have received a copy of the GNU General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

import (
	"errors"
	"io"

	"github.com/bishopfox/sliver/protobuf/rpcpb"
	"github.com/bishopfox/sliver/protobuf/sliverpb"
	"github.com/bishopfox/sliver/server/core"
	"github.com/golang/protobuf/proto"
)

var (
	// ErrTunnelInitFailure - Returned when a tunnel cannot be initialized
	ErrTunnelInitFailure = errors.New("Failed to initialize tunnel")
)

// Shell - Open an interactive shell
func (s *Server) Shell(stream rpcpb.SliverRPC_ShellServer) error {
	shellTun, err := stream.Recv()
	if err != nil {
		return err
	}
	session := core.Sessions.Get(shellTun.SessionID)
	tunnel := core.Tunnels.Create(session.ID)
	if tunnel == nil {
		return ErrTunnelInitFailure
	}
	shellTun.TunnelID = tunnel.ID
	data, _ := proto.Marshal(shellTun)
	session.Send <- &sliverpb.Envelope{
		ID:   core.EnvelopeID(),
		Type: sliverpb.MsgShellTunnel,
		Data: data,
	}

	go func() {
		for data := range tunnel.Session.Recv {
			stream.Send(&sliverpb.ShellTunnel{
				SessionID: session.ID,
				TunnelID:  tunnel.ID,
				Data:      data,
			})
		}
	}()

	for {
		shell, err := stream.Recv()
		if err == io.EOF {
			break
		}
		tunnel.Session.Send <- shell.Data
	}
	return nil
}
