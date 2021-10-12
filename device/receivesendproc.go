package device

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/KusakabeSi/EtherGuardVPN/config"
	orderedmap "github.com/KusakabeSi/EtherGuardVPN/orderdmap"
	"github.com/KusakabeSi/EtherGuardVPN/path"
	"github.com/KusakabeSi/EtherGuardVPN/tap"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
)

func (device *Device) SendPacket(peer *Peer, usage path.Usage, packet []byte, offset int) {
	if peer == nil {
		return
	} else if peer.endpoint == nil {
		return
	}
	if usage == path.NormalPacket && len(packet)-path.EgHeaderLen <= 12 {
		if device.LogLevel.LogNormal {
			fmt.Println("Normal: Invalid packet: Ethernet packet too small")
		}
		return
	}

	if device.LogLevel.LogNormal {
		EgHeader, _ := path.NewEgHeader(packet[:path.EgHeaderLen])
		if usage == path.NormalPacket && EgHeader.GetSrc() == device.ID {
			dst_nodeID := EgHeader.GetDst()
			packet_len := len(packet) - path.EgHeaderLen
			fmt.Println("Normal: Send Normal packet To:" + peer.GetEndpointDstStr() + " SrcID:" + device.ID.ToString() + " DstID:" + dst_nodeID.ToString() + " Len:" + strconv.Itoa(packet_len))
			packet := gopacket.NewPacket(packet[path.EgHeaderLen:], layers.LayerTypeEthernet, gopacket.Default)
			fmt.Println(packet.Dump())
		}
	}
	if device.LogLevel.LogControl {
		if usage != path.NormalPacket {
			if peer.GetEndpointDstStr() != "" {
				fmt.Println("Control: Send To:" + peer.GetEndpointDstStr() + " " + device.sprint_received(usage, packet[path.EgHeaderLen:]))
			}
		}
	}

	var elem *QueueOutboundElement
	elem = device.NewOutboundElement()
	copy(elem.buffer[offset:offset+len(packet)], packet)
	elem.Type = usage
	elem.packet = elem.buffer[offset : offset+len(packet)]
	if peer.isRunning.Get() {
		peer.StagePacket(elem)
		elem = nil
		peer.SendStagedPackets()
	}
}

func (device *Device) BoardcastPacket(skip_list map[config.Vertex]bool, usage path.Usage, packet []byte, offset int) { // Send packet to all connected peers
	send_list := device.graph.GetBoardcastList(device.ID)
	for node_id, _ := range skip_list {
		send_list[node_id] = false
	}
	device.peers.RLock()
	for node_id, should_send := range send_list {
		if should_send {
			peer_out, _ := device.peers.IDMap[node_id]
			device.SendPacket(peer_out, usage, packet, offset)
		}
	}
	device.peers.RUnlock()
}

func (device *Device) SpreadPacket(skip_list map[config.Vertex]bool, usage path.Usage, packet []byte, offset int) { // Send packet to all peers no matter it is alive
	device.peers.RLock()
	for peer_id, peer_out := range device.peers.IDMap {
		if _, ok := skip_list[peer_id]; ok {
			if device.LogLevel.LogTransit {
				fmt.Printf("Transit: Skipped Spread Packet packet through %d to %d\n", device.ID, peer_out.ID)
			}
			continue
		}
		device.SendPacket(peer_out, usage, packet, MessageTransportOffsetContent)
	}
	device.peers.RUnlock()
}

func (device *Device) TransitBoardcastPacket(src_nodeID config.Vertex, in_id config.Vertex, usage path.Usage, packet []byte, offset int) {
	node_boardcast_list := device.graph.GetBoardcastThroughList(device.ID, in_id, src_nodeID)
	device.peers.RLock()
	for peer_id := range node_boardcast_list {
		peer_out := device.peers.IDMap[peer_id]
		if device.LogLevel.LogTransit {
			fmt.Printf("Transit: Transfer packet from %d through %d to %d\n", in_id, device.ID, peer_out.ID)
		}
		device.SendPacket(peer_out, usage, packet, offset)
	}
	device.peers.RUnlock()
}

func (device *Device) Send2Super(usage path.Usage, packet []byte, offset int) {
	device.peers.RLock()
	if device.DRoute.SuperNode.UseSuperNode {
		for _, peer_out := range device.peers.SuperPeer {
			/*if device.LogTransit {
				fmt.Printf("Send to supernode %s\n", peer_out.endpoint.DstToString())
			}*/
			device.SendPacket(peer_out, usage, packet, offset)
		}
	}
	device.peers.RUnlock()
}

func (device *Device) CheckNoDup(packet []byte) bool {
	hasher := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	hasher.Write(packet)
	crc32result := hasher.Sum32()
	_, ok := device.DupData.Get(crc32result)
	device.DupData.Set(crc32result, true)
	return !ok
}

func (device *Device) process_received(msg_type path.Usage, peer *Peer, body []byte) (err error) {
	if device.IsSuperNode {
		switch msg_type {
		case path.Register:
			if content, err := path.ParseRegisterMsg(body); err == nil {
				return device.server_process_RegisterMsg(peer, content)
			}
		case path.PongPacket:
			if content, err := path.ParsePongMsg(body); err == nil {
				return device.server_process_Pong(peer, content)
			}
		default:
			err = errors.New("Not a valid msg_type")
		}
	} else {
		switch msg_type {
		case path.UpdatePeer:
			if content, err := path.ParseUpdatePeerMsg(body); err == nil {
				go device.process_UpdatePeerMsg(peer, content)
			}
		case path.UpdateNhTable:
			if content, err := path.ParseUpdateNhTableMsg(body); err == nil {
				go device.process_UpdateNhTableMsg(peer, content)
			}
		case path.UpdateError:
			if content, err := path.ParseUpdateErrorMsg(body); err == nil {
				device.process_UpdateErrorMsg(peer, content)
			}
		case path.PingPacket:
			if content, err := path.ParsePingMsg(body); err == nil {
				return device.process_ping(peer, content)
			}
		case path.PongPacket:
			if content, err := path.ParsePongMsg(body); err == nil {
				return device.process_pong(peer, content)
			}
		case path.QueryPeer:
			if content, err := path.ParseQueryPeerMsg(body); err == nil {
				return device.process_RequestPeerMsg(content)
			}
		case path.BroadcastPeer:
			if content, err := path.ParseBoardcastPeerMsg(body); err == nil {
				return device.process_BoardcastPeerMsg(peer, content)
			}
		default:
			err = errors.New("Not a valid msg_type")
		}
	}
	return
}

func (device *Device) sprint_received(msg_type path.Usage, body []byte) string {
	switch msg_type {
	case path.Register:
		if content, err := path.ParseRegisterMsg(body); err == nil {
			return content.ToString()
		}
		return "RegisterMsg: Parse failed"
	case path.UpdatePeer:
		if content, err := path.ParseUpdatePeerMsg(body); err == nil {
			return content.ToString()
		}
		return "UpdatePeerMsg: Parse failed"
	case path.UpdateNhTable:
		if content, err := path.ParseUpdateNhTableMsg(body); err == nil {
			return content.ToString()
		}
		return "UpdateNhTableMsg: Parse failed"
	case path.UpdateError:
		if content, err := path.ParseUpdateErrorMsg(body); err == nil {
			return content.ToString()
		}
		return "UpdateErrorMsg: Parse failed"
	case path.PingPacket:
		if content, err := path.ParsePingMsg(body); err == nil {
			return content.ToString()
		}
		return "PingPacketMsg: Parse failed"
	case path.PongPacket:
		if content, err := path.ParsePongMsg(body); err == nil {
			return content.ToString()
		}
		return "PongPacketMsg: Parse failed"
	case path.QueryPeer:
		if content, err := path.ParseQueryPeerMsg(body); err == nil {
			return content.ToString()
		}
		return "QueryPeerMsg: Parse failed"
	case path.BroadcastPeer:
		if content, err := path.ParseBoardcastPeerMsg(body); err == nil {
			return content.ToString()
		}
		return "BoardcastPeerMsg: Parse failed"
	default:
		return "UnknownMsg: Not a valid msg_type"
	}
}

func (device *Device) GeneratePingPacket(src_nodeID config.Vertex, request_reply int) ([]byte, path.Usage, error) {
	body, err := path.GetByte(&path.PingMsg{
		Src_nodeID:   src_nodeID,
		Time:         device.graph.GetCurrentTime(),
		RequestReply: request_reply,
	})
	if err != nil {
		return nil, path.PingPacket, err
	}
	buf := make([]byte, path.EgHeaderLen+len(body))
	header, _ := path.NewEgHeader(buf[0:path.EgHeaderLen])
	if err != nil {
		return nil, path.PingPacket, err
	}
	header.SetDst(config.ControlMessage)
	header.SetTTL(0)
	header.SetSrc(device.ID)
	header.SetPacketLength(uint16(len(body)))
	copy(buf[path.EgHeaderLen:], body)
	return buf, path.PingPacket, nil
}

func (device *Device) SendPing(peer *Peer, times int, replies int, interval float64) {
	for i := 0; i < times; i++ {
		packet, usage, _ := device.GeneratePingPacket(device.ID, replies)
		device.SendPacket(peer, usage, packet, MessageTransportOffsetContent)
		time.Sleep(path.S2TD(interval))
	}
}

func compareVersion(v1 string, v2 string) bool {
	if strings.Contains(v1, "-") {
		v1 = strings.Split(v1, "-")[0]
	}
	if strings.Contains(v2, "-") {
		v2 = strings.Split(v2, "-")[0]
	}
	return v1 == v2
}

func (device *Device) server_process_RegisterMsg(peer *Peer, content path.RegisterMsg) error {
	UpdateErrorMsg := path.UpdateErrorMsg{
		Node_id:   peer.ID,
		Action:    path.NoAction,
		ErrorCode: 0,
		ErrorMsg:  "",
	}
	if peer.ID != content.Node_id {
		UpdateErrorMsg = path.UpdateErrorMsg{
			Node_id:   peer.ID,
			Action:    path.Shutdown,
			ErrorCode: 401,
			ErrorMsg:  "Your node ID is not match with our registered nodeID",
		}
	}
	if compareVersion(content.Version, device.Version) == false {
		UpdateErrorMsg = path.UpdateErrorMsg{
			Node_id:   peer.ID,
			Action:    path.Shutdown,
			ErrorCode: 400,
			ErrorMsg:  fmt.Sprintf("Your version: \"%v\" is not compatible with our version: \"%v\"", content.Version, device.Version),
		}
	}
	if UpdateErrorMsg.Action != path.NoAction {
		body, err := path.GetByte(&UpdateErrorMsg)
		if err != nil {
			return err
		}
		buf := make([]byte, path.EgHeaderLen+len(body))
		header, err := path.NewEgHeader(buf[:path.EgHeaderLen])
		header.SetSrc(device.ID)
		header.SetTTL(device.DefaultTTL)
		header.SetPacketLength(uint16(len(body)))
		copy(buf[path.EgHeaderLen:], body)
		header.SetDst(peer.ID)
		device.SendPacket(peer, path.UpdateError, buf, MessageTransportOffsetContent)
		return nil
	}
	peer.LastPingReceived = time.Now()
	device.Event_server_register <- content
	return nil
}

func (device *Device) server_process_Pong(peer *Peer, content path.PongMsg) error {
	peer.LastPingReceived = time.Now()
	device.Event_server_pong <- content
	return nil
}

func (device *Device) process_ping(peer *Peer, content path.PingMsg) error {
	peer.LastPingReceived = time.Now()
	//peer.Lock()
	//remove peer.endpoint_trylist
	//peer.Unlock()
	PongMSG := path.PongMsg{
		Src_nodeID: content.Src_nodeID,
		Dst_nodeID: device.ID,
		Timediff:   device.graph.GetCurrentTime().Sub(content.Time),
	}
	if device.DRoute.P2P.UseP2P && time.Now().After(device.graph.NhTableExpire) {
		device.graph.UpdateLatency(content.Src_nodeID, device.ID, PongMSG.Timediff, true, false)
	}
	body, err := path.GetByte(&PongMSG)
	if err != nil {
		return err
	}
	buf := make([]byte, path.EgHeaderLen+len(body))
	header, err := path.NewEgHeader(buf[:path.EgHeaderLen])
	header.SetSrc(device.ID)
	header.SetTTL(device.DefaultTTL)
	header.SetPacketLength(uint16(len(body)))
	copy(buf[path.EgHeaderLen:], body)
	if device.DRoute.SuperNode.UseSuperNode {
		header.SetDst(config.SuperNodeMessage)
		device.Send2Super(path.PongPacket, buf, MessageTransportOffsetContent)
	}
	if device.DRoute.P2P.UseP2P {
		header.SetDst(config.ControlMessage)
		device.SpreadPacket(make(map[config.Vertex]bool), path.PongPacket, buf, MessageTransportOffsetContent)
	}
	go device.SendPing(peer, content.RequestReply, 0, 3)
	return nil
}

func (device *Device) process_pong(peer *Peer, content path.PongMsg) error {
	if device.DRoute.P2P.UseP2P {
		if time.Now().After(device.graph.NhTableExpire) {
			device.graph.UpdateLatency(content.Src_nodeID, content.Dst_nodeID, content.Timediff, true, false)
		}
		if !peer.AskedForNeighbor {
			QueryPeerMsg := path.QueryPeerMsg{
				Request_ID: uint32(device.ID),
			}
			body, err := path.GetByte(&QueryPeerMsg)
			if err != nil {
				return err
			}
			buf := make([]byte, path.EgHeaderLen+len(body))
			header, err := path.NewEgHeader(buf[:path.EgHeaderLen])
			header.SetSrc(device.ID)
			header.SetTTL(device.DefaultTTL)
			header.SetPacketLength(uint16(len(body)))
			copy(buf[path.EgHeaderLen:], body)
			device.SendPacket(peer, path.QueryPeer, buf, MessageTransportOffsetContent)
		}
	}
	return nil
}

func (device *Device) process_UpdatePeerMsg(peer *Peer, content path.UpdatePeerMsg) error {
	var send_signal bool
	if device.DRoute.SuperNode.UseSuperNode {
		if peer.ID != config.SuperNodeMessage {
			if device.LogLevel.LogControl {
				fmt.Println("Control: Ignored UpdateErrorMsg. Not from supernode.")
			}
			return nil
		}
		var peer_infos config.API_Peers
		if bytes.Equal(device.peers.Peer_state[:], content.State_hash[:]) {
			if device.LogLevel.LogControl {
				fmt.Println("Control: Same PeerState Hash, skip download nhTable")
			}
			return nil
		}

		downloadurl := device.DRoute.SuperNode.APIUrl + "/peerinfo?NodeID=" + strconv.Itoa(int(device.ID)) + "&PubKey=" + url.QueryEscape(device.staticIdentity.publicKey.ToString()) + "&State=" + url.QueryEscape(string(content.State_hash[:]))
		if device.LogLevel.LogControl {
			fmt.Println("Control: Download peerinfo from :" + downloadurl)
		}
		client := http.Client{
			Timeout: 30 * time.Second,
		}
		resp, err := client.Get(downloadurl)
		if err != nil {
			device.log.Errorf(err.Error())
			return err
		}
		defer resp.Body.Close()
		allbytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			device.log.Errorf(err.Error())
			return err
		}
		if resp.StatusCode != 200 {
			device.log.Errorf("Control: Download peerinfo result failed: " + strconv.Itoa(resp.StatusCode) + " " + string(allbytes))
			return nil
		}
		if device.LogLevel.LogControl {
			fmt.Println("Control: Download peerinfo result :" + string(allbytes))
		}
		if err := json.Unmarshal(allbytes, &peer_infos); err != nil {
			device.log.Errorf("JSON decode error:", err.Error())
			return err
		}

		for nodeID, thepeer := range device.peers.IDMap {
			pk := thepeer.handshake.remoteStatic
			psk := thepeer.handshake.presharedKey
			if val, ok := peer_infos[pk.ToString()]; ok {
				if val.NodeID != nodeID {
					device.RemovePeer(pk)
					continue
				} else if val.PSKey != psk.ToString() {
					device.RemovePeer(pk)
					continue
				}
			} else {
				device.RemovePeer(pk)
				continue
			}
		}

		for PubKey, peerinfo := range peer_infos {
			if len(peerinfo.Connurl) == 0 {
				continue
			}
			sk, err := Str2PubKey(PubKey)
			if err != nil {
				device.log.Errorf("Error decode base64:", err)
				continue
			}
			if bytes.Equal(sk[:], device.staticIdentity.publicKey[:]) {
				continue
			}
			thepeer := device.LookupPeer(sk)
			if thepeer == nil { //not exist in local
				if device.LogLevel.LogControl {
					fmt.Println("Control: Add new peer to local ID:" + peerinfo.NodeID.ToString() + " PubKey:" + PubKey)
				}
				if device.graph.Weight(device.ID, peerinfo.NodeID) == path.Infinity { // add node to graph
					device.graph.UpdateLatency(device.ID, peerinfo.NodeID, path.S2TD(path.Infinity), true, false)
				}
				if device.graph.Weight(peerinfo.NodeID, device.ID) == path.Infinity { // add node to graph
					device.graph.UpdateLatency(peerinfo.NodeID, device.ID, path.S2TD(path.Infinity), true, false)
				}
				device.NewPeer(sk, peerinfo.NodeID, false)
				thepeer = device.LookupPeer(sk)
			}
			if peerinfo.PSKey != "" {
				pk, err := Str2PSKey(peerinfo.PSKey)
				if err != nil {
					device.log.Errorf("Error decode base64:", err)
					continue
				}
				thepeer.SetPSK(pk)
			}

			if thepeer.LastPingReceived.Add(path.S2TD(device.DRoute.PeerAliveTimeout)).Before(time.Now()) {
				//Peer died, try to switch to this new endpoint
				for url, _ := range peerinfo.Connurl {
					thepeer.Lock()
					thepeer.endpoint_trylist.LoadOrStore(url, time.Time{}) //another gorouting will process it
					thepeer.Unlock()
					send_signal = true
				}
			}
		}
		device.peers.Peer_state = content.State_hash
		if send_signal {
			device.event_tryendpoint <- struct{}{}
		}
	}
	return nil
}

func (device *Device) process_UpdateNhTableMsg(peer *Peer, content path.UpdateNhTableMsg) error {
	if device.DRoute.SuperNode.UseSuperNode {
		if peer.ID != config.SuperNodeMessage {
			if device.LogLevel.LogControl {
				fmt.Println("Control: Ignored UpdateErrorMsg. Not from supernode.")
			}
			return nil
		}
		if bytes.Equal(device.graph.NhTableHash[:], content.State_hash[:]) {
			if device.LogLevel.LogControl {
				fmt.Println("Control: Same nhTable Hash, skip download nhTable")
			}
			device.graph.NhTableExpire = time.Now().Add(device.graph.SuperNodeInfoTimeout)
			return nil
		}
		var NhTable config.NextHopTable
		if bytes.Equal(device.graph.NhTableHash[:], content.State_hash[:]) {
			return nil
		}
		downloadurl := device.DRoute.SuperNode.APIUrl + "/nhtable?NodeID=" + strconv.Itoa(int(device.ID)) + "&PubKey=" + url.QueryEscape(device.staticIdentity.publicKey.ToString()) + "&State=" + url.QueryEscape(string(content.State_hash[:]))
		if device.LogLevel.LogControl {
			fmt.Println("Control: Download NhTable from :" + downloadurl)
		}
		client := http.Client{
			Timeout: 30 * time.Second,
		}
		resp, err := client.Get(downloadurl)
		if err != nil {
			device.log.Errorf(err.Error())
			return err
		}
		defer resp.Body.Close()
		allbytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			device.log.Errorf(err.Error())
			return err
		}
		if resp.StatusCode != 200 {
			device.log.Errorf("Control: Download peerinfo result failed: " + strconv.Itoa(resp.StatusCode) + " " + string(allbytes))
			return nil
		}
		if device.LogLevel.LogControl {
			fmt.Println("Control: Download NhTable result :" + string(allbytes))
		}
		if err := json.Unmarshal(allbytes, &NhTable); err != nil {
			device.log.Errorf("JSON decode error:", err.Error())
			return err
		}
		device.graph.SetNHTable(NhTable, content.State_hash)
	}
	return nil
}

func (device *Device) process_UpdateErrorMsg(peer *Peer, content path.UpdateErrorMsg) error {
	if peer.ID != config.SuperNodeMessage {
		if device.LogLevel.LogControl {
			fmt.Println("Control: Ignored UpdateErrorMsg. Not from supernode.")
		}
		return nil
	}
	device.log.Errorf(strconv.Itoa(content.ErrorCode) + ": " + content.ErrorMsg)
	if content.Action == path.Shutdown {
		device.closed <- struct{}{}
	} else if content.Action == path.Panic {
		panic(content.ToString())
	}
	return nil
}

func (device *Device) process_RequestPeerMsg(content path.QueryPeerMsg) error { //Send all my peers to all my peers
	if device.DRoute.P2P.UseP2P {
		device.peers.RLock()
		for pubkey, peer := range device.peers.keyMap {
			if peer.ID >= config.Special_NodeID {
				continue
			}
			if peer.endpoint == nil {
				continue
			}
			peer.handshake.mutex.RLock()
			response := path.BoardcastPeerMsg{
				Request_ID: content.Request_ID,
				NodeID:     peer.ID,
				PubKey:     pubkey,
				ConnURL:    peer.endpoint.DstToString(),
			}
			peer.handshake.mutex.RUnlock()
			body, err := path.GetByte(response)
			if err != nil {
				device.log.Errorf("Error at receivesendproc.go line221: ", err)
				continue
			}
			buf := make([]byte, path.EgHeaderLen+len(body))
			header, _ := path.NewEgHeader(buf[0:path.EgHeaderLen])
			header.SetDst(config.ControlMessage)
			header.SetTTL(device.DefaultTTL)
			header.SetSrc(device.ID)
			header.SetPacketLength(uint16(len(body)))
			copy(buf[path.EgHeaderLen:], body)
			device.SpreadPacket(make(map[config.Vertex]bool), path.BroadcastPeer, buf, MessageTransportOffsetContent)
		}
		device.peers.RUnlock()
	}
	return nil
}

func (device *Device) process_BoardcastPeerMsg(peer *Peer, content path.BoardcastPeerMsg) error {
	if device.DRoute.P2P.UseP2P {
		var pk NoisePublicKey
		if content.Request_ID == uint32(device.ID) {
			peer.AskedForNeighbor = true
		}
		if bytes.Equal(content.PubKey[:], device.staticIdentity.publicKey[:]) {
			return nil
		}
		copy(pk[:], content.PubKey[:])
		thepeer := device.LookupPeer(pk)
		if thepeer == nil { //not exist in local
			if device.LogLevel.LogControl {
				fmt.Println("Control: Add new peer to local ID:" + content.NodeID.ToString() + " PubKey:" + pk.ToString())
			}
			if device.graph.Weight(device.ID, content.NodeID) == path.Infinity { // add node to graph
				device.graph.UpdateLatency(device.ID, content.NodeID, path.S2TD(path.Infinity), true, false)
			}
			if device.graph.Weight(content.NodeID, device.ID) == path.Infinity { // add node to graph
				device.graph.UpdateLatency(content.NodeID, device.ID, path.S2TD(path.Infinity), true, false)
			}
			device.NewPeer(pk, content.NodeID, false)
		}
		if thepeer.LastPingReceived.Add(path.S2TD(device.DRoute.PeerAliveTimeout)).Before(time.Now()) {
			//Peer died, try to switch to this new endpoint
			thepeer.Lock()
			thepeer.endpoint_trylist.LoadOrStore(content.ConnURL, time.Time{}) //another gorouting will process it
			thepeer.Unlock()
			device.event_tryendpoint <- struct{}{}
		}

	}
	return nil
}

func (device *Device) RoutineSetEndpoint() {
	if !(device.DRoute.P2P.UseP2P || device.DRoute.SuperNode.UseSuperNode) {
		return
	}
	for {
		NextRun := false
		<-device.event_tryendpoint
		for _, thepeer := range device.peers.IDMap {
			if thepeer.LastPingReceived.Add(path.S2TD(device.DRoute.PeerAliveTimeout)).After(time.Now()) {
				//Peer alives
				continue
			} else {
				thepeer.RLock()
				thepeer.endpoint_trylist.Sort(func(a *orderedmap.Pair, b *orderedmap.Pair) bool {
					return a.Value().(time.Time).Before(b.Value().(time.Time))
				})
				trylist := thepeer.endpoint_trylist.Keys()
				thepeer.RUnlock()
				for _, key := range trylist { // try next endpoint
					connurl := key
					thepeer.RLock()
					val, hasval := thepeer.endpoint_trylist.Get(key)
					thepeer.RUnlock()
					if !hasval {
						continue
					}
					trytime := val.(time.Time)
					if trytime.Sub(time.Time{}) != time.Duration(0) && time.Now().Sub(trytime) > path.S2TD(device.DRoute.ConnTimeOut) { // tried before, but no response
						thepeer.Lock()
						thepeer.endpoint_trylist.Delete(key)
						thepeer.Unlock()
					} else {
						if device.LogLevel.LogControl {
							fmt.Println("Control: Set endpoint to " + connurl + " for NodeID:" + thepeer.ID.ToString())
						}
						err := thepeer.SetEndpointFromConnURL(connurl, thepeer.ConnAF, thepeer.StaticConn) //trying to bind first url in the list and wait device.DRoute.P2P.PeerAliveTimeout seconds
						if err != nil {
							device.log.Errorf("Bind " + connurl + " failed!")
							thepeer.Lock()
							thepeer.endpoint_trylist.Delete(connurl)
							thepeer.Unlock()
							continue
						}
						NextRun = true
						thepeer.Lock()
						thepeer.endpoint_trylist.Set(key, time.Now())
						thepeer.Unlock()
						//Send Ping message to it
						go device.SendPing(thepeer, int(device.DRoute.ConnNextTry+1), 1, 1)
						break
					}
				}
			}
		}
	ClearChanLoop:
		for {
			select {
			case <-device.event_tryendpoint:
			default:
				break ClearChanLoop
			}
		}
		time.Sleep(path.S2TD(device.DRoute.ConnNextTry))
		if NextRun {
			device.event_tryendpoint <- struct{}{}
		}
	}
}

func (device *Device) RoutineSendPing() {
	if !(device.DRoute.P2P.UseP2P || device.DRoute.SuperNode.UseSuperNode) {
		return
	}
	for {
		packet, usage, _ := device.GeneratePingPacket(device.ID, 0)
		device.SpreadPacket(make(map[config.Vertex]bool), usage, packet, MessageTransportOffsetContent)
		time.Sleep(path.S2TD(device.DRoute.SendPingInterval))
	}
}

func (device *Device) RoutineRegister() {
	if !(device.DRoute.SuperNode.UseSuperNode) {
		return
	}
	_ = <-device.Event_Supernode_OK
	for {

		body, _ := path.GetByte(path.RegisterMsg{
			Node_id:       device.ID,
			PeerStateHash: device.peers.Peer_state,
			NhStateHash:   device.graph.NhTableHash,
			Version:       device.Version,
			LocalV4: net.UDPAddr{
				IP:   device.peers.LocalV4,
				Port: int(device.net.port),
			},
			LocalV6: net.UDPAddr{
				IP:   device.peers.LocalV6,
				Port: int(device.net.port),
			},
		})
		buf := make([]byte, path.EgHeaderLen+len(body))
		header, _ := path.NewEgHeader(buf[0:path.EgHeaderLen])
		header.SetDst(config.SuperNodeMessage)
		header.SetTTL(0)
		header.SetSrc(device.ID)
		header.SetPacketLength(uint16(len(body)))
		copy(buf[path.EgHeaderLen:], body)
		device.Send2Super(path.Register, buf, MessageTransportOffsetContent)
		time.Sleep(path.S2TD(device.DRoute.SendPingInterval))
	}
}

func (device *Device) RoutineRecalculateNhTable() {
	if device.graph.TimeoutCheckInterval == 0 {
		return
	}
	if device.IsSuperNode {
		for {
			changed := device.graph.RecalculateNhTable(true)
			if changed {
				device.Event_server_NhTable_changed <- struct{}{}
			}
			time.Sleep(device.graph.TimeoutCheckInterval)
		}
	} else {
		if !device.DRoute.P2P.UseP2P {
			return
		}
		for {
			if time.Now().After(device.graph.NhTableExpire) {
				device.graph.RecalculateNhTable(false)
			}
			time.Sleep(device.graph.TimeoutCheckInterval)
		}
	}
}

func (device *Device) RoutineSpreadAllMyNeighbor() {
	if !device.DRoute.P2P.UseP2P {
		return
	}
	for {
		device.process_RequestPeerMsg(path.QueryPeerMsg{
			Request_ID: uint32(config.Broadcast),
		})
		time.Sleep(path.S2TD(device.DRoute.P2P.SendPeerInterval))
	}
}

func (device *Device) RoutineResetConn() {
	if device.ResetConnInterval <= 0.01 {
		return
	}
	for {
		for _, peer := range device.peers.keyMap {
			if !peer.StaticConn { //Do not reset connecton for dynamic peer
				continue
			}
			if peer.ConnURL == "" {
				continue
			}
			err := peer.SetEndpointFromConnURL(peer.ConnURL, peer.ConnAF, peer.StaticConn)
			if err != nil {
				device.log.Errorf("Failed to bind "+peer.ConnURL, err)
				continue
			}
		}
		time.Sleep(path.S2TD(device.ResetConnInterval))
	}
}

func (device *Device) RoutineClearL2FIB() {
	if device.fibTimeout <= 0.01 {
		return
	}
	timeout := path.S2TD(device.fibTimeout)
	for {
		device.l2fib.Range(func(k interface{}, v interface{}) bool {
			val := v.(*IdAndTime)
			if time.Now().After(val.Time.Add(timeout)) {
				mac := k.(tap.MacAddress)
				device.l2fib.Delete(k)
				if device.LogLevel.LogInternal {
					fmt.Printf("Internal: L2FIB [%v -> %v] deleted.\n", mac.String(), val.ID)
				}
			}
			return true
		})
		time.Sleep(timeout)
	}
}
