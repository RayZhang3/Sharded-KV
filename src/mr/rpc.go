package mr

//
// RPC definitions.
//
// remember to capitalize all names.
//

import (
	"os"
	"strconv"
	"time"
)

//
// example to show how to declare the arguments
// and reply for an RPC.
//

type ExampleArgs struct {
	X int
}

type ExampleReply struct {
	Y int
}

const (
	RPC_MAP      = 0
	RPC_REDUCE   = 1
	RPC_REQUEST  = 2
	RPC_WORKDONE = 3
	RPC_FAIL     = 4
	RPC_ALLDONE  = 5
)

// Add your RPC definitions here.

type Taskinfo struct {
	RPCtype   int
	TaskId    int
	Filename  string
	NMap      int
	NReduce   int
	StartTime time.Time
}

// Cook up a unique-ish UNIX-domain socket name
// in /var/tmp, for the coordinator.
// Can't use the current directory since
// Athena AFS doesn't support UNIX-domain sockets.
func coordinatorSock() string {
	s := "/var/tmp/824-mr-"
	s += strconv.Itoa(os.Getuid())
	return s
}
