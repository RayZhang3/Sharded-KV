package shardctrler

import (
	"bytes"
	"sync"
	"sync/atomic"

	"6.824/labgob"
	"6.824/labrpc"
	"6.824/raft"
)

type ShardCtrler struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg
	configs []Config // indexed by config num

	// Your data here.
	waitChan  map[int]chan Op
	clientSeq map[int64]int
	dead      int32 // set by Kill()
}

func (sc *ShardCtrler) getServerPersistData() []byte {
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)
	e.Encode(sc.configs)
	e.Encode(sc.clientSeq)
	data := w.Bytes()
	return data
}

func (sc *ShardCtrler) readServerPersistData(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}
	r := bytes.NewBuffer(data)
	d := labgob.NewDecoder(r)
	var configs []Config
	var clientSeq map[int64]int
	if d.Decode(&configs) != nil || d.Decode(&clientSeq) != nil {
		return
	} else {
		sc.mu.Lock()
		sc.configs = configs
		sc.clientSeq = clientSeq
		sc.mu.Unlock()
	}
}

type Op struct {
	// Your data here.
}

func (sc *ShardCtrler) applyHandler()                     {}
func (sc *ShardCtrler) applyToStateMachine(appliedOp *Op) {}

// get the wait channel for the index, if not exist, create one
// channel is used to send the appliedOp to the client
func (sc *ShardCtrler) getWaitCh(index int) (waitCh chan Op) {
	waitCh, isPresent := sc.waitChan[index]
	if isPresent {
		return waitCh
	} else {
		sc.waitChan[index] = make(chan Op, 1)
		return sc.waitChan[index]
	}
}

/*
func (sc *ShardCtrler) Join(args *JoinArgs, reply *JoinReply) {
	// Your code here.
}

func (sc *ShardCtrler) Leave(args *LeaveArgs, reply *LeaveReply) {
	// Your code here.
}

func (sc *ShardCtrler) Move(args *MoveArgs, reply *MoveReply) {
	// Your code here.
}

func (sc *ShardCtrler) Query(args *QueryArgs, reply *QueryReply) {
	// Your code here.
}
*/

func (sc *ShardCtrler) RequestHandler(args *CommandArgs, reply *CommandReply) {
	// Your code here.
	switch args.ArgsType {
	case JoinArgsType:
		PrettyDebug(dServer, "Server %d receive Join:", sc.me)
	case LeaveArgsType:
		PrettyDebug(dServer, "Server %d receive Leave:", sc.me)
	case MoveArgsType:
		PrettyDebug(dServer, "Server %d receive Move:", sc.me)
	case QueryArgsType:
		PrettyDebug(dServer, "Server %d receive Query:", sc.me)
	default:
		PrettyDebug(dServer, "Server receive wrong Request: args.ArgsType error")
	}
}

//
// the tester calls Kill() when a ShardCtrler instance won't
// be needed again. you are not required to do anything
// in Kill(), but it might be convenient to (for example)
// turn off debug output from this instance.
//
func (sc *ShardCtrler) Kill() {
	atomic.StoreInt32(&sc.dead, 1)
	sc.rf.Kill()
	// Your code here, if desired.
}
func (sc *ShardCtrler) killed() bool {
	z := atomic.LoadInt32(&sc.dead)
	return z == 1
}

// needed by shardkv tester
func (sc *ShardCtrler) Raft() *raft.Raft {
	return sc.rf
}

//
// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant shardctrler service.
// me is the index of the current server in servers[].
//
func StartServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister) *ShardCtrler {
	sc := new(ShardCtrler)
	sc.me = me

	sc.configs = make([]Config, 1)
	sc.configs[0].Groups = map[int][]string{}

	labgob.Register(Op{})
	sc.applyCh = make(chan raft.ApplyMsg)
	sc.rf = raft.Make(servers, me, persister, sc.applyCh)

	// Your code here.
	sc.waitChan = make(map[int]chan Op, 0)
	sc.clientSeq = make(map[int64]int, 0)

	return sc
}
