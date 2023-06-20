package kvraft

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"6.824/labgob"
	"6.824/labrpc"
	"6.824/raft"
)

const Debug = false

func DPrintf(format string, a ...interface{}) (n int, err error) {
	if Debug {
		log.Printf(format, a...)
	}
	return
}

type Op struct {
	// Your definitions here.
	// Field names must start with capital letters,
	// otherwise RPC will break.
	ClientID int64
	SeqNum   int
	Key      string
	Value    string
	Optype   string
}

func (op *Op) String() string {
	return fmt.Sprintf("Op{ClientID:%d, SeqNum:%d, Key:%s, Value:%s, Optype:%s}", op.ClientID, op.SeqNum, op.Key, op.Value, op.Optype)
}

type KVServer struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg
	dead    int32 // set by Kill()

	maxraftstate int // snapshot if log grows this big

	// Your definitions here.
	currentState map[string]string
	waitChan     map[int]chan Op
	clientSeq    map[int64]int
}

func (kv *KVServer) String() {
	fmt.Printf("KVServer{me:%d, currentState:%v, waitChan:%v, clientSeq:%v}\n", kv.me, kv.currentState, kv.waitChan, kv.clientSeq)
}

func (kv *KVServer) applyHandler() {
	for !kv.killed() {
		select {
		case applyMsg := <-kv.applyCh:
			if applyMsg.CommandValid == false {
				PrettyDebug(dServer, "Server%d get invalid command", kv.me)
				continue
			} else {
				op, _ := applyMsg.Command.(Op)
				// PrettyDebug(dServer, "Server%d get valid command %s", kv.me, op.String())
				appliedOp := Op{op.ClientID, op.SeqNum, op.Key, op.Value, op.Optype}
				kv.mu.Lock()

				// check if the client session is existing, if not, init one with 0
				_, clientIsPresent := kv.clientSeq[appliedOp.ClientID]
				if !clientIsPresent {
					kv.clientSeq[appliedOp.ClientID] = 0
				}

				commandIndex := applyMsg.CommandIndex
				// If sequenceNum already processed from client, reply OK with stored response

				// Check duplicated command
				// If duplicated command, reply OK with stored response
				if kv.clientSeq[appliedOp.ClientID] >= appliedOp.SeqNum {
					PrettyDebug(dServer, "Server%d sequenceNum already processed from client %d, appliedOp: %s, kv.clientSeq: %v",
						kv.me, appliedOp.ClientID, appliedOp.String(), kv.clientSeq)
					if appliedOp.Optype == "Get" {
						appliedOp.Value = kv.currentState[appliedOp.Key]
					}
					isLeader, currentTerm, _ := kv.rf.RaftState()
					if applyMsg.ApplyTerm != currentTerm || !isLeader {
						kv.mu.Unlock()
						continue
					}
					waitCh, isPresent := kv.waitChan[commandIndex]
					if isPresent {
						waitCh <- appliedOp
					}
					kv.mu.Unlock()
				} else {
					kv.applyToStateMachine(&appliedOp)
					// if the channel is existing, and the leader is still alive, send the appliedOp to the channel
					isLeader, currentTerm, _ := kv.rf.RaftState()
					if applyMsg.ApplyTerm != currentTerm || !isLeader {
						kv.mu.Unlock()
						continue
					}
					waitCh, isPresent := kv.waitChan[commandIndex]
					if isPresent {
						waitCh <- appliedOp
					}
					kv.mu.Unlock()
				}
			}
		}
	}
}

func (kv *KVServer) applyToStateMachine(appliedOp *Op) {
	// If sequenceNum not processed, store response and reply OK
	// update the clientSeq
	// apply the command to state machine
	kv.clientSeq[appliedOp.ClientID] = appliedOp.SeqNum
	switch appliedOp.Optype {
	case "Get":
		appliedOp.Value = kv.currentState[appliedOp.Key]
	case "Put":
		kv.currentState[appliedOp.Key] = appliedOp.Value
	case "Append":
		currValue := kv.currentState[appliedOp.Key]
		kv.currentState[appliedOp.Key] = currValue + appliedOp.Value
	}
	PrettyDebug(dServer, "Server%d apply command %s, kv.currentState:%v", kv.me, appliedOp.String(), kv.currentState)
}

// get the wait channel for the index, if not exist, create one
// channel is used to send the appliedOp to the client
func (kv *KVServer) getWaitCh(index int) (waitCh chan Op) {
	waitCh, isPresent := kv.waitChan[index]
	if isPresent {
		return waitCh
	} else {
		kv.waitChan[index] = make(chan Op)
		return kv.waitChan[index]
	}
}

func (kv *KVServer) Get(args *GetArgs, reply *GetReply) {
	// Your code here.

	isLeader, _, leaderHint := kv.rf.RaftState()

	// Reply NOT_Leader if not leader, providing hint when available
	if !isLeader {
		reply.Err = ErrWrongLeader
		reply.LeaderHint = leaderHint
		return
	}

	kv.mu.Lock()
	// Append command to log, replicate and commit it
	op := Op{args.ClientID, args.SeqNum, args.Key, "", "Get"}
	PrettyDebug(dServer, "Server%d insert GET command to raft, COMMAND %s", kv.me, op.String())
	kv.mu.Unlock()

	index, _, isLeader := kv.rf.Start(op)

	kv.mu.Lock()
	if !isLeader {
		reply.Err = ErrWrongLeader
		reply.LeaderHint = -1
		kv.mu.Unlock()
		return
	}
	waitCh := kv.getWaitCh(index)
	kv.mu.Unlock()
	// wait for appliedOp from applyHandler
	timer := time.NewTicker(1e8)

	defer func() {
		timer.Stop()
		kv.mu.Lock()
		close(waitCh)
		delete(kv.waitChan, index)
		kv.mu.Unlock()
	}()

	select {
	case <-timer.C:
		reply.Err = ErrWrongLeader
		reply.LeaderHint = -1
		return

	case applyOp, ok := <-waitCh:
		if !ok {
			reply.Err = ErrWrongLeader
			reply.LeaderHint = -1
			return
		}
		PrettyDebug(dServer, "Server%d receive GET command from raft, COMMAND %s", kv.me, op.String())
		if applyOp.SeqNum == args.SeqNum && applyOp.ClientID == args.ClientID {
			reply.Value = applyOp.Value
			reply.Err = OK
			reply.LeaderHint = kv.me
			return
		} else {
			reply.Err = ErrWrongLeader
			reply.LeaderHint = -1
			return
		}
	}
}

func (kv *KVServer) PutAppend(args *PutAppendArgs, reply *PutAppendReply) {
	// Your code here.
	isLeader, _, leaderHint := kv.rf.RaftState()

	// Reply NOT_Leader if not leader, providing hint when available
	if !isLeader {
		reply.Err = ErrWrongLeader
		reply.LeaderHint = leaderHint
		return
	}

	// Append command to log, replicate and commit it
	kv.mu.Lock()
	op := Op{args.ClientID, args.SeqNum, args.Key, args.Value, args.Op}
	kv.mu.Unlock()

	index, _, isLeader := kv.rf.Start(op)

	kv.mu.Lock()
	if !isLeader {
		reply.Err = ErrWrongLeader
		reply.LeaderHint = leaderHint
		kv.mu.Unlock()
		return
	}
	waitCh := kv.getWaitCh(index)
	PrettyDebug(dServer, "Server%d insert PUTAPPEND command to raft, COMMAND %s", kv.me, op.String())
	kv.mu.Unlock()

	// If sequenceNum already processed from client, reply OK with stored response
	// Apply command in log order
	// save state machine output with SeqNum for client, discard any prior response for client (smaller than SeqNum)

	// wait for appliedOp from applyHandler

	timer := time.NewTicker(1e8)

	defer func() {
		timer.Stop()
		kv.mu.Lock()
		close(waitCh)
		delete(kv.waitChan, index)
		kv.mu.Unlock()
	}()

	select {
	case <-timer.C:
		reply.Err = ErrWrongLeader
		reply.LeaderHint = -1
		return
	case applyOp, ok := <-waitCh:
		if !ok {
			reply.Err = ErrWrongLeader
			reply.LeaderHint = -1
			return
		}
		PrettyDebug(dServer, "Server%d receive PutAppend command from raft, COMMAND %s", kv.me, op.String())
		if applyOp.SeqNum == args.SeqNum && applyOp.ClientID == args.ClientID {
			reply.Err = OK
			reply.LeaderHint = kv.me
			return
		} else {
			reply.Err = ErrWrongLeader
			reply.LeaderHint = -1
			return
		}
	}

}

//
// the tester calls Kill() when a KVServer instance won't
// be needed again. for your convenience, we supply
// code to set rf.dead (without needing a lock),
// and a killed() method to test rf.dead in
// long-running loops. you can also add your own
// code to Kill(). you're not required to do anything
// about this, but it may be convenient (for example)
// to suppress debug output from a Kill()ed instance.
//
func (kv *KVServer) Kill() {
	atomic.StoreInt32(&kv.dead, 1)
	kv.rf.Kill()
	// Your code here, if desired.
}

func (kv *KVServer) killed() bool {
	z := atomic.LoadInt32(&kv.dead)
	return z == 1
}

//
// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant key/value service.
// me is the index of the current server in servers[].
// the k/v server should store snapshots through the underlying Raft
// implementation, which should call persister.SaveStateAndSnapshot() to
// atomically save the Raft state along with the snapshot.
// the k/v server should snapshot when Raft's saved state exceeds maxraftstate bytes,
// in order to allow Raft to garbage-collect its log. if maxraftstate is -1,
// you don't need to snapshot.
// StartKVServer() must return quickly, so it should start goroutines
// for any long-running work.
//
func StartKVServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxraftstate int) *KVServer {
	// call labgob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.
	labgob.Register(Op{})

	kv := new(KVServer)
	kv.me = me
	kv.maxraftstate = maxraftstate

	// You may need initialization code here.

	kv.applyCh = make(chan raft.ApplyMsg)
	kv.rf = raft.Make(servers, me, persister, kv.applyCh)

	// You may need initialization code here.
	kv.currentState = make(map[string]string, 0)
	kv.waitChan = make(map[int]chan Op, 0)
	kv.clientSeq = make(map[int64]int, 0)
	go kv.applyHandler()

	return kv
}
