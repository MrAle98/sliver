package handlers

/*
	Sliver Implant Framework
	Copyright (C) 2021  Bishop Fox

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
	------------------------------------------------------------------------

	WARNING: These functions can be invoked by remote implants without user interaction

*/

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	consts "github.com/bishopfox/sliver/client/constants"
	"github.com/bishopfox/sliver/protobuf/clientpb"
	sliverpb "github.com/bishopfox/sliver/protobuf/sliverpb"
	"github.com/bishopfox/sliver/server/core"
	"github.com/bishopfox/sliver/server/core/rtunnels"
	"github.com/bishopfox/sliver/server/db"
	"github.com/bishopfox/sliver/server/db/models"
	"github.com/bishopfox/sliver/server/log"
	"github.com/gofrs/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

var (
	beaconHandlerLog = log.NamedLogger("handlers", "beacons")
)

func beaconRegisterHandler(implantConn *core.ImplantConnection, data []byte) *sliverpb.Envelope {
	beaconReg := &sliverpb.BeaconRegister{}
	err := proto.Unmarshal(data, beaconReg)
	if err != nil {
		beaconHandlerLog.Errorf("Error decoding beacon registration message: %s", err)
		return nil
	}
	beaconHandlerLog.Infof("Beacon registration from %s", beaconReg.ID)
	beacon, err := db.BeaconByID(beaconReg.ID)
	beaconHandlerLog.Debugf("Found %v err = %s", beacon, err)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		beaconHandlerLog.Errorf("Database query error %s", err)
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		beacon = &models.Beacon{
			ID: uuid.FromStringOrNil(beaconReg.ID),
		}
	}
	beacon.Name = beaconReg.Register.Name
	beacon.Hostname = beaconReg.Register.Hostname
	beacon.UUID = uuid.FromStringOrNil(beaconReg.Register.Uuid)
	beacon.Username = beaconReg.Register.Username
	beacon.UID = beaconReg.Register.Uid
	beacon.GID = beaconReg.Register.Gid
	beacon.OS = beaconReg.Register.Os
	beacon.Arch = beaconReg.Register.Arch
	beacon.Transport = implantConn.Transport
	beacon.RemoteAddress = implantConn.RemoteAddress
	beacon.PID = beaconReg.Register.Pid
	beacon.Filename = beaconReg.Register.Filename
	beacon.LastCheckin = implantConn.GetLastMessage()
	beacon.Version = beaconReg.Register.Version
	beacon.ReconnectInterval = beaconReg.Register.ReconnectInterval
	beacon.ActiveC2 = beaconReg.Register.ActiveC2
	beacon.ProxyURL = beaconReg.Register.ProxyURL
	// beacon.ConfigID = uuid.FromStringOrNil(beaconReg.Register.ConfigID)
	beacon.Locale = beaconReg.Register.Locale

	beacon.Interval = beaconReg.Interval
	beacon.Jitter = beaconReg.Jitter
	beacon.NextCheckin = time.Now().Unix() + beaconReg.NextCheckin
	beacon.PeerID = beaconReg.Register.PeerID
	beacon.ImplantConnID = implantConn.ID
	err = db.Session().Save(beacon).Error
	if err != nil {
		beaconHandlerLog.Errorf("Database write %s", err)
	}

	eventData, _ := proto.Marshal(beacon.ToProtobuf())
	core.EventBroker.Publish(core.Event{
		EventType: consts.BeaconRegisteredEvent,
		Data:      eventData,
		Beacon:    beacon,
	})

	go auditLogBeacon(beacon, beaconReg.Register)
	return nil
}

type auditLogNewBeaconMsg struct {
	Beacon   *clientpb.Beacon
	Register *sliverpb.Register
}

func auditLogBeacon(beacon *models.Beacon, register *sliverpb.Register) {
	msg, err := json.Marshal(auditLogNewBeaconMsg{
		Beacon:   beacon.ToProtobuf(),
		Register: register,
	})
	if err != nil {
		beaconHandlerLog.Errorf("Failed to log new beacon to audit log: %s", err)
	} else {
		log.AuditLogger.Warn(string(msg))
	}
}

func beaconTasksHandler(implantConn *core.ImplantConnection, data []byte) *sliverpb.Envelope {
	beaconTasks := &sliverpb.BeaconTasks{}
	err := proto.Unmarshal(data, beaconTasks)
	if err != nil {
		beaconHandlerLog.Errorf("Error decoding beacon tasks message: %s", err)
		return nil
	}
	go func() {
		err := db.UpdateBeaconCheckinByID(beaconTasks.ID, beaconTasks.NextCheckin)
		if err != nil {
			beaconHandlerLog.Errorf("failed to update checkin: %s", err)
		}
	}()

	// If the message contains tasks then process it as results
	// otherwise send the beacon any pending tasks. Currently we
	// don't receive results and send pending tasks at the same
	// time. We only send pending tasks if the request is empty.
	// If we send the Beacon 0 tasks it should not respond at all.
	if 0 < len(beaconTasks.Tasks) {
		beaconHandlerLog.Infof("Beacon %s returned %d task result(s)", beaconTasks.ID, len(beaconTasks.Tasks))
		go beaconTaskResults(implantConn, beaconTasks.ID, beaconTasks.Tasks)
		return nil
	}

	beaconHandlerLog.Infof("Beacon %s requested pending task(s)", beaconTasks.ID)

	// Pending tasks are ordered by their creation time.
	pendingTasks, err := db.PendingBeaconTasksByBeaconID(beaconTasks.ID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		beaconHandlerLog.Errorf("Beacon task database error: %s", err)
		return nil
	}
	tasks := []*sliverpb.Envelope{}
	max := 5
	current := 0
L:
	for {
		select {
		case env := <-implantConn.Send:
			tasks = append(tasks, env)
			current++
			if current == max {
				break L
			}
		default:
			break L
		}
	}

	for _, pendingTask := range pendingTasks {
		envelope := &sliverpb.Envelope{}
		err = proto.Unmarshal(pendingTask.Request, envelope)
		if err != nil {
			beaconHandlerLog.Errorf("Error decoding pending task: %s", err)
			continue
		}
		envelope.ID = pendingTask.EnvelopeID
		tasks = append(tasks, envelope)
		pendingTask.State = models.SENT
		pendingTask.SentAt = time.Now()
		err = db.Session().Model(&models.BeaconTask{}).Where(&models.BeaconTask{
			ID: pendingTask.ID,
		}).Updates(pendingTask).Error
		if err != nil {
			beaconHandlerLog.Errorf("Database error: %s", err)
		}
	}
	taskData, err := proto.Marshal(&sliverpb.BeaconTasks{Tasks: tasks})
	if err != nil {
		beaconHandlerLog.Errorf("Error marshaling beacon tasks message: %s", err)
		return nil
	}
	beaconHandlerLog.Infof("Sending %d task(s) to beacon %s", len(pendingTasks), beaconTasks.ID)
	return &sliverpb.Envelope{
		Type: sliverpb.MsgBeaconTasks,
		Data: taskData,
	}
}

func beaconTaskResults(implantConn *core.ImplantConnection, beaconID string, taskEnvelopes []*sliverpb.Envelope) *sliverpb.Envelope {
	for _, envelope := range taskEnvelopes {
		if envelope.Type == sliverpb.MsgSocksData {
			socksDataHandler(implantConn, envelope.Data)
		} else if envelope.Type == sliverpb.MsgTunnelData {
			tunnelDataHandler(implantConn, envelope.Data)
		} else if envelope.Type == sliverpb.MsgTunnelClose {
			tunnelCloseHandler(implantConn, envelope.Data)
		} else {
			dbTask, err := db.BeaconTaskByEnvelopeID(beaconID, envelope.ID)
			if err != nil {
				beaconHandlerLog.Errorf("Error finding db task: %s", err)
				continue
			}
			if dbTask == nil {
				beaconHandlerLog.Errorf("Error: nil db task!")
				continue
			}
			dbTask.State = models.COMPLETED
			dbTask.CompletedAt = time.Now()
			dbTask.Response = envelope.Data
			err = db.Session().Model(&models.BeaconTask{}).Where(&models.BeaconTask{
				ID: dbTask.ID,
			}).Updates(dbTask).Error
			if err != nil {
				beaconHandlerLog.Errorf("Error updating db task: %s", err)
				continue
			}
			eventData, _ := proto.Marshal(dbTask.ToProtobuf(false))
			core.EventBroker.Publish(core.Event{
				EventType: consts.BeaconTaskResultEvent,
				Data:      eventData,
			})
		}
	}
	return nil
}

func BeaconSocksDataHandler(implantConn *core.ImplantConnection, beaconID string, data []byte) *sliverpb.Envelope {
	beacon, err := db.BeaconByID(beaconID)
	if err != nil {
		beaconHandlerLog.Warnf("Received socks data from unknown beacon: %v", beaconID)
		return nil
	}
	tunnelHandlerMutex.Lock()
	defer tunnelHandlerMutex.Unlock()
	socksData := &sliverpb.SocksData{}

	proto.Unmarshal(data, socksData)
	//if socksData.CloseConn{
	//	core.SocksTunnels.Close(socksData.TunnelID)
	//	return nil
	//}
	beaconHandlerLog.Debugf("socksDataHandler:", len(socksData.Data), socksData.Data)
	socksTunnel := core.SocksTunnels.Get(socksData.TunnelID)
	if socksTunnel != nil {
		if beacon.ID.String() == socksTunnel.SessionID {
			socksTunnel.FromImplant <- socksData
		} else {
			beaconHandlerLog.Warnf("Warning: Beacon %s attempted to send data on tunnel it did not own", beacon.ID)
		}
	} else {
		beaconHandlerLog.Warnf("Data sent on nil tunnel %d", socksData.TunnelID)
	}
	return nil
}

// The handler mutex prevents a send on a closed channel, without it
// two handlers calls may race when a tunnel is quickly created and closed.
func BeaconTunnelDataHandler(implantConn *core.ImplantConnection, beaconID string, data []byte) *sliverpb.Envelope {
	beacon, err := db.BeaconByID(beaconID)
	if err != nil {
		beaconHandlerLog.Warnf("Received tunnel data from unknown beacon: %v", beacon.ID)
		return nil
	}
	tunnelHandlerMutex.Lock()
	defer tunnelHandlerMutex.Unlock()
	tunnelData := &sliverpb.TunnelData{}
	proto.Unmarshal(data, tunnelData)

	beaconHandlerLog.Debugf("[DATA] Sequence on tunnel %d, %d, data: %s", tunnelData.TunnelID, tunnelData.Sequence, tunnelData.Data)

	rtunnel := rtunnels.GetRTunnel(tunnelData.TunnelID)
	if rtunnel != nil && beacon.ID.String() == rtunnel.SessionID {
		BeaconRTunnelDataHandler(tunnelData, rtunnel, implantConn)
	} else if rtunnel != nil && beacon.ID.String() != rtunnel.SessionID {
		beaconHandlerLog.Warnf("Warning: Beacon %s attempted to send data on reverse tunnel it did not own", beacon.ID)
	} else if rtunnel == nil && tunnelData.CreateReverse == true {
		BeaconCreateReverseTunnelHandler(implantConn, beaconID, data)
		//RTunnelDataHandler(tunnelData, rtunnel, implantConn)
	} else {
		tunnel := core.Tunnels.Get(tunnelData.TunnelID)
		if tunnel != nil {
			if beacon.ID.String() == tunnel.SessionID {
				tunnel.SendDataFromImplant(tunnelData)
			} else {
				beaconHandlerLog.Warnf("Warning: Beacon %s attempted to send data on tunnel it did not own", beacon.ID)
			}
		} else {
			beaconHandlerLog.Warnf("Data sent on nil tunnel %d", tunnelData.TunnelID)
		}
	}

	return nil
}

func BeaconRTunnelDataHandler(tunnelData *sliverpb.TunnelData, tunnel *rtunnels.RTunnel, connection *core.ImplantConnection) {

	// Since we have no guarantees that we will receive tunnel data in the correct order, we need
	// to ensure we write the data back to the reader in the correct order. The server will ensure
	// that TunnelData protobuf objects are numbered in the correct order using the Sequence property.
	// Similarly we ensure that any data we write-back to the server is also numbered correctly. To
	// reassemble the data, we just dump it into the cache and then advance the writer until we no longer
	// have sequential data. So we can receive `n` number of incorrectly ordered Protobuf objects and
	// correctly write them back to the reader.

	// {{if .Config.Debug}}
	//beaconHandlerLog.Infof("[tunnel] Cache tunnel %d (seq: %d)", tunnel.ID, tunnelData.Sequence)
	// {{end}}

	tunnelDataCache.Add(tunnel.ID, tunnelData.Sequence, tunnelData)

	// NOTE: The read/write semantics can be a little mind boggling, just remember we're reading
	// from the server and writing to the tunnel's reader (e.g. stdout), so that's why ReadSequence
	// is used here whereas WriteSequence is used for data written back to the server

	// Go through cache and write all sequential data to the reader
	for recv, ok := tunnelDataCache.Get(tunnel.ID, tunnel.ReadSequence()); ok; recv, ok = tunnelDataCache.Get(tunnel.ID, tunnel.ReadSequence()) {
		// {{if .Config.Debug}}
		//beaconHandlerLog.Infof("[tunnel] Write %d bytes to tunnel %d (read seq: %d)", len(recv.Data), recv.TunnelID, recv.Sequence)
		// {{end}}
		tunnel.Writer.Write(recv.Data)

		// Delete the entry we just wrote from the cache
		tunnelDataCache.DeleteSeq(tunnel.ID, tunnel.ReadSequence())
		tunnel.IncReadSequence() // Increment sequence counter

		// {{if .Config.Debug}}
		//beaconHandlerLog.Infof("[message just received] %v", tunnelData)
		// {{end}}
	}

	//If cache is building up it probably means a msg was lost and the server is currently hung waiting for it.
	//Send a Resend packet to have the msg resent from the cache
	if tunnelDataCache.Len(tunnel.ID) > 3 {
		data, err := proto.Marshal(&sliverpb.TunnelData{
			Sequence: tunnel.WriteSequence(), // The tunnel write sequence
			Ack:      tunnel.ReadSequence(),
			Resend:   true,
			TunnelID: tunnel.ID,
			Data:     []byte{},
		})
		if err != nil {
			// {{if .Config.Debug}}
			//beaconHandlerLog.Infof("[shell] Failed to marshal protobuf %s", err)
			// {{end}}
		} else {
			// {{if .Config.Debug}}
			//beaconHandlerLog.Infof("[tunnel] Requesting resend of tunnelData seq: %d", tunnel.ReadSequence())
			// {{end}}
			connection.RequestResend(data)
		}
	}
}

func BeaconCreateReverseTunnelHandler(implantConn *core.ImplantConnection, beaconID string, data []byte) *sliverpb.Envelope {
	beacon, err := db.BeaconByID(beaconID)

	req := &sliverpb.TunnelData{}
	proto.Unmarshal(data, req)

	var defaultDialer = new(net.Dialer)

	remoteAddress := fmt.Sprintf("%s:%d", req.Rportfwd.Host, req.Rportfwd.Port)

	ctx, cancelContext := context.WithCancel(context.Background())

	dst, err := defaultDialer.DialContext(ctx, "tcp", remoteAddress)
	//dst, err := net.Dial("tcp", remoteAddress)
	if err != nil {
		tunnelClose, _ := proto.Marshal(&sliverpb.TunnelData{
			Closed:   true,
			TunnelID: req.TunnelID,
		})
		implantConn.Send <- &sliverpb.Envelope{
			Type: sliverpb.MsgTunnelClose,
			Data: tunnelClose,
		}
		cancelContext()
		return nil
	}

	if conn, ok := dst.(*net.TCPConn); ok {
		// {{if .Config.Debug}}
		//log.Printf("[portfwd] Configuring keep alive")
		// {{end}}
		conn.SetKeepAlive(true)
		// TODO: Make KeepAlive configurable
		conn.SetKeepAlivePeriod(1000 * time.Second)
	}

	tunnel := rtunnels.NewRTunnel(req.TunnelID, beacon.ID.String(), dst, dst)
	rtunnels.AddRTunnel(tunnel)
	cleanup := func(reason error) {
		// {{if .Config.Debug}}
		beaconHandlerLog.Infof("[portfwd] Closing tunnel %d (%s)", tunnel.ID, reason)
		// {{end}}
		tunnel := rtunnels.GetRTunnel(tunnel.ID)
		rtunnels.RemoveRTunnel(tunnel.ID)
		dst.Close()
		cancelContext()
	}

	go func() {
		tWriter := tunnelWriter{
			tun:  tunnel,
			conn: implantConn,
		}
		// portfwd only uses one reader, hence the tunnel.Readers[0]
		n, err := io.Copy(tWriter, tunnel.Readers[0])
		_ = n // avoid not used compiler error if debug mode is disabled
		// {{if .Config.Debug}}
		beaconHandlerLog.Infof("[tunnel] Tunnel done, wrote %v bytes", n)
		// {{end}}

		cleanup(err)
	}()

	tunnelDataCache.Add(tunnel.ID, req.Sequence, req)

	// NOTE: The read/write semantics can be a little mind boggling, just remember we're reading
	// from the server and writing to the tunnel's reader (e.g. stdout), so that's why ReadSequence
	// is used here whereas WriteSequence is used for data written back to the server

	// Go through cache and write all sequential data to the reader
	for recv, ok := tunnelDataCache.Get(tunnel.ID, tunnel.ReadSequence()); ok; recv, ok = tunnelDataCache.Get(tunnel.ID, tunnel.ReadSequence()) {
		// {{if .Config.Debug}}
		//beaconHandlerLog.Infof("[tunnel] Write %d bytes to tunnel %d (read seq: %d)", len(recv.Data), recv.TunnelID, recv.Sequence)
		// {{end}}
		tunnel.Writer.Write(recv.Data)

		// Delete the entry we just wrote from the cache
		tunnelDataCache.DeleteSeq(tunnel.ID, tunnel.ReadSequence())
		tunnel.IncReadSequence() // Increment sequence counter

		// {{if .Config.Debug}}
		//beaconHandlerLog.Infof("[message just received] %v", tunnelData)
		// {{end}}
	}

	//If cache is building up it probably means a msg was lost and the server is currently hung waiting for it.
	//Send a Resend packet to have the msg resent from the cache
	if tunnelDataCache.Len(tunnel.ID) > 3 {
		data, err := proto.Marshal(&sliverpb.TunnelData{
			Sequence: tunnel.WriteSequence(), // The tunnel write sequence
			Ack:      tunnel.ReadSequence(),
			Resend:   true,
			TunnelID: tunnel.ID,
			Data:     []byte{},
		})
		if err != nil {
			// {{if .Config.Debug}}
			//beaconHandlerLog.Infof("[shell] Failed to marshal protobuf %s", err)
			// {{end}}
		} else {
			// {{if .Config.Debug}}
			//beaconHandlerLog.Infof("[tunnel] Requesting resend of tunnelData seq: %d", tunnel.ReadSequence())
			// {{end}}
			implantConn.RequestResend(data)
		}
	}
	return nil
}

func beaconTunnelCloseHandler(implantConn *core.ImplantConnection, beaconID string, data []byte) *sliverpb.Envelope {
	beacon, err := db.BeaconByID(beaconID)
	if err != nil {
		beaconHandlerLog.Warnf("Received tunnel data from unknown beacon: %v", beacon.ID)
		return nil
	}
	tunnelHandlerMutex.Lock()
	defer tunnelHandlerMutex.Unlock()

	tunnelData := &sliverpb.TunnelData{}
	proto.Unmarshal(data, tunnelData)
	beaconHandlerLog.Debugf("[CLOSE] Sequence on tunnel %d, %d, data: %s", tunnelData.TunnelID, tunnelData.Sequence, tunnelData.Data)
	if !tunnelData.Closed {
		return nil
	}
	tunnel := core.Tunnels.Get(tunnelData.TunnelID)
	if tunnel != nil {
		if beacon.ID.String() == tunnel.SessionID {
			beaconHandlerLog.Infof("Closing tunnel %d", tunnel.ID)
			go core.Tunnels.ScheduleClose(tunnel.ID)
		} else {
			beaconHandlerLog.Warnf("Warning: Beacon %s attempted to send data on tunnel it did not own", beacon.ID.String())
		}
	} else {
		rtunnel := rtunnels.GetRTunnel(tunnelData.TunnelID)
		if rtunnel != nil && beacon.ID.String() == tunnel.SessionID {
			rtunnel.Close()
			rtunnels.RemoveRTunnel(rtunnel.ID)
		} else if rtunnel != nil && beacon.ID.String() != tunnel.SessionID {
			beaconHandlerLog.Warnf("Warning: Session %s attempted to send data on reverse tunnel it did not own", beacon.ID.String())
		} else {
			beaconHandlerLog.Warnf("Close sent on nil tunnel %d", tunnelData.TunnelID)
		}
	}
	return nil
}
