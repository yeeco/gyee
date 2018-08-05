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

package dht

import (
	sch	"github.com/yeeco/gyee/p2p/scheduler"
)



//
// Query instance, just task entry point defined here, other control parameters
// about a query instance is backup in the "UserData" area of the task, and the
// pointer of the area can be obtained by scheduler's interface. See query.go to
// get more about the query instance control block pls.
//
type QryInst struct {
	tep sch.SchUserTaskEp	// task entry
}

//
// Create query instance
//
func NewQryInst() *QryInst {

	qryInst := QryInst{
		tep:	nil,
	}

	qryInst.tep = qryInst.qryInstProc

	return &qryInst
}

//
// Entry point exported to shceduler
//
func (qryInst *QryInst)TaskProc4Scheduler(ptn interface{}, msg *sch.SchMessage) sch.SchErrno {
	return qryInst.tep(ptn, msg)
}

//
// Query instance entry
//
func (qryInst *QryInst)qryInstProc(ptn interface{}, msg *sch.SchMessage) sch.SchErrno {
	return sch.SchEnoNone
}

