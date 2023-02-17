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
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	consts "github.com/bishopfox/sliver/client/constants"
	"github.com/bishopfox/sliver/protobuf/clientpb"
	sliverpb "github.com/bishopfox/sliver/protobuf/sliverpb"
	"github.com/bishopfox/sliver/server/core"
	"github.com/bishopfox/sliver/server/cryptography"
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
	beacon.Transport = implantConn.ID
	beacon.RemoteAddress = implantConn.RemoteAddress
	beacon.PID = beaconReg.Register.Pid
	beacon.Filename = beaconReg.Register.Filename
	beacon.LastCheckin = implantConn.GetLastMessage()
	beacon.Version = beaconReg.Register.Version
	beacon.ReconnectInterval = beaconReg.Register.ReconnectInterval
	beacon.ActiveC2 = beaconReg.Register.ActiveC2
	beacon.ProxyURL = beaconReg.Register.ProxyURL
	//beacon.ConfigID = uuid.FromStringOrNil(beaconReg.Register.ConfigID)
	beacon.Locale = beaconReg.Register.Locale

	beacon.Interval = beaconReg.Interval
	beacon.Jitter = beaconReg.Jitter
	beacon.NextCheckin = time.Now().Unix() + beaconReg.NextCheckin

	err = db.Session().Save(beacon).Error
	if err != nil {
		beaconHandlerLog.Errorf("Database write %s", err)
	}
	implantConnID := implantConn.ID
	var associated_pivot *core.Pivot = nil
	core.PivotSessions.Range(func(key, value interface{}) bool {
		pivot := value.(*core.Pivot)
		if pivot.ImplantConn.ID == implantConnID {
			associated_pivot = pivot
		}
		return true
	})
	if associated_pivot != nil && associated_pivot.Beacon == true {
		associated_pivot.BeaconID = beaconReg.ID
	}
	//associated_pivot.Start()
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
		go beaconTaskResults(beaconTasks.ID, beaconTasks.Tasks)
		return nil
	}

	beaconHandlerLog.Infof("Beacon %s requested pending task(s)", beaconTasks.ID)
	tasks := []*sliverpb.Envelope{}
	// Pending tasks are ordered by their creation time.
	pendingTasks, err := db.PendingBeaconTasksByBeaconID(beaconTasks.ID)
	var pendingPivotTasks []*models.BeaconTask
	core.PivotSessions.Range(func(key, value interface{}) bool {
		pivot := value.(*core.Pivot)
		if pivot.Beacon == true && pivot.ImmediateBeaconID == beaconTasks.ID {
			temp, _ := db.PendingBeaconTasksByBeaconID(pivot.BeaconID)
			for _, t := range temp {
				pendingPivotTasks = append(pendingPivotTasks, t)
			}
		}
		if pivot.Beacon == true && pivot.ImmediateBeaconID == beaconTasks.ID {
			select {
			case envelope := <-pivot.ImplantConn.Send:
				tasks = append(tasks, envelope)
			case <-time.After(1 * time.Second):
			}
		}
		return true
	})
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		beaconHandlerLog.Errorf("Beacon task database error: %s", err)
		return nil
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
	for _, pendingTask := range pendingPivotTasks {
		envelope := &sliverpb.Envelope{}
		err = proto.Unmarshal(pendingTask.Request, envelope)
		if err != nil {
			beaconHandlerLog.Errorf("Error decoding pending task: %s", err)
			continue
		}
		envelope.ID = pendingTask.EnvelopeID
		var associated_pivot *core.Pivot = nil
		core.PivotSessions.Range(func(key, value interface{}) bool {
			pivot := value.(*core.Pivot)
			if uuid.FromStringOrNil(pivot.BeaconID) == pendingTask.BeaconID {
				associated_pivot = pivot
			}
			return true
		})
		envelopeData, _ := proto.Marshal(envelope)
		ciphertext, _ := associated_pivot.CipherCtx.Encrypt(envelopeData)
		peerEnvelopeData, _ := proto.Marshal(&sliverpb.PivotPeerEnvelope{
			Type:  envelope.Type,
			Peers: associated_pivot.Peers,
			Data:  ciphertext,
		})
		final_envelope := &sliverpb.Envelope{
			Type: sliverpb.MsgPivotPeerEnvelope,
			Data: peerEnvelopeData,
		}
		tasks = append(tasks, final_envelope)
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

func beaconTaskResults(beaconID string, taskEnvelopes []*sliverpb.Envelope) *sliverpb.Envelope {
	for _, envelope := range taskEnvelopes {
		if envelope.Type == sliverpb.MsgPivotPeerEnvelope {
			peerEnvelope := &sliverpb.PivotPeerEnvelope{}
			err := proto.Unmarshal(envelope.Data, peerEnvelope)
			if err != nil {
				pivotLog.Errorf("failed to parse outermost peer envelope")
				return nil
			}
			switch peerEnvelope.Type {
			case sliverpb.MsgPivotServerKeyExchange:
				serverKeyEx := &sliverpb.PivotServerKeyExchange{}
				err := proto.Unmarshal(peerEnvelope.Data, serverKeyEx)
				if err != nil {
					pivotLog.Errorf("failed to parse pivot server key exchange: %v", err)
					return nil
				}
				var publicKeyDigest [32]byte
				copy(publicKeyDigest[:], serverKeyEx.SessionKey[:32])
				implantConfig, err := db.ImplantConfigByECCPublicKeyDigest(publicKeyDigest)
				if err != nil || implantConfig == nil {
					pivotLog.Warn("Unknown public key digest")
					return nil
				}
				publicKey, err := base64.RawStdEncoding.DecodeString(implantConfig.ECCPublicKey)
				if err != nil || len(publicKey) != 32 {
					pivotLog.Warn("Failed to decode public key")
					return nil
				}
				var senderPublicKey [32]byte
				copy(senderPublicKey[:], publicKey)
				serverKeyPair := cryptography.ECCServerKeyPair()
				rawSessionKey, err := cryptography.ECCDecrypt(&senderPublicKey, serverKeyPair.Private, serverKeyEx.SessionKey[32:])
				if err != nil {
					pivotLog.Warn("Failed to decrypt session key from origin")
					return nil
				}
				sessionKey, err := cryptography.KeyFromBytes(rawSessionKey)
				if err != nil {
					pivotLog.Warn("Failed to create session key from bytes")
					return nil
				}
				pivotBeacon := core.NewPivotBeacon(beaconID, peerEnvelope.Peers)
				pivotBeacon.OriginID = peerEnvelope.Peers[0].PeerID
				pivotBeacon.CipherCtx = cryptography.NewCipherContext(sessionKey)
				b, _ := db.BeaconByID(beaconID)
				pivotRemoteAddr := peersToString(b.RemoteAddress, peerEnvelope)
				pivotBeacon.ImplantConn = core.NewImplantConnection(core.PivotTransportName, pivotRemoteAddr)
				core.PivotSessions.Store(pivotBeacon.ID, pivotBeacon)
				keyExRespEnvelope := MustMarshal(&sliverpb.Envelope{
					Type: sliverpb.MsgPivotServerKeyExchange,
					Data: MustMarshal(&sliverpb.PivotServerKeyExchange{
						SessionKey: uuid.FromStringOrNil(pivotBeacon.ID).Bytes(), // Re-use the bytes field
					}),
				})
				ciphertext, err := pivotBeacon.CipherCtx.Encrypt(keyExRespEnvelope)
				if err != nil {
					pivotLog.Warn("Failed to encrypt pivot server key exchange response")
					return nil
				}
				peerEnvelopeData, _ := proto.Marshal(&sliverpb.PivotPeerEnvelope{
					Peers: pivotBeacon.Peers,
					Data:  ciphertext,
				})
				if err != nil {
					pivotLog.Warn("Failed to encrypt pivot server key exchange response")
					return nil
				}
				pivotBeacon.ImplantConn.Send <- &sliverpb.Envelope{Type: sliverpb.MsgPivotPeerEnvelope,
					Data: peerEnvelopeData}
				// t, _ := b.Task(&sliverpb.Envelope{Type: sliverpb.MsgPivotPeerEnvelope,
				// 	Data: peerEnvelopeData})
				// err = db.Session().Save(t).Error
				// if err == nil {
				// 	a := 2
				// 	a = a + 1
				// }
			case sliverpb.MsgPivotSessionEnvelope:
				pivotBeaconID := uuid.FromBytesOrNil(peerEnvelope.PivotSessionID).String()
				if pivotBeaconID == "" {
					pivotLog.Errorf("failed to parse pivot session id from peer envelope")
					return nil
				}
				pivotLog.Debugf("session envelope pivot session ID = %s", pivotBeaconID)
				pivotEntry, ok := core.PivotSessions.Load(pivotBeaconID)
				if !ok {
					pivotLog.Errorf("pivot session id '%s' not found", pivotBeaconID)
					return nil
				}
				pivot := pivotEntry.(*core.Pivot)
				plaintext, err := pivot.CipherCtx.Decrypt(peerEnvelope.Data)
				if err != nil {
					pivotLog.Errorf("failed to decrypt pivot session data: %v", err)
					return nil
				}
				envelope := &sliverpb.Envelope{}
				err = proto.Unmarshal(plaintext, envelope)
				if err != nil {
					pivotLog.Errorf("failed to unmarshal pivot session data: %v", err)
					return nil
				}
				go handlePivotEnvelope(pivot, envelope)
			}
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
