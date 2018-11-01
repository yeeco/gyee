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


package main


import (
	"os"
	"time"
	"fmt"
	"net"
	"sync"
	"math/rand"
	"os/signal"
	"bytes"
	"errors"
	golog	"log"
	"crypto/sha256"
	shell	"github.com/yeeco/gyee/p2p/shell"
	peer	"github.com/yeeco/gyee/p2p/peer"
	config	"github.com/yeeco/gyee/p2p/config"
	log		"github.com/yeeco/gyee/p2p/logger"
	sch		"github.com/yeeco/gyee/p2p/scheduler"
	dht		"github.com/yeeco/gyee/p2p/dht"
)

//
// Configuration pointer
//
var p2pName2Cfg = make(map[string]*config.Config)
var p2pInst2Cfg = make(map[*sch.Scheduler]*config.Config)

var dhtName2Cfg = make(map[string]*config.Config)
var dhtInst2Cfg = make(map[*sch.Scheduler]*config.Config)

//
// Indication/Package handlers
//
var (
	p2pIndHandler peer.P2pIndCallback = p2pIndProc
	p2pPkgHandler peer.P2pPkgCallback = p2pPkgProc
)

//
// extend peer identity
//
type peerIdEx struct {
	subNetId	peer.SubNetworkID
	nodeId		peer.PeerId
	dir			int
}

//
// test statistics
//
type testCaseCtrlBlock struct {
	peerId	peerIdEx
	doneTx 	chan bool
	doneRx	chan bool
	killing	bool
	txSeq	int64
	rxSeq	int64
}

//
// Done signal for Tx routines
//
var doneMapLock sync.Mutex
var indCbLock sync.Mutex
var doneMap = make(map[*sch.Scheduler]map[peerIdEx]*testCaseCtrlBlock)

//
// test case
//
type testCase struct {
	name		string
	description	string
	entry		func(tc *testCase)
}

//
// test case table
//
var testCaseTable = []testCase{
	{
		name:			"testCase0",
		description:	"common case for AnySubNet",
		entry:			testCase0,
	},
	{
		name:			"testCase1",
		description:	"multiple p2p instances",
		entry:			testCase1,
	},
	{
		name:			"testCase2",
		description:	"static network without any dynamic sub networks",
		entry:			testCase2,
	},
	{
		name:			"testCase3",
		description:	"multiple sub networks without a static network",
		entry:			testCase3,
	},
	{
		name:			"testCase4",
		description:	"multiple sub networks with a static network",
		entry:			testCase4,
	},
	{
		name:			"testCase5",
		description:	"stop p2p instance",
		entry:			testCase5,
	},
	{
		name:			"testCase6",
		description:	"dht common test",
		entry:			testCase6,
	},
	{
		name:			"testCase7",
		description:	"stop dht instance",
		entry:			testCase7,
	},
}

//
// target case
//
var tgtCase = "testCase5"

//
// port base
//
const (
	portBase4P2p = 30303
	portBase4Dht = 40404
)

//
// create test case control block by name
//
func newTcb(name string, idEx peerIdEx) *testCaseCtrlBlock {
	tcb := testCaseCtrlBlock {
		peerId: idEx,
		doneTx: make(chan bool),
		doneRx:	make(chan bool),
		txSeq:	0,
		rxSeq:	0,
	}
	switch name {
	case "testCase0":
	case "testCase1":
	case "testCase2":
	case "testCase3":
	case "testCase4":
	case "testCase5":
	case "testCase6":
	case "testCase7":
	default:
		log.Debug("newTcb: undefined test: %s", name)
		return nil
	}
	return &tcb
}

//
// Tx routine
//
var dataTxApply = true
var dataTxTmApply = true

func txStopAll() {
	doneMapLock.Lock()
	defer doneMapLock.Unlock()
	instKeys := make([]*sch.Scheduler, 0)
	for ik, tcbList := range doneMap {
		instKeys = append(instKeys, ik)
		tcbKeys := make([]peerIdEx, 0)
		for tk, tcb := range tcbList {
			if !tcb.killing {
				tcb.killing = true
				tcb.doneTx<-true
				<-tcb.doneTx
				tcb.doneRx<-true
				<-tcb.doneRx
				tcbKeys = append(tcbKeys, tk)
			}
		}
		for _, tk := range tcbKeys {
			delete(tcbList, tk)
		}
	}
	for _, ik := range instKeys {
		delete(doneMap, ik)
	}
}

func isAllDone() bool {
	doneMapLock.Lock()
	defer doneMapLock.Unlock()
	return len(doneMap) == 0
}

func txProc(p2pInst *sch.Scheduler, dir int, snid peer.SubNetworkID, id peer.PeerId) {
	// This demo simply apply timer with 1s cycle and then sends a string
	// again and again; The "done" signal is also checked to determine if
	// task is done. See bellow pls.
	sdl := p2pInst.SchGetP2pCfgName()
	idEx := peerIdEx {
		subNetId:	snid,
		nodeId:		id,
		dir:		dir,
	}
	doneMapLock.Lock()
	tcb, exist := doneMap[p2pInst][idEx]
	if !exist {
		log.Debug("txProc: no exist, sdl: %s, dir: %d, subnet: %s, id: %s",
			sdl, dir, fmt.Sprintf("%x", snid), fmt.Sprintf("%x", id))
		doneMapLock.Unlock()
		return
	}
	doneMapLock.Unlock()

	pkg := peer.P2pPackage2Peer {
		P2pInst:		p2pInst,
		IdList: 		make([]peer.PeerId, 0),
		ProtoId:		int(peer.PID_EXT),
		PayloadLength:	0,
		Payload:		make([]byte, 0, 512),
		Extra:			nil,
	}

	log.Debug("txProc: enter, sdl: %s, dir: %d, subnet: %s, id: %s",
		sdl, dir, fmt.Sprintf("%x", snid), fmt.Sprintf("%x", id))

	var tmHandler = func() {
		doneMapLock.Lock()
		tcb.txSeq++
		if dataTxApply {
			pkg.IdList = make([]peer.PeerId, 1)
			for id := range doneMap[p2pInst] {
				txString := fmt.Sprintf(">>>>>> \nseq:%d\n"+
					"to: subnet: %s\n, id: %s\n",
					tcb.txSeq,
					fmt.Sprintf("%x", snid),
					fmt.Sprintf("%x", id))
				pkg.SubNetId = id.subNetId
				pkg.IdList[0] = id.nodeId
				pkg.Payload = []byte(txString)
				pkg.PayloadLength = len(pkg.Payload)
				if eno := shell.P2pSendPackage(&pkg); eno != shell.P2pEnoNone {
					log.Debug("txProc: "+
						"send package failed, eno: %d, subnet: %s, id: %s",
						eno,
						fmt.Sprintf("%x", snid),
						fmt.Sprintf("%x", id))
				}
			}
		}
		doneMapLock.Unlock()
	}

	tm := time.NewTicker(time.Second * 1)
	defer tm.Stop()

	doneOk := true

txLoop:
	for {
		select {
		case _, doneOk = <-tcb.doneTx:
			log.Debug("txProc: it's done, subnet: %s, id: %s",
				fmt.Sprintf("%x", snid),
				fmt.Sprintf("%x", id))
			break txLoop
		case <-tm.C:
			indCbLock.Lock()
			if dataTxTmApply {
				tmHandler()
			}
			indCbLock.Unlock()
		}
	}

	if doneOk {
		close(tcb.doneTx)
	}

	log.Debug("txProc: exit, sdl: %s, dir: %d, subnet: %s, id: %s",
		sdl, dir, fmt.Sprintf("%x", snid), fmt.Sprintf("%x", id))
}

func rxProc(p2pInst *sch.Scheduler, rxChan chan *peer.P2pPackageRx, dir int, snid peer.SubNetworkID, id peer.PeerId) {
	sdl := p2pInst.SchGetP2pCfgName()
	idEx := peerIdEx {
		subNetId: snid,
		nodeId: id,
		dir: dir,
	}
	doneMapLock.Lock()
	tcb, exist := doneMap[p2pInst][idEx]
	if !exist {
		log.Debug("rxProc: not exist, sdl: %s, dir: %d, subnet: %s, id: %s",
			sdl, dir, fmt.Sprintf("%x", snid), fmt.Sprintf("%x", id))
		doneMapLock.Unlock()
		return
	}
	doneMapLock.Unlock()

	doneOk := true

	log.Debug("rxProc: enter, sdl: %s, dir: %d, subnet: %s, id: %s",
		sdl, dir, fmt.Sprintf("%x", snid), fmt.Sprintf("%x", id))

rxloop:
	for {
		select {
		case _, ok := <-rxChan:
			if !ok {
				log.Debug("rxProc: sdl: %s, rxChan closed, break", sdl)
				break rxloop
			}
			tcb.rxSeq += 1
		case _, doneOk = <-tcb.doneRx:
			break rxloop
		}
	}

	if doneOk {
		close(tcb.doneRx)
	}

	log.Debug("rxProc: exit, sdl: %s, dir: %d, subnet: %s, id: %s",
		sdl, dir, fmt.Sprintf("%x", snid), fmt.Sprintf("%x", id))
}

//
// Indication handler
//
func p2pIndProc(what int, para interface{}) interface{} {
	indCbLock.Lock()
	defer indCbLock.Unlock()
	// check what is indicated
	switch what {
	case shell.P2pIndPeerActivated:
		// a peer is activated to work, so one can install the incoming packages
		// handler.
		pap := para.(*peer.P2pIndPeerActivatedPara)
		p2pInst := sch.SchGetScheduler(pap.Ptn)
		snid := pap.PeerInfo.Snid
		peerId := pap.PeerInfo.NodeId
		dir := pap.PeerInfo.Dir
		idEx := peerIdEx {
			subNetId:	snid,
			nodeId:		peerId,
			dir:		dir,
		}
		doneMapLock.Lock()
		if _, exist := doneMap[p2pInst]; exist == false {
			doneMap[p2pInst] = make(map[peerIdEx] *testCaseCtrlBlock, 0)
		}
		if _, dup := doneMap[p2pInst][idEx]; dup == true {
			log.Debug("p2pIndProc: duplicated, subnet: %s, id: %s",
				fmt.Sprintf("%x", snid), 	fmt.Sprintf("%x", peerId))
			doneMapLock.Unlock()
			return nil
		}
		tcb := newTcb(tgtCase, idEx)
		doneMap[p2pInst][idEx] = tcb
		doneMapLock.Unlock()

		log.Debug("p2pIndProc: start tx/rx, dir: %d, snid: %x, peer: %x", dir, snid, peerId)
		go txProc(p2pInst, dir, snid, peerId)
		go rxProc(p2pInst, pap.RxChan, dir, snid, peerId)

	case shell.P2pIndPeerClosed:
		// Peer connection had been closed, one can clean his working context, see
		// bellow statements please.
		pcp := para.(*peer.P2pIndPeerClosedPara)
		p2pInst := sch.SchGetScheduler(pcp.Ptn)
		doneMapLock.Lock()
		defer doneMapLock.Unlock()

		idEx := peerIdEx{subNetId:pcp.Snid, nodeId:pcp.PeerId}
		if tcb, exist := doneMap[p2pInst][idEx]; exist {
			if !tcb.killing {
				log.Debug("p2pIndProc: try to kill, subnet: %s, id: %s",
					fmt.Sprintf("%x", pcp.Snid),
					fmt.Sprintf("%X", pcp.PeerId))
				tcb.killing = true
				tcb.doneTx<-true
				<-tcb.doneTx
				tcb.doneRx<-true
				<-tcb.doneRx
				delete(doneMap[p2pInst], idEx)
				break
			} else {
				log.Debug("p2pIndProc: in killing, subnet: %s, id: %s",
					fmt.Sprintf("%x", pcp.Snid),
					fmt.Sprintf("%X", pcp.PeerId))
			}
		}
	default:
		log.Debug("p2pIndProc: inknown indication: %d", what)
	}
	return para
}

//
// Package handler
//
func p2pPkgProc(pkg *peer.P2pPackageRx) interface{} {
	p2pInst := sch.SchGetScheduler(pkg.Ptn)
	snid := pkg.PeerInfo.Snid
	peerId := pkg.PeerInfo.NodeId

	doneMapLock.Lock()
	defer doneMapLock.Unlock()

	if _, exist := doneMap[p2pInst]; !exist {
		log.Debug("p2pPkgProc: " +
			"not activated, subnet: %s, id: %s",
			fmt.Sprintf("%x", snid),
			fmt.Sprintf("%X", peerId))
		return nil
	}
	idEx := peerIdEx{subNetId:snid, nodeId:peerId}
	tcb, exist := doneMap[p2pInst][idEx]
	if !exist {
		log.Debug("p2pPkgProc: " +
			"not activated, subnet: %s, id: %s",
			fmt.Sprintf("%x", snid),
			fmt.Sprintf("%X", peerId))
		return nil
	}
	tcb.rxSeq++
	return nil
}

//
// hook a system interrupt signal and wait on it
//
func waitInterrupt() {
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt)
	defer signal.Stop(sigc)
	<-sigc
}

//
// run target case
//
func main() {
	for _, tc := range testCaseTable {
		if tc.name == tgtCase {
			tc.entry(&tc)
			return
		}
	}
	log.Debug("main: target case not found: %s", tgtCase)
}

//
// testCase0
//
func testCase0(tc *testCase) {

	log.Debug("testCase0: going to start ycp2p...")

	// fetch default from underlying
	dftCfg := shell.ShellDefaultConfig()
	if dftCfg == nil {
		log.Debug("testCase0: ShellDefaultConfig failed")
		return
	}

	// one can then apply his configurations based on the default by calling
	// ShellSetConfig with a defferent configuration if he likes to. notice
	// that a configuration name also returned.
	myCfg := *dftCfg
	myCfg.AppType = config.P2P_TYPE_CHAIN
	cfgName := "p2pInst0"
	cfgName, _ = shell.ShellSetConfig(cfgName, &myCfg)
	p2pName2Cfg[cfgName] = shell.ShellGetConfig(cfgName)

	// init underlying p2p logic, an instance of p2p returned
	p2pInst, eno := shell.P2pCreateInstance(p2pName2Cfg[cfgName])
	if eno != sch.SchEnoNone {
		log.Debug("testCase0: SchSchedulerInit failed, eno: %d", eno)
		return
	}
	p2pInst2Cfg[p2pInst] = p2pName2Cfg[cfgName]

	// start p2p instance
	if eno = shell.P2pStart(p2pInst); eno != sch.SchEnoNone {
		log.Debug("testCase0: P2pStart failed, eno: %d", eno)
		return
	}

	// register indication handler. notice that please, the indication handler is a
	// global object for all peers connected, while the incoming packages callback
	// handler is owned by every peer, and it can be installed while activation of
	// a peer is indicated. See demo indication handler p2pIndHandler and incoming
	// package handler p2pPkgHandler for more please.
	if eno := shell.P2pRegisterCallback(shell.P2pIndCb, p2pIndHandler, p2pInst);
	eno != shell.P2pEnoNone {
		log.Debug("testCase0: P2pRegisterCallback failed, eno: %d", eno)
		return
	}
	log.Debug("testCase0: ycp2p started, cofig: %s", cfgName)

	// wait to be interrupted
	waitInterrupt()
}

//
// testCase1
//
func testCase1(tc *testCase) {
	log.Debug("testCase1: going to start ycp2p ...")

	var p2pInstNum = 16
	var bootstrapIp net.IP
	var bootstrapId = ""
	var bootstrapUdp uint16 = 0
	var bootstrapTcp uint16 = 0
	var bootstrapNodes = []*config.Node{}
	for loop := 0; loop < p2pInstNum; loop++ {
		cfgName := fmt.Sprintf("p2pInst%d", loop)
		log.Debug("testCase1: handling configuration:%s ...", cfgName)
		dftCfg := shell.ShellDefaultConfig()
		if dftCfg == nil {
			log.Debug("testCase1: ShellDefaultConfig failed")
			return
		}

		myCfg := *dftCfg
		myCfg.AppType = config.P2P_TYPE_CHAIN
		myCfg.Name = cfgName
		myCfg.Local.UDP = uint16(portBase4P2p + loop)
		myCfg.Local.TCP = uint16(portBase4P2p + loop)
		if loop == 0 {
			myCfg.NoDial = true
			myCfg.BootstrapNode = true
		}

		myCfg.BootstrapNodes = nil
		if loop != 0 {
			myCfg.BootstrapNodes = append(myCfg.BootstrapNodes, bootstrapNodes...)
		}

		cfgName, _ = shell.ShellSetConfig(cfgName, &myCfg)
		p2pName2Cfg[cfgName] = shell.ShellGetConfig(cfgName)

		if loop == 0 {
			bootstrapIp = p2pName2Cfg[cfgName].Local.IP
			bootstrapId = fmt.Sprintf("%X", p2pName2Cfg[cfgName].Local.ID)
			bootstrapUdp = p2pName2Cfg[cfgName].Local.UDP
			bootstrapTcp = p2pName2Cfg[cfgName].Local.TCP

			ipv4 := bootstrapIp.To4()
			url := []string {
				fmt.Sprintf("%s@%d.%d.%d.%d:%d:%d",
					bootstrapId,
					ipv4[0],ipv4[1],ipv4[2],ipv4[3],
					bootstrapUdp,
					bootstrapTcp),
			}
			bootstrapNodes = append(bootstrapNodes,config.P2pSetupBootstrapNodes(url)...)
		}

		p2pInst, eno := shell.P2pCreateInstance(p2pName2Cfg[cfgName])
		if eno != sch.SchEnoNone {
			log.Debug("testCase1: SchSchedulerInit failed, eno: %d", eno)
			return
		}
		p2pInst2Cfg[p2pInst] = p2pName2Cfg[cfgName]

		if eno = shell.P2pStart(p2pInst); eno != sch.SchEnoNone {
			log.Debug("testCase1: P2pStart failed, eno: %d", eno)
			return
		}

		if eno := shell.P2pRegisterCallback(shell.P2pIndCb, p2pIndHandler, p2pInst);
			eno != shell.P2pEnoNone {
			log.Debug("testCase1: P2pRegisterCallback failed, eno: %d", eno)
			return
		}

		log.Debug("testCase1: ycp2p started, cofig: %s", cfgName)
	}

	waitInterrupt()
}

//
// testCase2
//
func testCase2(tc *testCase) {

	log.Debug("testCase2: going to start ycp2p ...")

	var p2pInstNum = 8
	var cfgName = ""

	var staticNodeIdList = []*config.Node{}

	for loop := 0; loop < p2pInstNum; loop++ {

		cfgName = fmt.Sprintf("p2pInst%d", loop)
		log.Debug("testCase2: prepare node identity: %s ...", cfgName)

		dftCfg := shell.ShellDefaultConfig()
		if dftCfg == nil {
			log.Debug("testCase2: ShellDefaultConfig failed")
			return
		}

		myCfg := *dftCfg
		myCfg.AppType = config.P2P_TYPE_CHAIN
		myCfg.Name = cfgName
		myCfg.PrivateKey = nil
		myCfg.PublicKey = nil

		if config.P2pSetupLocalNodeId(&myCfg) != config.PcfgEnoNone {
			log.Debug("testCase2: P2pSetupLocalNodeId failed")
			return
		}

		log.Debug("testCase2: cfgName: %s, id: %X",
			cfgName, myCfg.Local.ID)

		n := config.Node{
			UDP:	uint16(portBase4P2p + loop),
			TCP:	uint16(portBase4P2p + loop),
			ID:		myCfg.Local.ID,
		}

		staticNodeIdList = append(staticNodeIdList, &n)
	}

	for loop := 0; loop < p2pInstNum; loop++ {

		cfgName := fmt.Sprintf("p2pInst%d", loop)
		log.Debug("testCase2: handling configuration:%s ...", cfgName)

		dftCfg := shell.ShellDefaultConfig()
		if dftCfg == nil {
			log.Debug("testCase2: ShellDefaultConfig failed")
			return
		}

		myCfg := *dftCfg
		myCfg.AppType = config.P2P_TYPE_CHAIN
		myCfg.Name = cfgName
		myCfg.PrivateKey = nil
		myCfg.PublicKey = nil
		myCfg.NetworkType = config.P2pNetworkTypeStatic
		myCfg.StaticNetId = config.ZeroSubNet
		myCfg.Local = *staticNodeIdList[loop]

		for idx, n := range staticNodeIdList {
			if idx != loop {
				myCfg.StaticNodes = append(myCfg.StaticNodes, n)
			}
		}

		myCfg.StaticMaxPeers = len(myCfg.StaticNodes) * 2
		myCfg.StaticMaxOutbounds = len(myCfg.StaticNodes)
		myCfg.StaticMaxInbounds = len(myCfg.StaticNodes)

		cfgName, _ = shell.ShellSetConfig(cfgName, &myCfg)
		p2pName2Cfg[cfgName] = shell.ShellGetConfig(cfgName)

		p2pInst, eno := shell.P2pCreateInstance(p2pName2Cfg[cfgName])
		if eno != sch.SchEnoNone {
			log.Debug("testCase2: SchSchedulerInit failed, eno: %d", eno)
			return
		}

		p2pInst2Cfg[p2pInst] = p2pName2Cfg[cfgName]
	}

	var p2pInstList = []*sch.Scheduler{}
	for p2pInst := range p2pInst2Cfg {
		p2pInstList = append(p2pInstList, p2pInst)
	}

	for piNum := len(p2pInstList); piNum > 0; piNum-- {

		time.Sleep(time.Second * 2)

		pidx := rand.Intn(piNum)
		p2pInst := p2pInstList[pidx]
		p2pInstList = append(p2pInstList[0:pidx], p2pInstList[pidx+1:]...)

		if eno := shell.P2pStart(p2pInst); eno != sch.SchEnoNone {
			log.Debug("testCase2: P2pStart failed, eno: %d", eno)
			return
		}

		if eno := shell.P2pRegisterCallback(shell.P2pIndCb, p2pIndHandler, p2pInst);
			eno != shell.P2pEnoNone {
			log.Debug("testCase2: P2pRegisterCallback failed, eno: %d", eno)
			return
		}

		log.Debug("testCase2: ycp2p started, cofig: %s", cfgName)
	}

	waitInterrupt()
}

//
// testCase3
//
func testCase3(tc *testCase) {

	log.Debug("testCase3: going to start ycp2p ...")

	var p2pInstNum = 16

	var bootstrapIp net.IP
	var bootstrapId = ""
	var bootstrapUdp uint16 = 0
	var bootstrapTcp uint16 = 0
	var bootstrapNodes = []*config.Node{}

	for loop := 0; loop < p2pInstNum; loop++ {

		cfgName := fmt.Sprintf("p2pInst%d", loop)
		log.Debug("testCase3: handling configuration:%s ...", cfgName)

		var dftCfg *config.Config = nil

		if loop == 0 {
			if dftCfg = shell.ShellDefaultBootstrapConfig(); dftCfg == nil {
				log.Debug("testCase3: ShellDefaultBootstrapConfig failed")
				return
			}
		} else {
			if dftCfg = shell.ShellDefaultConfig(); dftCfg == nil {
				log.Debug("testCase3: ShellDefaultConfig failed")
				return
			}
		}

		myCfg := *dftCfg
		myCfg.AppType = config.P2P_TYPE_CHAIN
		myCfg.Name = cfgName
		myCfg.PrivateKey = nil
		myCfg.PublicKey = nil
		myCfg.NetworkType = config.P2pNetworkTypeDynamic
		myCfg.Local.UDP = uint16(portBase4P2p + loop)
		myCfg.Local.TCP = uint16(portBase4P2p + loop)

		if loop == 0 {
			for idx := 0; idx < p2pInstNum; idx++ {
				snid0 := config.SubNetworkID{0xff, byte(idx & 0x0f)}
				myCfg.SubNetIdList = append(myCfg.SubNetIdList, snid0)
				myCfg.SubNetMaxPeers[snid0] = config.MaxPeers
				myCfg.SubNetMaxInBounds[snid0] = config.MaxPeers
				myCfg.SubNetMaxOutbounds[snid0] = 0
			}
		} else {
			snid0 := config.SubNetworkID{0xff, byte(loop & 0x0f)}
			snid1 := config.SubNetworkID{0xff, byte((loop + 1) & 0x0f)}
			myCfg.SubNetIdList = append(myCfg.SubNetIdList, snid0)
			myCfg.SubNetIdList = append(myCfg.SubNetIdList, snid1)
			myCfg.SubNetMaxPeers[snid0] = config.MaxPeers
			myCfg.SubNetMaxInBounds[snid0] = config.MaxInbounds
			myCfg.SubNetMaxOutbounds[snid0] = config.MaxOutbounds
			myCfg.SubNetMaxPeers[snid1] = config.MaxPeers
			myCfg.SubNetMaxInBounds[snid1] = config.MaxInbounds
			myCfg.SubNetMaxOutbounds[snid1] = config.MaxOutbounds
		}

		if loop == 0 {
			myCfg.NoDial = true
			myCfg.NoAccept = true
			myCfg.BootstrapNode = true
		} else {
			myCfg.NoDial = false
			myCfg.NoAccept = false
			myCfg.BootstrapNode = false
		}

		myCfg.BootstrapNodes = nil
		if loop != 0 {
			myCfg.BootstrapNodes = append(myCfg.BootstrapNodes, bootstrapNodes...)
		}

		cfgName, _ = shell.ShellSetConfig(cfgName, &myCfg)
		p2pName2Cfg[cfgName] = shell.ShellGetConfig(cfgName)

		if loop == 0 {

			bootstrapIp = p2pName2Cfg[cfgName].Local.IP
			bootstrapId = fmt.Sprintf("%X", p2pName2Cfg[cfgName].Local.ID)
			bootstrapUdp = p2pName2Cfg[cfgName].Local.UDP
			bootstrapTcp = p2pName2Cfg[cfgName].Local.TCP

			ipv4 := bootstrapIp.To4()
			url := []string {
				fmt.Sprintf("%s@%d.%d.%d.%d:%d:%d",
					bootstrapId,
					ipv4[0],ipv4[1],ipv4[2],ipv4[3],
					bootstrapUdp,
					bootstrapTcp),
			}
			bootstrapNodes = append(bootstrapNodes,config.P2pSetupBootstrapNodes(url)...)
		}

		p2pInst, eno := shell.P2pCreateInstance(p2pName2Cfg[cfgName])
		if eno != sch.SchEnoNone {
			log.Debug("testCase3: SchSchedulerInit failed, eno: %d", eno)
			return
		}
		p2pInst2Cfg[p2pInst] = p2pName2Cfg[cfgName]

		if eno = shell.P2pStart(p2pInst); eno != sch.SchEnoNone {
			log.Debug("testCase3: P2pStart failed, eno: %d", eno)
			return
		}

		if eno := shell.P2pRegisterCallback(shell.P2pIndCb, p2pIndHandler, p2pInst);
			eno != shell.P2pEnoNone {
			log.Debug("testCase3: P2pRegisterCallback failed, eno: %d", eno)
			return
		}

		log.Debug("testCase3: ycp2p started, cofig: %s", cfgName)
	}

	waitInterrupt()
}

//
// testCase4
//
func testCase4(tc *testCase) {

	log.Debug("testCase4: going to start ycp2p ...")

	var p2pInstNum = 8

	var bootstrapIp net.IP
	var bootstrapId = ""
	var bootstrapUdp uint16 = 0
	var bootstrapTcp uint16 = 0
	var bootstrapNodes = []*config.Node{}
	var p2pInstBootstrap *sch.Scheduler = nil

	var staticNodeIdList = []*config.Node{}

	for loop := 0; loop < p2pInstNum; loop++ {

		cfgName := fmt.Sprintf("p2pInst%d", loop)
		log.Debug("testCase4: prepare node identity: %s ...", cfgName)

		dftCfg := shell.ShellDefaultConfig()
		if dftCfg == nil {
			log.Debug("testCase4: ShellDefaultConfig failed")
			return
		}

		myCfg := *dftCfg
		myCfg.AppType = config.P2P_TYPE_CHAIN
		myCfg.Name = cfgName
		myCfg.PrivateKey = nil
		myCfg.PublicKey = nil

		if config.P2pSetupLocalNodeId(&myCfg) != config.PcfgEnoNone {
			log.Debug("testCase4: P2pSetupLocalNodeId failed")
			return
		}

		log.Debug("testCase4: cfgName: %s, id: %X",
			cfgName, myCfg.Local.ID)

		n := config.Node{
			UDP:	uint16(portBase4P2p + loop),
			TCP:	uint16(portBase4P2p + loop),
			ID:		myCfg.Local.ID,
		}

		staticNodeIdList = append(staticNodeIdList, &n)
	}

	for loop := 0; loop < p2pInstNum; loop++ {

		cfgName := fmt.Sprintf("p2pInst%d", loop)
		log.Debug("testCase4: handling configuration:%s ...", cfgName)

		var dftCfg *config.Config = nil

		if loop == 0 {
			if dftCfg = shell.ShellDefaultBootstrapConfig(); dftCfg == nil {
				log.Debug("testCase4: ShellDefaultBootstrapConfig failed")
				return
			}
		} else {
			if dftCfg = shell.ShellDefaultConfig(); dftCfg == nil {
				log.Debug("testCase4: ShellDefaultConfig failed")
				return
			}
		}

		myCfg := *dftCfg
		myCfg.AppType = config.P2P_TYPE_CHAIN
		myCfg.Name = cfgName
		myCfg.PrivateKey = nil
		myCfg.PublicKey = nil
		myCfg.NetworkType = config.P2pNetworkTypeDynamic
		myCfg.StaticNetId = config.ZeroSubNet
		myCfg.Local.UDP = uint16(portBase4P2p + loop)
		myCfg.Local.TCP = uint16(portBase4P2p + loop)
		myCfg.Local.ID = (*staticNodeIdList[loop]).ID

		for idx, n := range staticNodeIdList {
			if idx != loop {
				myCfg.StaticNodes = append(myCfg.StaticNodes, n)
			}
		}

		myCfg.StaticMaxPeers = len(myCfg.StaticNodes) * 2	// config.MaxPeers
		myCfg.StaticMaxOutbounds = len(myCfg.StaticNodes)	// config.MaxOutbounds
		myCfg.StaticMaxInbounds = len(myCfg.StaticNodes) 	// config.MaxInbounds

		if loop == 0 {
			for idx := 0; idx < p2pInstNum; idx++ {
				snid0 := config.SubNetworkID{0xff, byte(idx & 0x0f)}
				myCfg.SubNetIdList = append(myCfg.SubNetIdList, snid0)
				myCfg.SubNetMaxPeers[snid0] = config.MaxPeers
				myCfg.SubNetMaxInBounds[snid0] = config.MaxPeers
				myCfg.SubNetMaxOutbounds[snid0] = 0
			}
		} else {
			snid0 := config.SubNetworkID{0xff, byte(loop & 0x0f)}
			snid1 := config.SubNetworkID{0xff, byte((loop + 1) & 0x0f)}
			myCfg.SubNetIdList = append(myCfg.SubNetIdList, snid0)
			myCfg.SubNetIdList = append(myCfg.SubNetIdList, snid1)
			myCfg.SubNetMaxPeers[snid0] = config.MaxPeers
			myCfg.SubNetMaxInBounds[snid0] = config.MaxInbounds
			myCfg.SubNetMaxOutbounds[snid0] = config.MaxOutbounds
			myCfg.SubNetMaxPeers[snid1] = config.MaxPeers
			myCfg.SubNetMaxInBounds[snid1] = config.MaxInbounds
			myCfg.SubNetMaxOutbounds[snid1] = config.MaxOutbounds
		}

		if loop == 0 {
			myCfg.NoDial = true
			myCfg.NoAccept = true
			myCfg.BootstrapNode = true
		} else {
			myCfg.NoDial = false
			myCfg.NoAccept = false
			myCfg.BootstrapNode = false
		}

		myCfg.BootstrapNodes = nil
		if loop != 0 {
			myCfg.BootstrapNodes = append(myCfg.BootstrapNodes, bootstrapNodes...)
		}

		cfgName, _ = shell.ShellSetConfig(cfgName, &myCfg)
		p2pName2Cfg[cfgName] = shell.ShellGetConfig(cfgName)

		if loop == 0 {

			bootstrapIp = append(bootstrapIp, p2pName2Cfg[cfgName].Local.IP[:]...)
			bootstrapId = fmt.Sprintf("%X", p2pName2Cfg[cfgName].Local.ID)
			bootstrapUdp = p2pName2Cfg[cfgName].Local.UDP
			bootstrapTcp = p2pName2Cfg[cfgName].Local.TCP

			ipv4 := bootstrapIp.To4()
			url := []string{
				fmt.Sprintf("%s@%d.%d.%d.%d:%d:%d",
					bootstrapId,
					ipv4[0], ipv4[1], ipv4[2], ipv4[3],
					bootstrapUdp,
					bootstrapTcp),
			}
			bootstrapNodes = append(bootstrapNodes, config.P2pSetupBootstrapNodes(url)...)
		}

		p2pInst, eno := shell.P2pCreateInstance(p2pName2Cfg[cfgName])
		if eno != sch.SchEnoNone {
			log.Debug("testCase4: SchSchedulerInit failed, eno: %d", eno)
			return
		}
		p2pInst2Cfg[p2pInst] = p2pName2Cfg[cfgName]

		if loop == 0 {
			p2pInstBootstrap = p2pInst
		}
	}

	var p2pInstList = []*sch.Scheduler{}
	for p2pInst := range p2pInst2Cfg {
		if p2pInst != p2pInstBootstrap {
			p2pInstList = append(p2pInstList, p2pInst)
		}
	}

	if eno := shell.P2pStart(p2pInstBootstrap); eno != sch.SchEnoNone {
		log.Debug("testCase4: P2pStart failed, eno: %d", eno)
		return
	}

	for piNum := len(p2pInstList); piNum > 0; piNum-- {

		time.Sleep(time.Second * 2)

		pidx := rand.Intn(piNum)
		p2pInst := p2pInstList[pidx]
		p2pInstList = append(p2pInstList[0:pidx], p2pInstList[pidx+1:]...)

		if eno := shell.P2pStart(p2pInst); eno != sch.SchEnoNone {
			log.Debug("testCase4: P2pStart failed, eno: %d", eno)
			return
		}

		if eno := shell.P2pRegisterCallback(shell.P2pIndCb, p2pIndHandler, p2pInst);
			eno != shell.P2pEnoNone {
			log.Debug("testCase4: P2pRegisterCallback failed, eno: %d", eno)
			return
		}

		cfgName := p2pInst.SchGetP2pCfgName()
		log.Debug("testCase4: ycp2p started, cofig: %s", cfgName)
	}

	waitInterrupt()
}

//
// testCase5
//
func testCase5(tc *testCase) {

	log.Debug("testCase5: going to start ycp2p ...")

	var p2pInstNum = 16

	var bootstrapIp net.IP
	var bootstrapId = ""
	var bootstrapUdp uint16 = 0
	var bootstrapTcp uint16 = 0
	var bootstrapNodes = []*config.Node{}
	var p2pInstBootstrap *sch.Scheduler = nil

	var staticNodeIdList = []*config.Node{}

	for loop := 0; loop < p2pInstNum; loop++ {

		cfgName := fmt.Sprintf("p2pInst%d", loop)
		log.Debug("testCase5: prepare node identity: %s ...", cfgName)

		dftCfg := shell.ShellDefaultConfig()
		if dftCfg == nil {
			log.Debug("testCase5: ShellDefaultConfig failed")
			return
		}

		myCfg := *dftCfg
		myCfg.AppType = config.P2P_TYPE_CHAIN
		myCfg.Name = cfgName
		myCfg.PrivateKey = nil
		myCfg.PublicKey = nil

		if config.P2pSetupLocalNodeId(&myCfg) != config.PcfgEnoNone {
			log.Debug("testCase5: P2pSetupLocalNodeId failed")
			return
		}

		log.Debug("testCase5: cfgName: %s, id: %X",
			cfgName, myCfg.Local.ID)

		n := config.Node{
			IP:		dftCfg.Local.IP,
			UDP:	uint16(portBase4P2p + loop),
			TCP:	uint16(portBase4P2p + loop),
			ID:		myCfg.Local.ID,
		}

		staticNodeIdList = append(staticNodeIdList, &n)
	}

	for loop := 0; loop < p2pInstNum; loop++ {

		cfgName := fmt.Sprintf("p2pInst%d", loop)
		log.Debug("testCase5: handling configuration:%s ...", cfgName)

		var dftCfg *config.Config = nil

		if loop == 0 {
			if dftCfg = shell.ShellDefaultBootstrapConfig(); dftCfg == nil {
				log.Debug("testCase5: ShellDefaultBootstrapConfig failed")
				return
			}
		} else {
			if dftCfg = shell.ShellDefaultConfig(); dftCfg == nil {
				log.Debug("testCase5: ShellDefaultConfig failed")
				return
			}
		}

		myCfg := *dftCfg
		myCfg.AppType = config.P2P_TYPE_CHAIN
		myCfg.Name = cfgName
		myCfg.PrivateKey = nil
		myCfg.PublicKey = nil
		myCfg.NetworkType = config.P2pNetworkTypeDynamic
		myCfg.StaticNetId = config.ZeroSubNet
		myCfg.Local.UDP = uint16(portBase4P2p + loop)
		myCfg.Local.TCP = uint16(portBase4P2p + loop)
		myCfg.Local.ID = (*staticNodeIdList[loop]).ID

		for idx, n := range staticNodeIdList {
			if idx != loop {
				myCfg.StaticNodes = append(myCfg.StaticNodes, n)
			}
		}

		myCfg.StaticMaxPeers = len(myCfg.StaticNodes) * 2	// config.MaxPeers
		myCfg.StaticMaxOutbounds = len(myCfg.StaticNodes)	// config.MaxOutbounds
		myCfg.StaticMaxInbounds = len(myCfg.StaticNodes) 	// config.MaxInbounds

		if loop == 0 {
			for idx := 0; idx < p2pInstNum; idx++ {
				snid0 := config.SubNetworkID{0xff, byte(idx & 0x3f)}
				myCfg.SubNetIdList = append(myCfg.SubNetIdList, snid0)
				myCfg.SubNetMaxPeers[snid0] = config.MaxPeers
				myCfg.SubNetMaxInBounds[snid0] = config.MaxPeers
				myCfg.SubNetMaxOutbounds[snid0] = 0
			}
		} else {
			snid0 := config.SubNetworkID{0xff, byte(loop & 0x3f)}
			snid1 := config.SubNetworkID{0xff, byte((loop + 1) & 0x3f)}
			myCfg.SubNetIdList = append(myCfg.SubNetIdList, snid0)
			myCfg.SubNetIdList = append(myCfg.SubNetIdList, snid1)
			myCfg.SubNetMaxPeers[snid0] = config.MaxPeers
			myCfg.SubNetMaxInBounds[snid0] = config.MaxInbounds
			myCfg.SubNetMaxOutbounds[snid0] = config.MaxOutbounds
			myCfg.SubNetMaxPeers[snid1] = config.MaxPeers
			myCfg.SubNetMaxInBounds[snid1] = config.MaxInbounds
			myCfg.SubNetMaxOutbounds[snid1] = config.MaxOutbounds
		}

		if loop == 0 {
			myCfg.NoDial = true
			myCfg.NoAccept = true
			myCfg.BootstrapNode = true
		} else {
			myCfg.NoDial = false
			myCfg.NoAccept = false
			myCfg.BootstrapNode = false
		}

		myCfg.BootstrapNodes = nil
		if loop != 0 {
			myCfg.BootstrapNodes = append(myCfg.BootstrapNodes, bootstrapNodes...)
		}

		cfgName, _ = shell.ShellSetConfig(cfgName, &myCfg)
		p2pName2Cfg[cfgName] = shell.ShellGetConfig(cfgName)

		if loop == 0 {

			bootstrapIp = append(bootstrapIp, p2pName2Cfg[cfgName].Local.IP[:]...)
			bootstrapId = fmt.Sprintf("%X", p2pName2Cfg[cfgName].Local.ID)
			bootstrapUdp = p2pName2Cfg[cfgName].Local.UDP
			bootstrapTcp = p2pName2Cfg[cfgName].Local.TCP

			ipv4 := bootstrapIp.To4()
			url := []string{
				fmt.Sprintf("%s@%d.%d.%d.%d:%d:%d",
					bootstrapId,
					ipv4[0], ipv4[1], ipv4[2], ipv4[3],
					bootstrapUdp,
					bootstrapTcp),
			}
			bootstrapNodes = append(bootstrapNodes, config.P2pSetupBootstrapNodes(url)...)
		}

		p2pInst, eno := shell.P2pCreateInstance(p2pName2Cfg[cfgName])
		if eno != sch.SchEnoNone {
			log.Debug("testCase5: SchSchedulerInit failed, eno: %d", eno)
			return
		}
		p2pInst2Cfg[p2pInst] = p2pName2Cfg[cfgName]

		if loop == 0 {
			p2pInstBootstrap = p2pInst
		}
	}

	var p2pInstList = []*sch.Scheduler{}
	for p2pInst := range p2pInst2Cfg {
		if p2pInst != p2pInstBootstrap {
			p2pInstList = append(p2pInstList, p2pInst)
		}
	}

	if eno := shell.P2pStart(p2pInstBootstrap); eno != sch.SchEnoNone {
		log.Debug("testCase5: P2pStart failed, eno: %d", eno)
		return
	}

	for piNum := len(p2pInstList); piNum > 0; piNum-- {

		time.Sleep(time.Second * 2)

		pidx := rand.Intn(piNum)
		p2pInst := p2pInstList[pidx]
		p2pInstList = append(p2pInstList[0:pidx], p2pInstList[pidx+1:]...)

		if eno := shell.P2pStart(p2pInst); eno != sch.SchEnoNone {
			log.Debug("testCase5: P2pStart failed, eno: %d", eno)
			return
		}

		if eno := shell.P2pRegisterCallback(shell.P2pIndCb, p2pIndHandler, p2pInst);
			eno != shell.P2pEnoNone {
			log.Debug("testCase5: P2pRegisterCallback failed, eno: %d", eno)
			return
		}

		cfgName := p2pInst.SchGetP2pCfgName()
		log.Debug("testCase5: ycp2p started, cofig: %s", cfgName)
	}

	//
	// Sleep and then stop p2p instance
	//

	time.Sleep(time.Second * 64)
	golog.Printf("testCase5: going to stop p2p instances ...")

	stopCount := len(p2pInst2Cfg)
	stopChain := make(chan bool, stopCount)
	for p2pInst := range p2pInst2Cfg {
		go shell.P2pStop(p2pInst, stopChain)
	}

_waitStop:
	for {
		<-stopChain
		if stopCount--; stopCount == 0 {
			break _waitStop
		}
	}

	txStopAll()
	for !isAllDone() {
		log.Debug("testCase5: wait all tx/rx instances to be done...")
		time.Sleep(time.Second * 1)
	}
	golog.Printf("testCase5: it's the end")

	waitInterrupt()
}

//
// testCase6
//
func testCase6(tc *testCase) {
	
	log.Debug("testCase6: going to start ycDht ...")

	var dhtInstNum = 8
	var dhtInstList = []*sch.Scheduler{}

	for loop := 0; loop < dhtInstNum; loop++ {

		cfgName := fmt.Sprintf("dhtInst%d", loop)
		log.Debug("testCase6: handling configuration:%s ...", cfgName)

		var dftCfg *config.Config = nil

		if dftCfg = shell.ShellDefaultConfig(); dftCfg == nil {
			log.Debug("testCase6: ShellDefaultConfig failed")
			return
		}

		myCfg := *dftCfg
		myCfg.AppType = config.P2P_TYPE_DHT
		myCfg.Name = cfgName
		myCfg.PrivateKey = nil
		myCfg.PublicKey = nil
		myCfg.Local.UDP = uint16(portBase4Dht + loop)
		myCfg.Local.TCP = uint16(portBase4Dht + loop)

		cfgName, _ = shell.ShellSetConfig(cfgName, &myCfg)
		dhtName2Cfg[cfgName] = shell.ShellGetConfig(cfgName)

		dhtInst, eno := shell.P2pCreateInstance(dhtName2Cfg[cfgName])
		if eno != sch.SchEnoNone {
			log.Debug("testCase6: SchSchedulerInit failed, eno: %d", eno)
			return
		}

		dhtInst2Cfg[dhtInst] = dhtName2Cfg[cfgName]
		dhtInstList = append(dhtInstList, dhtInst)
	}

	for _, dhtInst := range dhtInstList {
		time.Sleep(time.Second * 1)
		if eno := shell.P2pStart(dhtInst); eno != sch.SchEnoNone {
			log.Debug("testCase6: P2pStart failed, eno: %d", eno)
			return
		}
		dhtMgr := dhtInst.SchGetUserTaskIF(sch.DhtMgrName).(*dht.DhtMgr)
		if eno := dhtMgr.InstallEventCallback(dhtTestEventCallback); eno != dht.DhtEnoNone {
			log.Debug("testCase6: InstallEventCallback failed, eno: %d", eno)
			return
		}
	}

	cm := dhtTestBuildConnMatrix(dhtInstList)
	if cm == nil {
		log.Debug("testCase6: dhtBuildConnMatrix failed")
		return
	}

	log.Debug("testCase6: sleep a while for instances starup ...")
	time.Sleep(time.Second * 4)
	log.Debug("testCase6: going to apply connection matrix ...")

	if eno := dhtTestConnMatrixApply(dhtInstList, cm); eno != dht.DhtEnoNone {
		log.Debug("testCase6: dhtConnMatrixApply failed, eno: %d", eno)
		return
	}

	time.Sleep(time.Second*8)

	if eno := dhtTestBootstrapStart(dhtInstList); eno != dht.DhtEnoNone {
		log.Debug("testCase6: dhtTestBootstrapStart failed, eno: %d", eno)
		return
	}

	waitInterrupt()
}

//
// testCase7
//
func testCase7(tc *testCase) {

	log.Debug("testCase7: going to start ycDht ...")

	var dhtInstNum = 32
	var dhtInstList = []*sch.Scheduler{}

	for loop := 0; loop < dhtInstNum; loop++ {

		cfgName := fmt.Sprintf("dhtInst%d", loop)
		log.Debug("testCase7: handling configuration:%s ...", cfgName)

		var dftCfg *config.Config = nil

		if dftCfg = shell.ShellDefaultConfig(); dftCfg == nil {
			log.Debug("testCase7: ShellDefaultConfig failed")
			return
		}

		myCfg := *dftCfg
		myCfg.AppType = config.P2P_TYPE_DHT
		myCfg.Name = cfgName
		myCfg.PrivateKey = nil
		myCfg.PublicKey = nil
		myCfg.Local.UDP = uint16(portBase4Dht + loop)
		myCfg.Local.TCP = uint16(portBase4Dht + loop)

		cfgName, _ = shell.ShellSetConfig(cfgName, &myCfg)
		dhtName2Cfg[cfgName] = shell.ShellGetConfig(cfgName)

		dhtInst, eno := shell.P2pCreateInstance(dhtName2Cfg[cfgName])
		if eno != sch.SchEnoNone {
			log.Debug("testCase7: SchSchedulerInit failed, eno: %d", eno)
			return
		}

		dhtInst2Cfg[dhtInst] = dhtName2Cfg[cfgName]
		dhtInstList = append(dhtInstList, dhtInst)
	}

	for _, dhtInst := range dhtInstList {
		time.Sleep(time.Second * 1)
		if eno := shell.P2pStart(dhtInst); eno != sch.SchEnoNone {
			log.Debug("testCase7: P2pStart failed, eno: %d", eno)
			return
		}
		dhtMgr := dhtInst.SchGetUserTaskIF(sch.DhtMgrName).(*dht.DhtMgr)
		if eno := dhtMgr.InstallEventCallback(dhtTestEventCallback); eno != dht.DhtEnoNone {
			log.Debug("testCase7: InstallEventCallback failed, eno: %d", eno)
			return
		}
	}

	cm := dhtTestBuildConnMatrix(dhtInstList)
	if cm == nil {
		log.Debug("testCase7: dhtBuildConnMatrix failed")
		return
	}

	log.Debug("testCase7: sleep a while for instances starup ...")
	time.Sleep(time.Second * 4)
	log.Debug("testCase7: going to apply connection matrix ...")

	if eno := dhtTestConnMatrixApply(dhtInstList, cm); eno != dht.DhtEnoNone {
		log.Debug("testCase7: dhtConnMatrixApply failed, eno: %d", eno)
		return
	}

	time.Sleep(time.Second * 8)
	golog.Printf("testCase7: going to stop dht instances ...")

	stopCount := len(dhtInst2Cfg)
	stopChain := make(chan bool, stopCount)

	for dhtInst := range dhtInst2Cfg {
		go shell.P2pStop(dhtInst, stopChain)
	}

_waitStop:
	for {
		<-stopChain
		if stopCount--; stopCount == 0 {
			break _waitStop
		}
	}
	golog.Printf("testCase7: it's the end")
	waitInterrupt()
}

//
// build connection matrix for instance list
//
const (
	minInstNum		= 7
	instNeightbors	= 3

	lineMatrixType	= 0
	ringMatrixType	= 1
	starMatrixType	= 2
	randMatrixType	= 3
)

func dhtTestBuildConnMatrix(p2pInstList []*sch.Scheduler) [][]bool {
	// initialize specific connection relationship between dht instances, for example:
	// ring, star, line, random, ...
	var instNum int
	if instNum = len(p2pInstList); instNum <= minInstNum {
		log.Debug("dhtTestBuildConnMatrix: " +
			"instance not enougth, instNum: %d, should more than: %d",
			instNum, minInstNum)
		return nil
	}

	var m = make([][]bool, instNum)
	for idx := range m {
		row := make([]bool, instNum)
		m[idx] = row
		for idx := range row {
			row[idx] = false
		}
	}

	mt := lineMatrixType

	if mt == lineMatrixType {
		lineMatrix(m)
	} else if mt == ringMatrixType {
		ringMatrix(m)
	} else if mt == starMatrixType {
		starMatrix(m)
	} else if mt == randMatrixType{
		randMatrix(m)
	} else {
		lineMatrix(m)
	}
	return m
}

//
// setup line type connection matrix
//
func lineMatrix(m [][]bool) error {
	instNum := cap(m[0])
	if instNum <= minInstNum {
		return errors.New(fmt.Sprintf("min instances number: %d", minInstNum + 1))
	}
	for idx := 0; idx < instNum - 1; idx++ {
		m[idx][idx+1] = true
	}
	return nil
}

//
// setup ring type connection matrix
//
func ringMatrix(m [][]bool) error {
	instNum := cap(m[0])
	if instNum <= minInstNum {
		return errors.New(fmt.Sprintf("min instances number: %d", minInstNum + 1))
	}
	if err := lineMatrix(m); err != nil {
		return nil
	}
	m[instNum-1][0] = true
	return nil
}

//
// setup star type connection matrix
//
func starMatrix(m [][]bool) error {
	instNum := cap(m[0])
	if instNum <= minInstNum {
		return errors.New(fmt.Sprintf("min instances number: %d", minInstNum + 1))
	}
	for idx := 1; idx < instNum; idx++ {
		m[0][idx] = true
	}
	return nil
}

//
// setup a random connection matrix
//
func randMatrix(m [][]bool) error {
	rand.Seed(time.Now().Unix())
	instNum := cap(m[0])
	if instNum <= minInstNum {
		return errors.New(fmt.Sprintf("min instances number: %d", minInstNum + 1))
	}
	var neighbors = make([]int, instNum)
	for idx := 0; idx < instNum; idx++ {
		neighbors[idx] = 0
	}
	log.Debug("dhtTestBuildConnMatrix: total instance number: %d", instNum)
	for idx, row := range m {
		log.Debug("dhtTestBuildConnMatrix: instance index: %d", idx)
		count := neighbors[idx]
		if count >= instNeightbors {
			continue
		}
		for ; count < instNeightbors; {
			mask := bytes.Repeat([]byte{1}, instNum)
			randStop := false
			for !randStop {
				n := rand.Intn(instNum)
				mask[n] = 0
				allCovered := true
				for _, mk := range mask {
					allCovered = allCovered && (mk == 0)
				}
				randStop = allCovered
				if n == idx {
					continue
				}

				if row[n] == true {
					continue
				}

				if neighbors[n] >= instNeightbors {
					continue
				}

				row[n] = true
				m[n][idx] = true
				neighbors[n]++
				count++
				break
			}
			if randStop {
				break
			}
		}
		neighbors[idx] = count
	}
	return nil
}

//
// apply connection matrix for instance list
//
func dhtTestConnMatrixApply(p2pInstList []*sch.Scheduler, cm [][]bool) int {
	// setup connection between dht instance according to the connection matrix
	if len(p2pInstList) == 0 || cm == nil {
		log.Debug("dhtTestConnMatrixApply: invalid parameters")
		return -1
	}
	instNum := len(p2pInstList)
	cmBackup := make([][]bool, instNum)
	for idx := 0; idx < instNum; idx++ {
		cmBackup[idx] = append(cmBackup[idx], cm[idx]...)
	}
	conCount := 0
	for idx, row := range cmBackup {
		for n, connFlag := range row {
			if connFlag {
				dhtMgr := p2pInstList[idx].SchGetUserTaskIF(dht.DhtMgrName).(*dht.DhtMgr)
				local := dhtInst2Cfg[p2pInstList[idx]].Local
				peerCfg := dhtInst2Cfg[p2pInstList[n]]
				req := sch.MsgDhtBlindConnectReq {
					Peer: &peerCfg.Local,
				}
				if eno := dhtMgr.DhtCommand(sch.EvDhtBlindConnectReq, &req); eno != sch.SchEnoNone {
					log.Debug("dhtTestConnMatrixApply: DhtCommand failed, eno: %d", eno)
					return -1
				}
				cmBackup[idx][n] = false
				cmBackup[n][idx] = false
				conCount++
				log.Debug("dhtTestConnMatrixApply: " +
					"blind connect request sent ok, from: %+v, to: %+v",
					local, peerCfg.Local)
			}
		}
	}
	log.Debug("dhtTestConnMatrixApply: applied, blind connection count: %d", conCount)
	return 0
}

//
// dht start route bootstrap(refreshing)
//
func dhtTestBootstrapStart(dhtInstList []*sch.Scheduler) int {
	for _, dhtInst := range dhtInstList {
		dhtMgr := dhtInst.SchGetUserTaskIF(dht.DhtMgrName).(*dht.DhtMgr)
		dhtMgr.DhtCommand(sch.EvDhtRutRefreshReq, nil)
	}
	return dht.DhtEnoNone
}

//
// find node
//
func dhtTestFindNode(dhtInstList []*sch.Scheduler) int {
	rand.Seed(time.Now().Unix())
	req := sch.MsgDhtQryMgrQueryStartReq {
		Target:		config.NodeID{},
		Msg:		nil,
		ForWhat:	dht.MID_FINDNODE,
		Seq:		-1,
	}
	instNum := len(dhtInstList)
	for idx, dhtInst := range dhtInstList {
		targetIdx := rand.Intn(instNum)
		for ; targetIdx == idx; {
			targetIdx = rand.Intn(instNum)
		}
		req.Target = dhtInstList[targetIdx].SchGetP2pConfig().Local.ID
		req.Seq = time.Now().Unix()
		dhtMgr := dhtInst.SchGetUserTaskIF(dht.DhtMgrName).(*dht.DhtMgr)
		dhtMgr.DhtCommand(sch.EvDhtMgrFindPeerReq, &req)
	}
	return dht.DhtEnoNone
}

//
// put value
//
type DhtTestKV struct {
	Key		[]byte
	Val		[]byte
}
func dhtTestPutValue(dhtInstList []*sch.Scheduler) (int, [] *DhtTestKV) {
	rand.Seed(time.Now().Unix())
	kvList := []*DhtTestKV{}
	req := sch.MsgDhtMgrPutValueReq{
		Key:	nil,
		Val:	nil,
	}
	for _, dhtInst := range dhtInstList {
		val := make([]byte, 128, 128)
		rand.Read(val)
		req.Val = []byte(fmt.Sprintf("%s:%x", dhtInst.SchGetP2pCfgName(), val))
		key := sha256.Sum256(req.Val)
		req.Key = key[0:]
		dhtMgr := dhtInst.SchGetUserTaskIF(dht.DhtMgrName).(*dht.DhtMgr)
		dhtMgr.DhtCommand(sch.EvDhtMgrPutValueReq, &req)
		kv := DhtTestKV {
			Key:	req.Key,
			Val:	req.Val,
		}
		kvList = append(kvList, &kv)
	}
	return dht.DhtEnoNone, kvList
}

//
// get value
//
func dhtTestGetValue(dhtInstList []*sch.Scheduler, keys [][]byte) int {
	if len(dhtInstList) != len(keys) {
		return dht.DhtEnoParameter
	}
	req := sch.MsgDhtMgrGetValueReq{
		Key:	nil,
	}
	for idx, dhtInst := range dhtInstList {
		req.Key = keys[idx]
		dhtMgr := dhtInst.SchGetUserTaskIF(dht.DhtMgrName).(*dht.DhtMgr)
		dhtMgr.DhtCommand(sch.EvDhtMgrGetValueReq, &req)
	}
	return dht.DhtEnoNone
}

//
// put provider
//
type DhtTestPrd struct {
	Key		[]byte
	Prd		config.Node
}
func dhtTestPutProvider(dhtInstList []*sch.Scheduler) (int, []*DhtTestPrd) {
	rand.Seed(time.Now().Unix())
	prdList := []*DhtTestPrd{}
	req := sch.MsgDhtPrdMgrAddProviderReq {
		Key:	nil,
		Prd:	config.Node{},
	}
	for _, dhtInst := range dhtInstList {
		val := make([]byte, 128, 128)
		rand.Read(val)
		key := sha256.Sum256([]byte(fmt.Sprintf("%s:%x", dhtInst.SchGetP2pCfgName(), val)))
		req.Key = key[0:]
		req.Prd = dhtInst.SchGetP2pConfig().Local
		dhtMgr := dhtInst.SchGetUserTaskIF(dht.DhtMgrName).(*dht.DhtMgr)
		dhtMgr.DhtCommand(sch.EvDhtMgrPutProviderReq, &req)
		prd := DhtTestPrd {
			Key:	req.Key,
			Prd:	req.Prd,
		}
		prdList = append(prdList, &prd)
	}
	return dht.DhtEnoNone, prdList
}

//
// get provider
//
func dhtTestGetProvider(dhtInstList []*sch.Scheduler, keys [][]byte) int {
	if len(dhtInstList) != len(keys) {
		return dht.DhtEnoParameter
	}
	req := sch.MsgDhtMgrGetProviderReq {
		Key:	nil,
	}
	for idx, dhtInst := range dhtInstList {
		req.Key = keys[idx]
		dhtMgr := dhtInst.SchGetUserTaskIF(dht.DhtMgrName).(*dht.DhtMgr)
		dhtMgr.DhtCommand(sch.EvDhtMgrGetProviderReq, &req)
	}
	return dht.DhtEnoNone
}

//
// dht event callback
//
func dhtTestEventCallback(mgr interface{}, mid int, msg interface{}) int {
	eno := -1
	switch mid {
	case  sch.EvDhtBlindConnectRsp:
		eno = dhtTestBlindConnectRsp(mgr.(*dht.DhtMgr), msg.(*sch.MsgDhtBlindConnectRsp))
	case  sch.EvDhtMgrFindPeerRsp:
		eno = dhtTestMgrFindPeerRsp(mgr.(*dht.DhtMgr), msg.(*sch.MsgDhtQryMgrQueryResultInd))
	case  sch.EvDhtQryMgrQueryStartRsp:
		eno = dhtTestQryMgrQueryStartRsp(mgr.(*dht.DhtMgr), msg.(*sch.MsgDhtQryMgrQueryStartRsp))
	case  sch.EvDhtQryMgrQueryStopRsp:
		eno = dhtTestQryMgrQueryStopRsp(mgr.(*dht.DhtMgr), msg.(*sch.MsgDhtQryMgrQueryStopRsp))
	case  sch.EvDhtConMgrSendCfm:
		eno = dhtTestConMgrSendCfm(mgr.(*dht.DhtMgr), msg.(*sch.MsgDhtConMgrSendCfm))
	case  sch.EvDhtMgrPutProviderRsp:
		eno = dhtTestMgrPutProviderRsp(mgr.(*dht.DhtMgr), msg.(*sch.MsgDhtPrdMgrAddProviderRsp))
	case  sch.EvDhtMgrGetProviderRsp:
		eno = dhtTestMgrGetProviderRsp(mgr.(*dht.DhtMgr), msg.(*sch.MsgDhtMgrGetProviderRsp))
	case  sch.EvDhtMgrPutValueRsp:
		eno = dhtTestMgrPutValueRsp(mgr.(*dht.DhtMgr), msg.(*sch.MsgDhtMgrPutValueRsp))
	case  sch.EvDhtMgrGetValueRsp:
		eno = dhtTestMgrGetValueRsp(mgr.(*dht.DhtMgr), msg.(*sch.MsgDhtMgrGetValueRsp))
	case  sch.EvDhtConMgrCloseRsp:
		eno = dhtTestConMgrCloseRsp(mgr.(*dht.DhtMgr), msg.(*sch.MsgDhtConMgrCloseRsp))
	case  sch.EvDhtConInstStatusInd:
		eno = dhtTestConInstStatusInd(mgr.(*dht.DhtMgr), msg.(*sch.MsgDhtConInstStatusInd))
	default:
		log.Debug("dhtTestEventCallback: unknown event: %d", mid)
	}
	return eno
}

func dhtTestBlindConnectRsp(mgr *dht.DhtMgr, msg *sch.MsgDhtBlindConnectRsp) int {
	if mgr == nil {
		log.Debug("dhtTestBlindConnectRsp: nil manager")
		return -1
	}
	sdl := mgr.GetScheduler()
	if sdl == nil {
		log.Debug("dhtTestBlindConnectRsp: nil scheduler")
		return -1
	}
	cfgName := sdl.SchGetP2pCfgName()
	log.Debug("dhtTestBlindConnectRsp: instance: %s, msg: %v", cfgName, *msg)
	return 0
}

func dhtTestMgrFindPeerRsp(mgr *dht.DhtMgr, msg *sch.MsgDhtQryMgrQueryResultInd) int {
	if mgr == nil {
		log.Debug("dhtTestMgrFindPeerRsp: nil manager")
		return -1
	}
	sdl := mgr.GetScheduler()
	if sdl == nil {
		log.Debug("dhtTestMgrFindPeerRsp: nil scheduler")
		return -1
	}
	cfgName := sdl.SchGetP2pCfgName()
	log.Debug("dhtTestMgrFindPeerRsp: instance: %s, msg: %v", cfgName, *msg)
	return 0
}

func dhtTestQryMgrQueryStartRsp(mgr *dht.DhtMgr, msg *sch.MsgDhtQryMgrQueryStartRsp) int {
	if mgr == nil {
		log.Debug("dhtTestQryMgrQueryStartRsp: nil manager")
		return -1
	}
	sdl := mgr.GetScheduler()
	if sdl == nil {
		log.Debug("dhtTestQryMgrQueryStartRsp: nil scheduler")
		return -1
	}
	cfgName := sdl.SchGetP2pCfgName()
	log.Debug("dhtTestQryMgrQueryStartRsp: instance: %s, msg: %v", cfgName, *msg)
	return 0
}

func dhtTestQryMgrQueryStopRsp(mgr *dht.DhtMgr, msg *sch.MsgDhtQryMgrQueryStopRsp) int {
	if mgr == nil {
		log.Debug("dhtTestQryMgrQueryStopRsp: nil manager")
		return -1
	}
	sdl := mgr.GetScheduler()
	if sdl == nil {
		log.Debug("dhtTestQryMgrQueryStopRsp: nil scheduler")
		return -1
	}
	cfgName := sdl.SchGetP2pCfgName()
	log.Debug("dhtTestQryMgrQueryStopRsp: instance: %s, msg: %v", cfgName, *msg)
	return 0
}

func dhtTestConMgrSendCfm(mgr *dht.DhtMgr, msg *sch.MsgDhtConMgrSendCfm) int {
	if mgr == nil {
		log.Debug("dhtTestConMgrSendCfm: nil manager")
		return -1
	}
	sdl := mgr.GetScheduler()
	if sdl == nil {
		log.Debug("dhtTestConMgrSendCfm: nil scheduler")
		return -1
	}
	cfgName := sdl.SchGetP2pCfgName()
	log.Debug("dhtTestConMgrSendCfm: instance: %s, msg: %v", cfgName, *msg)
	return 0
}

func dhtTestMgrPutProviderRsp(mgr *dht.DhtMgr, msg *sch.MsgDhtPrdMgrAddProviderRsp) int {
	if mgr == nil {
		log.Debug("dhtTestMgrPutProviderRsp: nil manager")
		return -1
	}
	sdl := mgr.GetScheduler()
	if sdl == nil {
		log.Debug("dhtTestMgrPutProviderRsp: nil scheduler")
		return -1
	}
	cfgName := sdl.SchGetP2pCfgName()
	log.Debug("dhtTestMgrPutProviderRsp: instance: %s, msg: %v", cfgName, *msg)
	return 0
}

func dhtTestMgrGetProviderRsp(mgr *dht.DhtMgr, msg *sch.MsgDhtMgrGetProviderRsp) int {
	if mgr == nil {
		log.Debug("dhtTestMgrGetProviderRsp: nil manager")
		return -1
	}
	sdl := mgr.GetScheduler()
	if sdl == nil {
		log.Debug("dhtTestMgrGetProviderRsp: nil scheduler")
		return -1
	}
	cfgName := sdl.SchGetP2pCfgName()
	log.Debug("dhtTestMgrGetProviderRsp: instance: %s, msg: %v", cfgName, *msg)
	return 0
}

func dhtTestMgrPutValueRsp(mgr *dht.DhtMgr, msg *sch.MsgDhtMgrPutValueRsp) int {
	if mgr == nil {
		log.Debug("dhtTestMgrPutValueRsp: nil manager")
		return -1
	}
	sdl := mgr.GetScheduler()
	if sdl == nil {
		log.Debug("dhtTestMgrPutValueRsp: nil scheduler")
		return -1
	}
	cfgName := sdl.SchGetP2pCfgName()
	log.Debug("dhtTestMgrPutValueRsp: instance: %s, msg: %v", cfgName, *msg)
	return 0
}

func dhtTestMgrGetValueRsp(mgr *dht.DhtMgr, msg *sch.MsgDhtMgrGetValueRsp) int {
	if mgr == nil {
		log.Debug("dhtTestMgrGetValueRsp: nil manager")
		return -1
	}
	sdl := mgr.GetScheduler()
	if sdl == nil {
		log.Debug("dhtTestMgrGetValueRsp: nil scheduler")
		return -1
	}
	cfgName := sdl.SchGetP2pCfgName()
	log.Debug("dhtTestMgrGetValueRsp: instance: %s, msg: %v", cfgName, *msg)
	return 0
}

func dhtTestConMgrCloseRsp(mgr *dht.DhtMgr, msg *sch.MsgDhtConMgrCloseRsp) int {
	if mgr == nil {
		log.Debug("dhtTestConMgrCloseRsp: nil manager")
		return -1
	}
	sdl := mgr.GetScheduler()
	if sdl == nil {
		log.Debug("dhtTestConMgrCloseRsp: nil scheduler")
		return -1
	}
	cfgName := sdl.SchGetP2pCfgName()
	log.Debug("dhtTestConMgrCloseRsp: instance: %s, msg: %v", cfgName, *msg)
	return 0
}

func dhtTestConInstStatusInd(mgr *dht.DhtMgr, msg *sch.MsgDhtConInstStatusInd) int {
	switch msg.Status {
	case dht.CisNull:
		log.Debug("dhtTestConInstStatusInd: CisNull")
	case dht.CisConnecting:
		log.Debug("dhtTestConInstStatusInd: CisConnecting")
	case dht.CisConnected:
		log.Debug("dhtTestConInstStatusInd: CisConnected")
		if msg.Dir == dht.ConInstDirOutbound {
			if eno := mgr.InstallRxDataCallback(dhtTestConInstRxDataCallback,
				msg.Peer, dht.ConInstDir(msg.Dir)); eno != dht.DhtEnoNone {

				log.Debug("dhtTestConInstStatusInd: "+
					"InstallRxDataCallback failed, eno: %d, peer: %x",
					eno, *msg.Peer)

				return -1
			}
		}
	case dht.CisAccepted:
		log.Debug("dhtTestConInstStatusInd: CisAccepted")
	case dht.CisInHandshaking:
		log.Debug("dhtTestConInstStatusInd: CisInHandshaking")
	case dht.CisHandshaked:
		log.Debug("dhtTestConInstStatusInd: CisHandshaked")
		if msg.Dir == dht.ConInstDirInbound {
			if eno := mgr.InstallRxDataCallback(dhtTestConInstRxDataCallback,
				msg.Peer, dht.ConInstDir(msg.Dir)); eno != dht.DhtEnoNone {
				log.Debug("dhtTestConInstStatusInd: "+
					"InstallRxDataCallback failed, eno: %d, peer: %x",
					eno, *msg.Peer)
				return -1
			}
		}
	case dht.CisInService:
		log.Debug("dhtTestConInstStatusInd: CisInService")
	case dht.CisClosed:
		log.Debug("dhtTestConInstStatusInd: CisClosed")
	default:
		log.Debug("dhtTestConInstStatusInd: unknown status: %d", msg.Status)
	}
	return 0
}

//
// connetion instance rx-data callback
//
func dhtTestConInstRxDataCallback (conInst interface{}, pid uint32, msg interface{}) int {
	if conInst == nil || msg == nil {
		log.Debug("dhtTestConInstRxDataCallback:invalid parameters, " +
			"conInst: %p, pid: %d, msg: %p", conInst, pid, msg)
		return -1
	}
	ci, ok := conInst.(*dht.ConInst)
	if !ok {
		log.Debug("dhtTestConInstRxDataCallback: invalid connection instance, type: %T", conInst)
		return -1
	}
	data, ok := msg.([]byte)
	if !ok {
		log.Debug("dhtTestConInstRxDataCallback: invalid message, type: %T", msg)
		return -1
	}

	if pid != uint32(dht.PID_EXT) {
		log.Debug("dhtTestConInstRxDataCallback: " +
			"invalid pid: %d",
			pid)
		return -1
	}
	sdl := ci.GetScheduler()
	if sdl == nil {
		log.Debug("dhtTestConInstRxDataCallback: nil scheduler")
		return -1
	}
	cfgName := sdl.SchGetP2pCfgName()
	log.Debug("dhtTestConInstRxDataCallback: instance: %s, pid: %d, length: %d, data: %x",
		cfgName, pid, len(data), data)
	return 0
}
