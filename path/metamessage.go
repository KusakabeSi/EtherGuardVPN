package path

import (
	"bytes"
	"encoding/base64"
	"encoding/gob"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/KusakabeSi/EtherGuardVPN/config"
)

func GetByte(structIn interface{}) (bb []byte, err error) {
	var b bytes.Buffer
	e := gob.NewEncoder(&b)
	if err := e.Encode(structIn); err != nil {
		panic(err)
	}
	bb = b.Bytes()
	return
}

type RegisterMsg struct {
	Node_id       config.Vertex
	Version       string
	PeerStateHash [32]byte
	NhStateHash   [32]byte
	LocalV4       net.UDPAddr
	LocalV6       net.UDPAddr
}

func Hash2Str(h []byte) string {
	for _, v := range h {
		if v != 0 {
			return base64.StdEncoding.EncodeToString(h)[:10] + "..."
		}
	}
	return "\"\""
}

func (c *RegisterMsg) ToString() string {
	return fmt.Sprint("RegisterMsg Node_id:"+c.Node_id.ToString(), " Version:"+c.Version, " PeerHash:"+Hash2Str(c.PeerStateHash[:]), " NhHash:"+Hash2Str(c.NhStateHash[:]), " LocalV4:"+c.LocalV4.String(), " LocalV6:"+c.LocalV6.String())
}

func ParseRegisterMsg(bin []byte) (StructPlace RegisterMsg, err error) {
	var b bytes.Buffer
	b.Write(bin)
	d := gob.NewDecoder(&b)
	err = d.Decode(&StructPlace)
	return
}

type ErrorAction int

const (
	NoAction ErrorAction = iota
	Shutdown
	Panic
)

func (a *ErrorAction) ToString() string {
	if *a == Shutdown {
		return "shutdown"
	} else if *a == Panic {
		return "panic"
	}
	return "unknow"
}

type UpdateErrorMsg struct {
	Node_id   config.Vertex
	Action    ErrorAction
	ErrorCode int
	ErrorMsg  string
}

func ParseUpdateErrorMsg(bin []byte) (StructPlace UpdateErrorMsg, err error) {
	var b bytes.Buffer
	b.Write(bin)
	d := gob.NewDecoder(&b)
	err = d.Decode(&StructPlace)
	return
}

func (c *UpdateErrorMsg) ToString() string {
	return "UpdateErrorMsg Node_id:" + c.Node_id.ToString() + " Action:" + c.Action.ToString() + " ErrorCode:" + strconv.Itoa(c.ErrorCode) + " ErrorMsg " + c.ErrorMsg
}

type UpdatePeerMsg struct {
	State_hash [32]byte
}

func (c *UpdatePeerMsg) ToString() string {
	return "UpdatePeerMsg State_hash:" + string(c.State_hash[:])
}

func ParseUpdatePeerMsg(bin []byte) (StructPlace UpdatePeerMsg, err error) {
	var b bytes.Buffer
	b.Write(bin)
	d := gob.NewDecoder(&b)
	err = d.Decode(&StructPlace)
	return
}

type UpdateNhTableMsg struct {
	State_hash [32]byte
}

func (c *UpdateNhTableMsg) ToString() string {
	return "UpdateNhTableMsg State_hash:" + string(c.State_hash[:])
}

func ParseUpdateNhTableMsg(bin []byte) (StructPlace UpdateNhTableMsg, err error) {
	var b bytes.Buffer
	b.Write(bin)
	d := gob.NewDecoder(&b)
	err = d.Decode(&StructPlace)
	return
}

type PingMsg struct {
	RequestID    uint32
	Src_nodeID   config.Vertex
	Time         time.Time
	RequestReply int
}

func (c *PingMsg) ToString() string {
	return "PingMsg SID:" + c.Src_nodeID.ToString() + " Time:" + c.Time.String() + " RequestID:" + strconv.Itoa(int(c.RequestID))
}

func ParsePingMsg(bin []byte) (StructPlace PingMsg, err error) {
	var b bytes.Buffer
	b.Write(bin)
	d := gob.NewDecoder(&b)
	err = d.Decode(&StructPlace)
	return
}

type PongMsg struct {
	RequestID      uint32
	Src_nodeID     config.Vertex
	Dst_nodeID     config.Vertex
	Timediff       time.Duration
	AdditionalCost float64
}

func (c *PongMsg) ToString() string {
	return "PongMsg SID:" + c.Src_nodeID.ToString() + " DID:" + c.Dst_nodeID.ToString() + " Timediff:" + c.Timediff.String() + " RequestID:" + strconv.Itoa(int(c.RequestID))
}

func ParsePongMsg(bin []byte) (StructPlace PongMsg, err error) {
	var b bytes.Buffer
	b.Write(bin)
	d := gob.NewDecoder(&b)
	err = d.Decode(&StructPlace)
	return
}

type QueryPeerMsg struct {
	Request_ID uint32
}

func (c *QueryPeerMsg) ToString() string {
	return "QueryPeerMsg Request_ID:" + strconv.Itoa(int(c.Request_ID))
}

func ParseQueryPeerMsg(bin []byte) (StructPlace QueryPeerMsg, err error) {
	var b bytes.Buffer
	b.Write(bin)
	d := gob.NewDecoder(&b)
	err = d.Decode(&StructPlace)
	return
}

type BoardcastPeerMsg struct {
	Request_ID uint32
	NodeID     config.Vertex
	PubKey     [32]byte
	ConnURL    string
}

func (c *BoardcastPeerMsg) ToString() string {
	return "BoardcastPeerMsg Request_ID:" + strconv.Itoa(int(c.Request_ID)) + " NodeID:" + c.NodeID.ToString() + " ConnURL:" + c.ConnURL
}

func ParseBoardcastPeerMsg(bin []byte) (StructPlace BoardcastPeerMsg, err error) {
	var b bytes.Buffer
	b.Write(bin)
	d := gob.NewDecoder(&b)
	err = d.Decode(&StructPlace)
	return
}

type SUPER_Events struct {
	Event_server_pong     chan PongMsg
	Event_server_register chan RegisterMsg
}
