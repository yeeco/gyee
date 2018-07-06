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


package scheduler

import (
			"time"
			"strings"
	golog	"log"
	config	"github.com/yeeco/gyee/p2p/config"
	log		"github.com/yeeco/gyee/p2p/logger"
)

//
// Default task node for shceduler to send event
//
const rawSchTaskName = "schTsk"

var rawSchTsk = schTaskNode {
	task: schTask{name:rawSchTaskName,},
	last: nil,
	next: nil,
}

//
// Default task node for shceduler to send event
//
const rawTmTaskName = "tmTsk"

var rawTmTsk = schTaskNode {
	task: schTask{name:rawTmTaskName,},
	last: nil,
	next: nil,
}

//
// Scheduler initilization
//
func schSchedulerInit(cfg *config.Config) (*scheduler, SchErrno) {

	var sdl = new(scheduler)

	//
	// backup p2p network configuration
	//

	sdl.p2pCfg = cfg

	//
	// make maps
	//

	sdl.tkMap = make(map[schTaskName] *schTaskNode)
	sdl.tmMap = make(map[*schTmcbNode] *schTaskNode)

	//
	// setup free task node queue
	//

	for loop := 0; loop < schTaskNodePoolSize; loop++ {
		sdl.schTaskNodePool[loop].last = &sdl.schTaskNodePool[(loop - 1 + schTaskNodePoolSize) & (schTaskNodePoolSize - 1)]
		sdl.schTaskNodePool[loop].next = &sdl.schTaskNodePool[(loop + 1) & (schTaskNodePoolSize - 1)]
		sdl.schTaskNodePool[loop].task.tmIdxTab = make(map[*schTmcbNode] int)
	}

	sdl.freeSize = schTaskNodePoolSize
	sdl.tkFree = &sdl.schTaskNodePool[0]

	//
	// setup free timer node queue
	//

	for loop := 0; loop < schTimerNodePoolSize; loop++ {
		sdl.schTimerNodePool[loop].last = &sdl.schTimerNodePool[(loop - 1 + schTimerNodePoolSize) & (schTimerNodePoolSize - 1)]
		sdl.schTimerNodePool[loop].next = &sdl.schTimerNodePool[(loop + 1) & (schTimerNodePoolSize - 1)]
		sdl.schTimerNodePool[loop].tmcb.stop = make(chan bool, 1)
		sdl.schTimerNodePool[loop].tmcb.stopped = make(chan bool)
	}

	sdl.tmFreeSize = schTimerNodePoolSize
	sdl.tmFree = &sdl.schTimerNodePool[0]

	return sdl, SchEnoNone
}

//
// the common entry point for a scheduler task
//
func (sdl *scheduler)schCommonTask(ptn *schTaskNode) SchErrno {

	var queMsg	*chan schMessage
	var done 	*chan SchErrno
	var eno		SchErrno

	//
	// check pointer to task node
	//

	if ptn == nil {
		log.LogCallerFileLine("schCommonTask: invalid task node pointer")
		return SchEnoParameter
	}

	//
	// check user task more
	//

	if ptn.task.utep == nil || ptn.task.mailbox.que == nil || ptn.task.done == nil {

		log.LogCallerFileLine("schCommonTask: " +
			"invalid user task: %s",
			ptn.task.name)

		return SchEnoParameter
	}

	//
	// get chans
	//

	queMsg = ptn.task.mailbox.que
	done = &ptn.task.done

	//
	// loop to schedule, until done(or something else happened).
	//
	// Notice: if the message queue size of one user task is zero, then this means
	// the user task would be a longlong loop, which need not to be shceduled by
	// messages. In this case, we go a routine for the loop first, then we check
	// the task until it done.
	//

	if ptn.task.mailbox.size == 0 ||
		ptn.task.mailbox.que == nil ||
		cap(*ptn.task.mailbox.que) == 0 {

		log.LogCallerFileLine("schCommonTask: " +
			"dead loop user task: %s",
			ptn.task.name)

		go ptn.task.utep.TaskProc4Scheduler(ptn, nil)
	}

taskLoop:

	for {
		select {

		case msg := <-*queMsg:

			//
			// dog wakes up
			//

			ptn.task.dog.lock.Lock()
			ptn.task.dog.Inited = ptn.task.dog.HaveDog
			ptn.task.dog.lock.Unlock()

			//
			// call handler
			//

			ptn.task.utep.TaskProc4Scheduler(ptn, (*SchMessage)(&msg))

			//
			// dog sleeps
			//

			ptn.task.dog.lock.Lock()
			ptn.task.dog.Inited = false
			ptn.task.dog.lock.Unlock()

		case eno = <-*done:

			if eno != SchEnoNone {

				log.LogCallerFileLine("schCommonTask: done with eno: %d", eno)
			}

			break taskLoop
		}
	}

	//
	// see function:
	//
	// 	schTaskDone
	//	schStopTask
	//
	// for more pls
	//

	ptn.task.stopped<-true

	//
	// exit, remove user task
	//

	return sdl.schStopTaskEx(ptn)
}

//
// the common entry point for timer task
//
func (sdl *scheduler)schTimerCommonTask(ptm *schTmcbNode) SchErrno {

	//
	// backup for debug output
	//

	ptm.tmcb.taskNode.task.lock.Lock()
	var tid = ptm.tmcb.taskNode.task.tmIdxTab[ptm]
	ptm.tmcb.taskNode.task.lock.Unlock()

	var killed = false

	//
	// cleaner for absolute timer when it expired
	//

	var funcAbsTimerClean = func (tn *schTmcbNode) {

		//
		// clear timer control block and remove it from maps, notice that the task
		// node should not be released here, it's accessed later after this function
		// called; and do not ret the timer control block node here.
		//

		delete(tn.tmcb.taskNode.task.tmIdxTab, tn)
		delete(sdl.tmMap, tn)
		tn.tmcb.taskNode.task.tmTab[tid] = nil

		tn.tmcb.name	= ""
		tn.tmcb.tmt		= schTmTypeNull
		tn.tmcb.dur		= 0
		tn.tmcb.extra	= nil
	}

	//
	// cleaning job for cyclic timers are same as those absolute ones
	//

	var funcCycTimerClean = funcAbsTimerClean

	var tm *time.Ticker

	//
	// check pointer to timer node
	//

	if ptm == nil {
		log.LogCallerFileLine("schTimerCommonTask: invalid timer pointer")
		return SchEnoParameter
	}

	//
	// check timer type to deal with it
	//

	if ptm.tmcb.tmt == schTmTypePeriod {

		//
		// period, we loop for ever until killed
		//

		tm = time.NewTicker(ptm.tmcb.dur)

timerLoop:

		for {

			select {

			case <-tm.C:

				ptm.tmcb.taskNode.task.lock.Lock()

				if eno := schSendTimerEvent(ptm); eno != SchEnoNone {

					log.LogCallerFileLine("schTimerCommonTask: " +
						"send timer event failed, eno: %d, task: %s",
						eno,
						ptm.tmcb.taskNode.task.name)
				}

			case stop := <-ptm.tmcb.stop:

				ptm.tmcb.taskNode.task.lock.Lock()

				if stop == true {

					tm.Stop()
					killed = true
					funcCycTimerClean(ptm)

					break timerLoop
				}
			}

			ptm.tmcb.taskNode.task.lock.Unlock()
		}
	} else if ptm.tmcb.tmt == schTmTypeAbsolute {

		//
		// absolute, check duration
		//

		ptm.tmcb.taskNode.task.lock.Lock()

		dur := ptm.tmcb.dur

		if dur <= 0 {

			log.LogCallerFileLine("schTimerCommonTask: " +
				"invalid absolute timer duration:%d",
				ptm.tmcb.dur)

			ptm.tmcb.taskNode.task.lock.Unlock()

			return SchEnoParameter
		}

		ptm.tmcb.taskNode.task.lock.Unlock()

		//
		// send timer event after duration specified. we could not call time.After
		// directly, or we will blocked until timer expired, go a routine instead.
		//

		var to = make(chan bool)
		go func() {
			<-time.After(dur)
			to<-true
		}()

absTimerLoop:

		for {

			select {

			case <-to:

				//
				// Notice: here we must try to obtain the task first, since function
				// schRetTimerNode would try to get the lock of the shceduler,
				// see function schKillTimer for details please.
				//

				ptm.tmcb.taskNode.task.lock.Lock()
				sdl.lock.Lock()

				if eno := schSendTimerEvent(ptm); eno != SchEnoNone {

					log.LogCallerFileLine("schTimerCommonTask: "+
						"send timer event failed, eno: %d, task: %s",
						eno,
						ptm.tmcb.taskNode.task.name)
				}

				funcAbsTimerClean(ptm)
				sdl.lock.Unlock()

				break absTimerLoop

			case stop := <-ptm.tmcb.stop:

				ptm.tmcb.taskNode.task.lock.Lock()
				sdl.lock.Lock()

				if stop == true {

					funcAbsTimerClean(ptm)
					killed = true
				}

				sdl.lock.Unlock()

				break absTimerLoop
			}
		}
	} else {

		//
		// unknown
		//

		log.LogCallerFileLine("schTimerCommonTask: " +
			"invalid timer type: %d",
			ptm.tmcb.tmt)

		return SchEnoParameter
	}

	//
	// exit, notice that here task is still locked, and only when killed we
	// need more cleaning job.
	//

	if killed {

		ptm.tmcb.stopped<-true

		close(ptm.tmcb.stop)
		close(ptm.tmcb.stopped)

		ptm.tmcb.stop = nil
		ptm.tmcb.stopped = nil
	}

	ptm.tmcb.taskNode.task.lock.Unlock()
	ptm.tmcb.taskNode = nil

	if eno := sdl.schRetTimerNode(ptm); eno != SchEnoNone {

		log.LogCallerFileLine("schTimerCommonTask: " +
			"schRetTimerNode failed, eno: %d",
			eno)

		return eno
	}

	return SchEnoNone
}

//
// Get timer node
//
func (sdl *scheduler)schGetTimerNode() (SchErrno, *schTmcbNode) {

	var tmn *schTmcbNode = nil

	sdl.lock.Lock()
	defer sdl.lock.Unlock()

	//
	// if empty
	//

	if sdl.tmFree == nil {
		log.LogCallerFileLine("schGetTimerNode: free queue is empty")
		return SchEnoResource, nil
	}

	//
	// dequeue one node
	//

	tmn = sdl.tmFree

	if tmn.last == tmn && tmn.next == tmn {

		if sdl.tmFreeSize - 1 != 0 {

			log.LogCallerFileLine("schGetTimerNode: " +
				"internal errors, should be 0, but free size: %d",
				sdl.tmFreeSize)

			return SchEnoInternal, nil
		}

		sdl.tmFreeSize--
		sdl.tmFree = nil

	} else {

		if sdl.tmFreeSize - 1 <= 0 {

			log.LogCallerFileLine("schGetTimerNode: " +
				"internal errors, should equal or less than 0, but free size: %d",
				sdl.tmFreeSize)

			return SchEnoInternal, nil
		}

		last := tmn.last
		next := tmn.next
		next.last = last
		last.next = next

		sdl.tmFree = next
		sdl.tmFreeSize--
	}

	tmn.next = tmn
	tmn.last = tmn

	return SchEnoNone, tmn
}

//
// Ret timer node to free queue
//
func (sdl *scheduler)schRetTimerNode(ptm *schTmcbNode) SchErrno {

	if ptm == nil {
		log.LogCallerFileLine("schRetTimerNode: invalid timer node pointer")
		return SchEnoParameter
	}

	sdl.lock.Lock()
	defer  sdl.lock.Unlock()

	//
	// enqueue a node
	//

	if sdl.tmFree == nil {

		ptm.last = ptm
		ptm.next = ptm

	} else {

		last := sdl.tmFree.last
		ptm.last = last
		last.next = ptm
		ptm.next = sdl.tmFree
		sdl.tmFree.last = ptm

	}

	sdl.tmFree = ptm
	sdl.tmFreeSize++

	return SchEnoNone
}

//
// Get task node
//
func (sdl *scheduler)schGetTaskNode() (SchErrno, *schTaskNode) {

	var tkn *schTaskNode = nil

	sdl.lock.Lock()
	defer sdl.lock.Unlock()

	//
	// if free node queue empty
	//

	if sdl.tkFree== nil {
		log.LogCallerFileLine("schGetTaskNode: free queue is empty")
		return SchEnoResource, nil
	}

	//
	// dequeue one node
	//

	tkn = sdl.tkFree

	if tkn.last == tkn && tkn.next == tkn {

		sdl.tkFree = nil

		if sdl.freeSize--; sdl.freeSize != 0 {

			log.LogCallerFileLine("schGetTaskNode: internal errors")

			return SchEnoInternal, nil
		}

	} else {

		last := tkn.last
		next := tkn.next
		next.last = last
		last.next = next
		sdl.tkFree = next

		if sdl.freeSize--; sdl.freeSize <= 0 {

			log.LogCallerFileLine("schGetTaskNode: internal errors")
			return SchEnoInternal, nil
		}
	}

	tkn.next = tkn
	tkn.last = tkn

	return SchEnoNone, tkn
}

//
// Ret task node
//
func (sdl *scheduler)schRetTaskNode(ptn *schTaskNode) SchErrno {

	if ptn == nil {
		log.LogCallerFileLine("schRetTaskNode: invalid task node pointer")
		return SchEnoParameter
	}

	sdl.lock.Lock()
	defer  sdl.lock.Unlock()

	//
	// enqueue a node
	//

	if sdl.tkFree == nil {

		ptn.last = ptn
		ptn.next = ptn

	} else {

		last := sdl.tkFree.last
		ptn.last = last
		last.next = ptn
		ptn.next = sdl.tkFree
		sdl.tkFree.last = ptn

	}

	sdl.tkFree = ptn
	sdl.freeSize++

	return SchEnoNone
}

//
// Task node enter the busy queue
//
func (sdl *scheduler)schTaskBusyEnque(ptn *schTaskNode) SchErrno {
	
	if ptn == nil {
		log.LogCallerFileLine("schTaskBusyEnque: invalid task node pointer")
		return SchEnoParameter
	}

	//
	// lock/unlock scheduler control block
	//

	sdl.lock.Lock()
	defer  sdl.lock.Unlock()

	//
	// enqueue a node
	//

	if sdl.tkBusy == nil {

		ptn.last = ptn
		ptn.next = ptn

	} else {

		last := sdl.tkBusy.last
		ptn.last = last
		last.next = ptn
		ptn.next = sdl.tkBusy
		sdl.tkBusy.last = ptn

	}

	sdl.tkBusy = ptn
	sdl.busySize++

	return SchEnoNone
}

//
// Task node dequeue from the busy queue
//
func (sdl *scheduler)schTaskBusyDeque(ptn *schTaskNode) SchErrno {

	if ptn == nil {
		log.LogCallerFileLine("schTaskBusyDeque: invalid parameter")
		return SchEnoParameter
	}

	//
	// lock/unlock schduler control block
	//

	sdl.lock.Lock()
	defer sdl.lock.Unlock()

	//
	// remove the busy node
	//

	if sdl.busySize <= 0 {

		log.LogCallerFileLine("schTaskBusyDeque: invalid parameter")
		return SchEnoInternal

	} else if sdl.busySize == 1 {

		if sdl.tkBusy != ptn {

			log.LogCallerFileLine("schTaskBusyDeque: invalid parameter")
			return SchEnoInternal

		} else {

			sdl.tkBusy = nil
			sdl.busySize = 0
			return SchEnoNone
		}
	}

	if ptn.last == ptn && ptn.next == ptn {

		if ptn == sdl.tkBusy {

			sdl.tkBusy = nil

		} else {

			log.LogCallerFileLine("schTaskBusyDeque: internal errors")

			return SchEnoInternal
		}
		if sdl.busySize--; sdl.busySize != 0 {

			log.LogCallerFileLine("schTaskBusyDeque: internal errors")

			return SchEnoInternal
		}
	} else {

		last := ptn.last
		next := ptn.next
		last.next = next
		next.last = last

		if sdl.tkBusy == ptn {

			sdl.tkBusy = next
		}

		if sdl.busySize--; sdl.busySize <= 0 {

			log.LogCallerFileLine("schTaskBusyDeque: internal errors")

			return SchEnoInternal
		}
	}

	return SchEnoNone
}

//
// Send timer event to user task when timer expired
//
func schSendTimerEvent(ptm *schTmcbNode) SchErrno {

	//
	// get owner task
	//

	var task = &ptm.tmcb.taskNode.task

	//
	// setup timer event message. notice that the sender is the scheduler indeed,
	// we set sender pointer to raw timer task in this case; and the extra set when
	// timer crated is also return to timer owner.
	//

	var msg = schMessage{
		sender:	&rawTmTsk,
		recver:	ptm.tmcb.taskNode,
		Id:		EvSchNull,
		Body:	ptm.tmcb.extra,
	}

	msg.Id = EvTimerBase + ptm.tmcb.utid

	//
	// put message to task mailbox
	//

	*task.mailbox.que<-msg

	return SchEnoNone
}

//
// Create a single task
//
type schTaskDescription SchTaskDescription

func (sdl *scheduler)schCreateTask(taskDesc *schTaskDescription) (SchErrno, interface{}){

	var eno SchErrno
	var ptn *schTaskNode

	if taskDesc == nil {
		log.LogCallerFileLine("schCreateTask: invalid user task description")
		return SchEnoParameter, nil
	}

	//
	// get task node
	//

	if eno, ptn = sdl.schGetTaskNode(); eno != SchEnoNone || ptn == nil {

		log.LogCallerFileLine("schCreateTask: " +
			"schGetTaskNode failed, eno: %d",
			eno)

		return eno, nil
	}

	//
	// check if a nil mailbox
	//

	if ptn.task.mailbox.que != nil {

		close(*ptn.task.mailbox.que)
		ptn.task.mailbox.que = nil
		ptn.task.mailbox.size = 0
	}

	//
	// check if a nil done channel
	//

	if ptn.task.done != nil {

		close(ptn.task.done)
		ptn.task.done = nil
	}

	if ptn.task.stopped != nil {

		close(ptn.task.stopped)
		ptn.task.stopped = nil
	}

	//
	// setup user task
	//

	ptn.task.sdl			= sdl
	ptn.task.name			= strings.TrimSpace(taskDesc.Name)
	ptn.task.utep			= taskDesc.Ep
	mq 						:= make(chan schMessage, taskDesc.MbSize)
	ptn.task.mailbox.que	= &mq
	ptn.task.mailbox.size	= taskDesc.MbSize
	ptn.task.done			= make(chan SchErrno, 1)
	ptn.task.stopped		= make(chan bool, 1)
	ptn.task.dog			= schWatchDog(*taskDesc.Wd)
	ptn.task.dieCb			= taskDesc.DieCb
	ptn.task.userData		= taskDesc.UserDa

	//
	// make timer table
	//

	for idx, ptm := range ptn.task.tmTab {

		if ptm != nil {

			ptn.task.tmTab[idx] = nil
		}
	}

	//
	// make timer map clean
	//

	for k := range ptn.task.tmIdxTab {

		delete(ptn.task.tmIdxTab, k)
	}

	//
	// map task name to task node pointer. some dynamic tasks might have empty
	// task name, in this case, the task node pointer would not be mapped in
	// table, and this task could not be found by function schGetTaskNodeByName
	//

	sdl.lock.Lock()

	if len(ptn.task.name) <= 0 {

		log.LogCallerFileLine("schCreateTask: task with empty name")

	} else if _, dup := sdl.tkMap[schTaskName(ptn.task.name)]; dup == true {

		log.LogCallerFileLine("schCreateTask: " +
			"duplicated task name: %s",
			ptn.task.name)

		sdl.lock.Unlock()

		return SchEnoDuplicated, nil

	} else {

		sdl.tkMap[schTaskName(ptn.task.name)] = ptn
	}

	sdl.lock.Unlock()

	//
	// put task node to busy queue
	//

	if eno := sdl.schTaskBusyEnque(ptn); eno != SchEnoNone {

		log.LogCallerFileLine("schCreateTask: " +
			"schTaskBusyEnque failed, rc: %d",
			eno)

		return eno, nil
	}

	//
	// start task to work according the flag, if the flag is invalid, we suspend
	// the user task and inform caller with SchEnoSuspended returned.
	//

	eno = SchEnoNone

	if taskDesc.Flag == SchCreatedGo {

		ptn.task.goStatus = SchCreatedGo
		go sdl.schCommonTask(ptn)

	} else if taskDesc.Flag == SchCreatedSuspend {

		ptn.task.goStatus = SchCreatedSuspend

	} else {

		log.LogCallerFileLine("schCreateTask: " +
			"suspended for invalid goStatus flag: %d",
			taskDesc.Flag)

		ptn.task.goStatus = SchCreatedSuspend
		eno = SchEnoSuspended
	}

	return eno, ptn
}

//
// Start a single task by task name
//
func (sdl *scheduler)schStartTask(name string) SchErrno {

	//
	// Notice: only those suspended user task can be started, so this function does
	// not create new user task, instead, it try to find the suspended task and then
	// start it.
	//

	//
	// get task node pointer by name
	//

	eno, ptn := sdl.schGetTaskNodeByName(name)

	if eno != SchEnoNone || ptn == nil {

		log.LogCallerFileLine("schStartTask: " +
			"schGetTaskNodeByName failed, name: %s, eno: %d, ptn: %X",
			name, eno, ptn)

		return eno
	}

	//
	// can only a suspended task be started
	//

	if ptn.task.goStatus != SchCreatedSuspend {

		log.LogCallerFileLine("schStartTask: " +
			"invalid user task status: %d",
			ptn.task.goStatus)

		return SchEnoMismatched
	}

	//
	// go the user task
	//

	ptn.task.goStatus = SchCreatedGo
	go sdl.schCommonTask(ptn)

	return SchEnoNone
}

//
// Start task by task node pointer
//
func (sdl *scheduler)schStartTaskEx(ptn *schTaskNode) SchErrno {

	if ptn == nil {
		log.LogCallerFileLine("schStartTaskEx: invalid pointer to task node")
		return SchEnoParameter
	}

	//
	// can only a suspended task be started
	//

	if ptn.task.goStatus != SchCreatedSuspend {

		log.LogCallerFileLine("schStartTaskEx: " +
			"invalid user task status: %d",
			ptn.task.goStatus)

		return SchEnoMismatched
	}

	//
	// go the user task
	//

	ptn.task.goStatus = SchCreatedGo
	go sdl.schCommonTask(ptn)

	return SchEnoNone
}

//
// Stop a single task by task name
//
func (sdl *scheduler)schStopTask(name string) SchErrno {

	//
	// Attention: this function MUST only be called to kill a task than the
	// caller itself, or will get a deadlock.
	//

	//
	// get task node pointer by name
	//

	eno, ptn := sdl.schGetTaskNodeByName(name)

	if eno != SchEnoNone || ptn == nil {

		log.LogCallerFileLine("schStopTask: " +
			"schGetTaskNodeByName failed, name: %s, eno: %d, ptn: %X",
			name, eno, ptn)

		return eno
	}

	//
	// done with "killed" signal and then wait "stopped"
	//

	ptn.task.done<-SchEnoKilled

	<-ptn.task.stopped

	return SchEnoNone
}

//
// Stop a single task by task pointer
//
func (sdl *scheduler)schStopTaskEx(ptn *schTaskNode) SchErrno {

	//
	// Seems need not to lock the scheduler control block ?! for functions
	// called here had applied the lock if necessary when they called in.
	//

	var eno SchErrno

	if ptn == nil {
		log.LogCallerFileLine("schStopTaskEx: invalid task node pointer")
		return SchEnoParameter
	}

	//
	// dequeue form busy queue
	//

	if eno := sdl.schTaskBusyDeque(ptn); eno != SchEnoNone {

		log.LogCallerFileLine("schStopTaskEx: " +
			"schTaskBusyDeque failed, eno: %d",
			eno)

		return eno
	}

	//
	// callback user task to die.
	//

	if ptn.task.dieCb != nil {

		if eno = ptn.task.dieCb(ptn); eno != SchEnoNone {

			//
			// Notice: here dieCb return failed, but we SHOULD NOT care it since
			// this function is called into a user context, we can not return here
			// or the resources allocated to this task will leak. We had to go
			// ahead.
			//

			log.LogCallerFileLine("schStopTaskEx: "+
				"dieCb failed, task: %s, eno: %d",
				ptn.task.name,
				eno)
		}
	}

	//
	// stop user timers
	//

	if eno = sdl.schKillTaskTimers(&ptn.task); eno != SchEnoNone {

		log.LogCallerFileLine("schStopTaskEx: " +
			"schKillTaskTimers faild, eno: %d",
			eno)

		return eno
	}

	//
	// clean the user task control block
	//

	if eno = sdl.schTcbClean(&ptn.task); eno != SchEnoNone {

		log.LogCallerFileLine("schStopTaskEx: " +
			"schTcbClean faild, eno: %d",
			eno)

		return eno
	}

	//
	// free task node
	//

	if eno = sdl.schRetTaskNode(ptn); eno != SchEnoNone {

		log.LogCallerFileLine("schStopTaskEx: " +
			"schRetTimerNode failed, task: %s, eno: %d",
			ptn.task.name,
			eno)

		return  eno
	}

	//
	// remove name to task node pointer map
	//

	if len(ptn.task.name) > 0 {

		sdl.lock.Lock()
		delete(sdl.tkMap, schTaskName(ptn.task.name))
		sdl.lock.Unlock()
	}

	return SchEnoNone
}

//
// Make user task control block clean
//
func (sdl *scheduler)schTcbClean(tcb *schTask) SchErrno {

	tcb.lock.Lock()
	defer tcb.lock.Unlock()

	if tcb == nil {
		log.LogCallerFileLine("schTcbClean: invalid task control block pointer")
		return SchEnoParameter
	}

	tcb.name				= ""
	tcb.utep				= nil
	tcb.dog.Cycle			= SchDefaultDogCycle
	tcb.dog.BiteCounter		= 0
	tcb.dog.DieThreshold	= SchDefaultDogDieThresold
	tcb.dieCb				= nil
	tcb.goStatus			= SchCreatedSuspend

	close(*tcb.mailbox.que)
	tcb.mailbox.que = nil
	tcb.mailbox.size = 0

	close(tcb.done)
	tcb.done = nil
	close(tcb.stopped)
	tcb.stopped = nil

	for loop := 0; loop < schMaxTaskTimer; loop++ {

		if tcb.tmTab[loop] != nil {

			delete(tcb.tmIdxTab, tcb.tmTab[loop])
			delete(sdl.tmMap, tcb.tmTab[loop])

			tcb.tmTab[loop] = nil
		}
	}

	return SchEnoNone
}

//
// Delete a single task by task name
//
func (sdl *scheduler)schDeleteTask(name string) SchErrno {

	//
	// Currently, "Delete" implented as "Stop"
	//

	return sdl.schStopTask(name)
}

//
// Send message to a specific task
//
func (sdl *scheduler)schSendMsg(msg *schMessage) (eno SchErrno) {

	//
	// check the message to be sent
	//

	if msg == nil {
		log.LogCallerFileLine("schSendMsg: invalid message")
		return SchEnoParameter
	}

	if msg.sender == nil {
		log.LogCallerFileLine("schSendMsg: invalid sender")
		return SchEnoParameter
	}

	if msg.recver == nil {
		log.LogCallerFileLine("schSendMsg: invalid receiver")
		return SchEnoParameter
	}

	//
	// put message to receiver mailbox. More work might be needed, such as
	// checking against the sender and receiver name to see if they are in
	// busy queue; checking go status of both to see if they are matched,
	// and so on.
	//

	if msg.recver.task.mailbox.que == nil {
		log.LogCallerFileLine("schSendMsg: mailbox of target is empty")
		return SchEnoInternal
	}

	*msg.recver.task.mailbox.que<-*msg

	return SchEnoNone
}

//
// Set a timer: extra passed in, which would ret to timer owner when
// timer expired; and timer identity returned to caller. So when timer
// event received, one can determine which timer it is, and extract
// those extra put into timer when it's created.
//
func (sdl *scheduler)schSetTimer(ptn *schTaskNode, tdc *timerDescription) (SchErrno, int) {

	//
	// Here we had got a task node, since the timer is still not in running,
	// seems we had no need to do anything to protect the task. Notice that
	// function schGetTimerNode would get the scheduler lock internal
	// itself, see it pls.
	//

	var tid int
	var eno SchErrno
	var ptm *schTmcbNode

	if ptn == nil || tdc == nil {
		log.LogCallerFileLine("schSetTimer: invalid parameter(s)")
		return SchEnoParameter, schInvalidTid
	}

	//
	// check if some user task timers are free
	//

	for tid = 0; tid < schMaxTaskTimer; tid++ {
		if ptn.task.tmTab[tid] == nil {
			break
		}
	}

	if tid >= schMaxTaskTimer {
		log.LogCallerFileLine("schSetTimer: too much, timer table is full")
		return SchEnoResource, schInvalidTid
	}

	//
	// get a timer node
	//

	if eno, ptm = sdl.schGetTimerNode(); eno != SchEnoNone || ptm == nil {

		log.LogCallerFileLine("schSetTimer: " +
			"schGetTimerNode failed, eno: %d",
			eno)

		return eno, schInvalidTid
	}

	ptm.tmcb.stopped = make(chan  bool)
	ptm.tmcb.stop = make(chan bool, 1)

	//
	// backup timer node
	//

	ptn.task.tmTab[tid] = ptm
	ptn.task.tmIdxTab[ptm] = tid

	sdl.lock.Lock()
	sdl.tmMap[ptm] = ptn
	sdl.lock.Unlock()

	//
	// setup timer control block
	//

	tcb 			:= &ptm.tmcb
	tcb.name		= tdc.Name
	tcb.utid		= tdc.Utid
	tcb.tmt			= schTimerType(tdc.Tmt)
	tcb.dur			= tdc.Dur
	tcb.taskNode	= ptn
	tcb.extra		= tdc.Extra

	//
	// go timer common task for timer
	//

	go sdl.schTimerCommonTask(ptm)

	return SchEnoNone, tid
}

//
// Kill a timer
//
func (sdl *scheduler)schKillTimer(ptn *schTaskNode, tid int) SchErrno {

	//
	// para check
	//

	if ptn == nil || tid < 0 || tid > schMaxTaskTimer {
		log.LogCallerFileLine("schKillTimer: invalid parameter(s)")
		return SchEnoParameter
	}

	//
	// lock the task, we can't use defer here. see function schTimerCommonTask
	// for more about sync please. we would unlock bellow, see it.
	//

	ptn.task.lock.Lock()

	//
	// Notice: when try to kill a timer, the timer might have been expired, but
	// the message sent about this is still not received by user task. In this
	// case, user task would get "SchEnoNotFound", and this is not fault of the
	// scheduler really, but the user task would be confuse with the event about
	// a timer killed failed(SchEnoNotFound) received later. We still not solve
	// this issue now.
	//

	if ptn.task.tmTab[tid] == nil {
		ptn.task.lock.Unlock()
		return SchEnoNone
	}

	//
	// emit stop signal and wait stopped signal
	//

	var tcb = &ptn.task.tmTab[tid].tmcb
	tcb.stop<-true

	ptn.task.lock.Unlock()

	if stopped := <-tcb.stopped; stopped {

		return SchEnoNone
	}

	return SchEnoInternal
}

//
// Kill all active timers owned by a task
//
func (sdl *scheduler)schKillTaskTimers(task *schTask) SchErrno {

	if task == nil {
		log.LogCallerFileLine("schKillTaskTimers: nil task pointer")
		return SchEnoParameter
	}

	task.lock.Lock()
	defer task.lock.Unlock()

	for tm, idx := range task.tmIdxTab {

		//
		// check
		//

		if tm != task.tmTab[idx] {

			log.LogCallerFileLine("schKillTaskTimers: " +
				"timer node pointer mismatched, tm: %p, idx: %d, tmTab: %p",
				tm,
				idx,
				task.tmTab[idx])

			return SchEnoInternal
		}

		//
		// done the timer task
		//

		tm.tmcb.stop<-true

		//
		// wait until done
		//

		if stopped := <-tm.tmcb.stopped; stopped != true {

			log.LogCallerFileLine("schKillTaskTimers: "+
				"timer stopped with: %t",
				stopped)
		}
	}

	return SchEnoNone
}


//
// Get task node pointer by task name
//
func (sdl *scheduler)schGetTaskNodeByName(name string) (SchErrno, *schTaskNode) {

	sdl.lock.Lock()
	defer sdl.lock.Unlock()

	// if exist
	if _, err := sdl.tkMap[schTaskName(name)]; !err {
		return SchEnoNotFound, nil
	}

	// yes
	return SchEnoNone, sdl.tkMap[schTaskName(name)]
}

//
// Done a task
//
func (sdl *scheduler)schTaskDone(ptn *schTaskNode, eno SchErrno) SchErrno {

	//
	// Notice: this function should be called inside a task to kill itself, so it
	// could not to poll the "stopped" signal, for this signal is fired by itself,
	// to do this will result in a deadlock.
	//

	if ptn == nil {
		log.LogCallerFileLine("schTaskDone: invalid task node pointer")
		return SchEnoParameter
	}

	//
	// some tasks might have empty names
	//

	var tskName = ""

	if tskName = sdl.schGetTaskName(ptn); len(tskName) == 0 {

		log.LogCallerFileLine("schTaskDone: done task without name")

	}

	//
	// see function schCommonTask for more please
	//

	ptn.task.done<-eno

	//
	// when coming here, it just the "done" fired, it's still not killed really,
	// see function schCommonTask for more pls. we could not try to poll the
	// "stopped" signal here, since we need our task to try the "done" we fired
	// above, there the "stopped" would be fired, but no one would care it is the
	// case.
	//

	return SchEnoNone
}

//
// Get user data area pointer
//
func (sdl *scheduler)schGetUserDataArea(ptn *schTaskNode) interface{} {
	if ptn == nil {
		return nil
	}
	return ptn.task.userData
}

//
// Set user data area pointer
//
func (sdl *scheduler)schSetUserDataArea(ptn *schTaskNode, uda interface{}) SchErrno {
	if ptn == nil {
		log.LogCallerFileLine("schSetUserDataArea: invalid task node pointer")
		return SchEnoParameter
	}
	ptn.task.userData = uda
	return SchEnoNone
}

//
// Get task name
//
func (sdl *scheduler)schGetTaskName(ptn *schTaskNode) string {
	if ptn == nil {
		return ""
	}
	return ptn.task.name
}

//
// Start scheduler
//
func (sdl *scheduler)schSchedulerStart(tsd []TaskStaticDescription, tpo []string) (eno SchErrno, name2Ptn *map[string]interface{}){

	golog.Printf("schSchedulerStart:")
	golog.Printf("schSchedulerStart:")
	golog.Printf("schSchedulerStart: going to start ycp2p scheduler ...")
	golog.Printf("schSchedulerStart:")
	golog.Printf("schSchedulerStart:")

	var po = schMessage {
		sender:	&rawSchTsk,
		recver: nil,
		Id:		EvSchPoweron,
		Body:	nil,
	}

	var ptn interface{} = nil

	var tkd  = schTaskDescription {
		MbSize:	schMaxMbSize,
		Wd:		&SchWatchDog {Cycle:SchDefaultDogCycle, DieThreshold:SchDefaultDogDieThresold},
		Flag:	SchCreatedGo,
	}

	var name2PtnMap = make(map[string] interface{})

	if len(tsd) <= 0 {
		log.LogCallerFileLine("schSchedulerStart: static task table is empty")
		return SchEnoParameter, nil
	}

	//
	// loop the static table table
	//

	for loop, desc := range tsd {

		log.LogCallerFileLine("schSchedulerStart: " +
			"start a static task, idx: %d, name: %s",
			loop,
			desc.Name)

		//
		// setup task description. notice here the "Flag" always set to SchCreatedGo,
		// so task routine always goes when schCreateTask called, and later we
		// would not send poweron to an user task if it's flag (tsd[loop].Flag) is not
		// SchCreatedGo(SchCreatedSuspend), see bellow pls.
		//

		tkd.Name	= tsd[loop].Name
		tkd.DieCb	= tsd[loop].DieCb
		tkd.Ep		= tsd[loop].Tep
		tkd.Flag	= SchCreatedGo

		if tsd[loop].MbSize < 0 {
			tkd.MbSize = schMaxMbSize
		} else {
			tkd.MbSize = tsd[loop].MbSize
		}

		//
		// create task
		//

		if eno, ptn = sdl.schCreateTask(&tkd); eno != SchEnoNone {

			log.LogCallerFileLine("schSchedulerStart: " +
				"schCreateTask failed, task: %s",
				tkd.Name)

			return SchEnoParameter, nil
		}

		//
		// backup task node pointer by name
		//

		name2PtnMap[tkd.Name] = ptn

		//
		// send poweron event to task created aboved if it is required to be shceduled
		// at once; if the flag is SchCreatedSuspend, here JUST no poweron sending, BUT
		// the routine is going! see aboved pls.
		//

		if tsd[loop].Flag == SchCreatedGo {

			po.recver = ptn.(*schTaskNode)

			if eno = sdl.schSendMsg(&po); eno != SchEnoNone {

				log.LogCallerFileLine("schSchedulerStart: "+
					"schSendMsg failed, event: EvSchPoweron, eno: %d, task: %s",
					eno,
					tkd.Name)

				return eno, nil
			}
		}
	}

	//
	// send poweron event for those taskes registed in table "tpo" passed in
	//

	for _, name := range tpo {

		log.LogCallerFileLine("schSchedulerStart: send poweron to task: %s", name)

		eno, tsk := sdl.schGetTaskNodeByName(name)

		if eno != SchEnoNone {

			log.LogCallerFileLine("schSchedulerStart: " +
				"schGetTaskNodeByName failed, eno: %d, name: %s",
				eno,
				name)

			continue
		}

		if tsk == nil {

			log.LogCallerFileLine("schSchedulerStart: " +
				"nil task node pointer, eno: %d, name: %s",
				eno,
				name)

			continue
		}

		po.recver = tsk

		if eno = sdl.schSendMsg(&po); eno != SchEnoNone {

			log.LogCallerFileLine("schSchedulerStart: "+
				"schSendMsg failed, event: EvSchPoweron, eno: %d, task: %s",
				eno,
				name)

			return eno, nil
		}
	}

	golog.Printf("schSchedulerStart:")
	golog.Printf("schSchedulerStart:")
	golog.Printf("schSchedulerStart: it's ok, ycp2p in running")
	golog.Printf("schSchedulerStart:")
	golog.Printf("schSchedulerStart:")

	return SchEnoNone, &name2PtnMap
}
