/*
 *  Copyright (C) 2017 gyee authors
 *
 *  This file is part of the gyee library.
 *
 *  the gyee library is free software: you can redistribute it and/or modify
 *  it under the terms of the GNU General Public License as published by
 *  the Free Software Foundation, either version 3 of the License, or
 *  (at your option) any later version.
 *
 *  the gyee library is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *  GNU General Public License for more details.
 *
 *  You should have received a copy of the GNU General Public License
 *  along with the gyee library.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package neighbor

import (
	"os"
	"syscall"
	"net"
	"fmt"
	"time"
	"sync"
	sch		"github.com/yeeco/gyee/p2p/scheduler"
	config	"github.com/yeeco/gyee/p2p/config"
	umsg	"github.com/yeeco/gyee/p2p/discover/udpmsg"
	log	"github.com/yeeco/gyee/p2p/logger"
)

// the listener task name
const LsnMgrName = sch.NgbLsnName

type listenerConfig struct {
	IP	net.IP			// IP
	UDP	uint16			// UDP port number
	TCP	uint16			// TCP port number
	ID	config.NodeID	// node identity: the public key
}

type ListenerManager struct {
	sdl			*sch.Scheduler		// pointer to scheduler
	name		string				// name
	tep			sch.SchUserTaskEp	// entry
	cfg			listenerConfig		// configuration
	conn		*net.UDPConn		// udp connection
	addr		net.UDPAddr			// real udp address
	state		int					// state
	ptnMe		interface{}			// pointer to myself task
	ptnReader	interface{}			// pointer to udp reader task
	lock		sync.Mutex			// lock for stop udp reader
}

// listener manager task state
const (
	LmsNull		= iota		// not be inited, configurations are all invalid
	LmsInited				// configurated but not started
	LmsStarted				// in running
	LmsStopped				// stopped, configurations are still validate
)

func NewLsnMgr() *ListenerManager {
	var lsnMgr = ListenerManager{
		name:      LsnMgrName,
		state:     LmsNull,
	}
	lsnMgr.tep = lsnMgr.lsnMgrProc
	return &lsnMgr
}

func (lsnMgr *ListenerManager)TaskProc4Scheduler(ptn interface{}, msg *sch.SchMessage) sch.SchErrno {
	return lsnMgr.tep(ptn, msg)
}

func (lsnMgr *ListenerManager)lsnMgrProc(ptn interface{}, msg *sch.SchMessage) sch.SchErrno {
	var eno = sch.SchEnoUnknown

	switch msg.Id {
	case sch.EvSchPoweron:
		eno = lsnMgr.procPoweron(ptn)
	case sch.EvSchPoweroff:
		eno = lsnMgr.procPoweroff(ptn)
	case sch.EvNblStart:
		eno = lsnMgr.procStart()
	case sch.EvNblStop:
		eno = lsnMgr.procStop()
	case sch.EvNblDataReq:
		eno = lsnMgr.nblDataReq(ptn, msg.Body)
	default:
		log.LogCallerFileLine("LsnMgrProc: unknown message: %d", msg.Id)
		return sch.SchEnoMismatched
	}

	return eno
}

func (lsnMgr *ListenerManager) setupConfig() sch.SchErrno {
	var ptCfg *config.Cfg4UdpNgbListener = nil
	if ptCfg = config.P2pConfig4UdpNgbListener(lsnMgr.sdl.SchGetP2pCfgName()); ptCfg == nil {
		return sch.SchEnoConfig
	}
	lsnMgr.cfg.IP	= ptCfg.IP
	lsnMgr.cfg.UDP	= ptCfg.UDP
	lsnMgr.cfg.TCP	= ptCfg.TCP
	lsnMgr.cfg.ID	= ptCfg.ID
	return sch.SchEnoNone
}

func (lsnMgr *ListenerManager)setupUdpConn() sch.SchErrno {
	var conn		*net.UDPConn = nil
	var realAddr	*net.UDPAddr = nil

	strAddr := fmt.Sprintf("%s:%d", lsnMgr.cfg.IP.String(), lsnMgr.cfg.UDP)
	udpAddr, err := net.ResolveUDPAddr("udp", strAddr)
	if err != nil {
		log.LogCallerFileLine("setupUdpConn: ResolveUDPAddr failed, err: %s", err.Error())
		return sch.SchEnoOS
	}

	conn, err = net.ListenUDP("udp", udpAddr)
	if err != nil || conn == nil {
		log.LogCallerFileLine("setupUdpConn: ListenUDP failed, err: %s", err.Error())
		return sch.SchEnoOS
	}

	realAddr = conn.LocalAddr().(*net.UDPAddr)
	if realAddr == nil {
		log.LogCallerFileLine("setupUdpConn: LocalAddr failed")
		return sch.SchEnoOS
	}
	log.LogCallerFileLine("setupUdpConn: real address: %s", realAddr.String())

	lsnMgr.addr = *realAddr
	lsnMgr.conn = conn
	return sch.SchEnoNone
}

func (lsnMgr *ListenerManager) start() sch.SchErrno {
	var eno sch.SchErrno
	var msg sch.SchMessage
	if eno = lsnMgr.canStart(); eno != sch.SchEnoNone {
		log.LogCallerFileLine("start: could not start, eno: %d", eno)
		return eno
	}
	lsnMgr.sdl.SchMakeMessage(&msg, lsnMgr.ptnMe, lsnMgr.ptnMe, sch.EvNblStart, nil)
	lsnMgr.sdl.SchSendMessage(&msg)
	return sch.SchEnoNone
}

func (lsnMgr *ListenerManager) nextState(s int) sch.SchErrno {
	lsnMgr.state = s
	return sch.SchEnoNone
}

func (lsnMgr *ListenerManager) canStart() sch.SchErrno {
	if lsnMgr.state == LmsInited || lsnMgr.state == LmsStopped {
		return sch.SchEnoNone
	}
	return sch.SchEnoMismatched
}

func (lsnMgr *ListenerManager) canStop() sch.SchErrno {
	if lsnMgr.state == LmsStarted &&
		lsnMgr.ptnReader != nil &&
		lsnMgr.conn != nil	{
		return sch.SchEnoNone
	}
	return sch.SchEnoMismatched
}

func (lsnMgr *ListenerManager) procPoweron(ptn interface{}) sch.SchErrno {
	var eno sch.SchErrno
	lsnMgr.ptnMe = ptn
	lsnMgr.sdl = sch.SchGetScheduler(ptn)
	sdl := lsnMgr.sdl

	if sdl.SchGetP2pConfig().NetworkType == config.P2pNetworkTypeStatic {
		log.LogCallerFileLine("procPoweron: static type, lsnMgr is not needed, done it ...")
		return sdl.SchTaskDone(ptn, sch.SchEnoNone)
	}

	lsnMgr.nextState(LmsNull)
	if eno = lsnMgr.setupConfig(); eno != sch.SchEnoNone {
		log.LogCallerFileLine("procPoweron：setupConfig failed, eno: %d", eno)
		return eno
	}

	lsnMgr.nextState(LmsInited)
	if eno = lsnMgr.start(); eno != sch.SchEnoNone {
		log.LogCallerFileLine("procPoweron：start failed, eno: %d", eno)
	}

	return eno
}

func (lsnMgr *ListenerManager) procPoweroff(ptn interface{}) sch.SchErrno {
	log.LogCallerFileLine("procPoweroff: task will be done, name: %s", lsnMgr.sdl.SchGetTaskName(ptn))
	if eno := lsnMgr.procStop(); eno != sch.SchEnoNone {
		log.LogCallerFileLine("procPoweroff: procStop failed, eno: %d", eno)
		return eno
	}
	return lsnMgr.sdl.SchTaskDone(lsnMgr.ptnMe, sch.SchEnoKilled)
}

func (lsnMgr *ListenerManager) procStart() sch.SchErrno {
	var eno = sch.SchEnoUnknown
	var ptnLoop interface{} = nil

	if eno = lsnMgr.setupUdpConn(); eno != sch.SchEnoNone {
		log.LogCallerFileLine("procStart：setupUdpConn failed, eno: %d", eno)
		return eno
	}

	var udpReader = NewUdpReader()
	udpReader.lsnMgr = lsnMgr
	udpReader.sdl = lsnMgr.sdl
	udpReader.conn = lsnMgr.conn
	eno, ptnLoop = lsnMgr.sdl.SchCreateTask(&udpReader.desc)
	if eno != sch.SchEnoNone {
		log.LogCallerFileLine("procStart: SchCreateTask failed, eno: %d, ptn: %p", eno, ptnLoop)
		return eno
	}

	lsnMgr.ptnReader = ptnLoop
	return lsnMgr.nextState(LmsStarted)
}

func (lsnMgr *ListenerManager) procStop() sch.SchErrno {
	lsnMgr.lock.Lock()
	defer lsnMgr.lock.Unlock()

	if eno := lsnMgr.canStop(); eno != sch.SchEnoNone {
		log.LogCallerFileLine("procStop: we can't stop, eno: %d", eno)
		return eno
	}

	lsnMgr.conn.Close()
	lsnMgr.conn = nil
	lsnMgr.sdl.SchStopTask(lsnMgr.ptnReader)
	return lsnMgr.nextState(LmsStopped)
}

func (lsnMgr *ListenerManager)nblDataReq(ptn interface{}, msg interface{}) sch.SchErrno {
	req := msg.(*sch.NblDataReq)
	return lsnMgr.sendUdpMsg(req.Payload, req.TgtAddr)
}

// Reader task on UDP connection
const udpReaderName = sch.NgbReaderName
const udpMaxMsgSize = 1024 * 32

var noDog = sch.SchWatchDog {
	HaveDog:false,
}

type UdpReaderTask struct {
	sdl			*sch.Scheduler			// pointer to scheduler
	lsnMgr		*ListenerManager		// pointer to listener manager
	name		string					// name
	tep			sch.SchUserTaskEp		// entry
	conn		*net.UDPConn			// udp connection
	ptnMe		interface{}				// pointer to myself task
	ptnNgbMgr	interface{}				// pointer to neighbor manager task
	desc		sch.SchTaskDescription	// description
	udpMsg		*umsg.UdpMsg			// decode/encode wrapper
}

type UdpMsgInd struct {
	msgType		umsg.UdpMsgType			// message type
	msgBody		interface{}				// message body, like Ping, Pong, ... see udpmsg.go
}

func NewUdpReader() *UdpReaderTask {
	var udpReader = UdpReaderTask {
		name: udpReaderName,
		tep:  nil,
		conn: nil,
		desc: sch.SchTaskDescription{
			Name:   udpReaderName,
			MbSize: 0,
			Ep:     nil,
			Wd:     &noDog,
			Flag:   sch.SchCreatedGo,
			DieCb:  nil,
		},
		udpMsg: umsg.NewUdpMsg(),
	}
	udpReader.tep		= udpReader.udpReaderLoop
	udpReader.desc.Ep	= &udpReader
	return &udpReader
}

func (udpReader *UdpReaderTask)TaskProc4Scheduler(ptn interface{}, msg *sch.SchMessage) sch.SchErrno {
	return udpReader.tep(ptn, msg)
}

func (udpReader *UdpReaderTask)udpReaderLoop(ptn interface{}, msg *sch.SchMessage) sch.SchErrno {

	var _ = msg
	var eno = sch.SchEnoNone
	buf := make([]byte, udpMaxMsgSize)

	udpReader.ptnMe = ptn
	_, udpReader.ptnNgbMgr = udpReader.sdl.SchGetTaskNodeByName(NgbMgrName)

	//
	// We just read until errors fired from udp, for example, when
	// the mamager is asked to stop the reader, it can close the
	// connection. See function procStop for details please.
	//
	// When a message recevied, the reader decode it to get an UDP
	// discover protocol message, it than create a protocol task to
	// deal with the message received.
	//

_loop:

	for {

		if NgbProtoReadTimeout > 0 {
			udpReader.conn.SetReadDeadline(time.Now().Add(NgbProtoReadTimeout))
		}
		bys, peer, err := udpReader.conn.ReadFromUDP(buf)

		// check error
		if err != nil {
			if udpReader.canErrIgnored(err) != true {
				log.LogCallerFileLine("udpReaderLoop: broken, err: %s", err.Error())
				eno = sch.SchEnoOS
				break _loop
			}
		} else {
			udpReader.msgHandler(&buf, bys, peer)
		}
	}

	//
	// here we get out, but this might be caused by abnormal cases than we
	// are closed by manager task, we check this: if it is the later, the
	// connection pointer held by manager must be nil, see lsnMgr.procStop
	// for details pls.
	//
	// if it's an abnormal case that the reader task still in running, we
	// need to make it done.
	//

	if udpReader.lsnMgr.conn == nil {
		log.LogCallerFileLine("udpReaderLoop: seems we are closed by manager task")
	} else {
		log.LogCallerFileLine("udpReaderLoop: abnormal case, stop the task")
		eno = udpReader.lsnMgr.procStop()
	}

	log.LogCallerFileLine("udpReaderLoop: exit ...")
	udpReader.conn = nil
	return eno
}

func (rd *UdpReaderTask) canErrIgnored(err error) bool {
	const WSAEMSGSIZE = syscall.Errno(10040)
	if opErr, ok := err.(*net.OpError); ok {
		if opErr.Temporary() {
			return true
		}
		if sce, ok := opErr.Err.(*os.SyscallError); ok {
			if sce.Err == WSAEMSGSIZE {
				return true
			}
		}
	}
	return false
}

func (rd *UdpReaderTask) msgHandler(pbuf *[]byte, len int, from *net.UDPAddr) sch.SchErrno {
	var msg sch.SchMessage
	var eno umsg.UdpMsgErrno

	if eno := rd.udpMsg.SetRawMessage(pbuf, len, from); eno != umsg.UdpMsgEnoNone {
		return sch.SchEnoUserTask
	}

	if eno = rd.udpMsg.Decode(); eno != umsg.UdpMsgEnoNone {
		return sch.SchEnoUserTask
	}

	udpMsgInd := UdpMsgInd {
		msgType:rd.udpMsg.GetDecodedMsgType(),
		msgBody:rd.udpMsg.GetDecodedMsg(),
	}

	if rd.udpMsg.CheckUdpMsgFromPeer(from) != true {
		return sch.SchEnoUserTask
	}

	rd.sdl.SchMakeMessage(&msg, rd.ptnMe, rd.ptnNgbMgr, sch.EvNblMsgInd, &udpMsgInd)
	rd.sdl.SchSendMessage(&msg)
	return sch.SchEnoNone
}

func (lsnMgr *ListenerManager)sendUdpMsg(buf []byte, toAddr *net.UDPAddr) sch.SchErrno {
	if lsnMgr.conn == nil {
		log.LogCallerFileLine("sendUdpMsg: invalid UDP connection")
		return sch.SchEnoInternal
	}

	if len(buf) == 0 || toAddr == nil {
		log.LogCallerFileLine("sendUdpMsg: empty to send")
		return sch.SchEnoParameter
	}

	if err := lsnMgr.conn.SetWriteDeadline(time.Now().Add(NgbProtoWriteTimeout)); err != nil {
		log.LogCallerFileLine("sendUdpMsg: SetDeadline failed, err: %s", err.Error())
		return sch.SchEnoOS
	}

	sent, err := lsnMgr.conn.WriteToUDP(buf, toAddr)
	if err != nil {
		log.LogCallerFileLine("sendUdpMsg: WriteToUDP failed, err: %s", err.Error())
		return sch.SchEnoOS
	}

	if sent != len(buf) {
		log.LogCallerFileLine("sendUdpMsg: WriteToUDP failed, len: %d, sent: %d", len(buf), sent)
		return sch.SchEnoOS
	}

	return sch.SchEnoNone
}

func sendUdpMsg(sdl *sch.Scheduler, lsn interface{}, sender interface{}, buf []byte, toAddr *net.UDPAddr) sch.SchErrno {
	var schMsg = sch.SchMessage{}
	req := sch.NblDataReq {
		Payload:	buf,
		TgtAddr:	toAddr,
	}
	sdl.SchMakeMessage(&schMsg, sender, lsn, sch.EvNblDataReq, &req)
	sdl.SchSendMessage(&schMsg)
	return sch.SchEnoNone
}