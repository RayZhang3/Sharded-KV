package shardkv

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"6.824/labgob"
	"6.824/labrpc"
	"6.824/raft"
	"6.824/shardctrler"
)

type Op struct {
	// Your definitions here.
	// Field names must start with capital letters,
	// otherwise RPC will break.
	// Lab 3B
	ClientID int64
	SeqNum   int
	Key      string
	Value    string
	Optype   string
}

type ShardKV struct {
	mu           sync.Mutex
	me           int
	rf           *raft.Raft
	applyCh      chan raft.ApplyMsg
	dead         int32 // set by Kill()
	maxraftstate int   // snapshot if log grows this big

	make_end func(string) *labrpc.ClientEnd
	gid      int
	ctrlers  []*labrpc.ClientEnd

	// Your definitions here.
	currentState      map[string]string
	waitChan          map[int]chan Op
	clientSeq         map[int64]int
	lastIncludedIndex int // for snapshot

	// Lab 4B
	mck        *shardctrler.Clerk
	config     shardctrler.Config
	prevConfig shardctrler.Config
}

func (kv *ShardKV) getLastConfig() {
	for !kv.killed() {
		newConfig := kv.mck.Query(-1)
		if newConfig.Num > kv.config.Num {
			kv.mu.Lock()
			kv.prevConfig = kv.config
			kv.config = newConfig
			PrettyDebug(dServer, "KVServer %d-%d get new config %v\n", kv.gid, kv.me, kv.config.String())
			kv.mu.Unlock()
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (kv *ShardKV) isWrongGroup(shardIndex int) bool {
	kv.mu.Lock()
	/*
		if kv.config.Num == 0 {
			panic("kv.config.Num == 0")
		}
	*/
	defer kv.mu.Unlock()
	return kv.config.Shards[shardIndex] != kv.gid
}

// Code from Lab3
func (kv *ShardKV) getServerPersistData() []byte {
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)
	e.Encode(kv.currentState)
	e.Encode(kv.clientSeq)
	data := w.Bytes()
	return data
}

func (kv *ShardKV) readServerPersistData(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}
	r := bytes.NewBuffer(data)
	d := labgob.NewDecoder(r)
	var currentState map[string]string
	var clientSeq map[int64]int
	if d.Decode(&currentState) != nil || d.Decode(&clientSeq) != nil {
		return
	} else {
		kv.currentState = currentState
		kv.clientSeq = clientSeq
	}
}

func (kv *ShardKV) String() {
	fmt.Printf("KVServer{GID: %d, me:%d, currentState:%v, waitChan:%v, clientSeq:%v}\n", kv.gid, kv.me, kv.currentState, kv.waitChan, kv.clientSeq)
}

func (kv *ShardKV) applyHandler() {
	for !kv.killed() {
		select {
		case applyMsg := <-kv.applyCh:
			if applyMsg.SnapshotValid == true {
				kv.mu.Lock()
				if !kv.rf.CondInstallSnapshot(applyMsg.SnapshotTerm, applyMsg.SnapshotIndex, applyMsg.Snapshot) {
					kv.mu.Unlock()
					continue
				}
				kv.readServerPersistData(applyMsg.Snapshot)
				kv.lastIncludedIndex = applyMsg.SnapshotIndex
				PrettyDebug(dPersist, "Server %d receive snapshot with applyMsg.SnapshotIndex %d, kv.currentState:%v, kv.clientSeq:%v", kv.me, applyMsg.SnapshotIndex, kv.currentState, kv.clientSeq)
				kv.mu.Unlock()
			} else {
				op, _ := applyMsg.Command.(Op)
				// PrettyDebug(dServer, "Server%d get valid command %s", kv.me, op.String())
				appliedOp := Op{op.ClientID, op.SeqNum, op.Key, op.Value, op.Optype}
				kv.mu.Lock()
				// Check if the applyMsg have been applied before
				if applyMsg.CommandIndex <= kv.lastIncludedIndex {
					kv.mu.Unlock()
					continue
				}
				// Check if the client session is existing, if not, init one with 0
				_, clientIsPresent := kv.clientSeq[appliedOp.ClientID]
				if !clientIsPresent {
					kv.clientSeq[appliedOp.ClientID] = 0
				}
				// Check duplicated command, If sequenceNum already processed from client, reply OK with stored response
				if kv.clientSeq[appliedOp.ClientID] >= appliedOp.SeqNum {
					if appliedOp.Optype == "Get" {
						appliedOp.Value = kv.currentState[appliedOp.Key]
					}
					// PrettyDebug(dServer, "Server%d sequenceNum already processed from client %d, appliedOp: %s, kv.clientSeq: %v",
					// kv.me, appliedOp.ClientID, appliedOp.String(), kv.clientSeq)
				} else {
					// If sequenceNum not processed, store response and reply OK
					if appliedOp.Optype == "Get" {
						appliedOp.Value = kv.currentState[appliedOp.Key]
					}
					kv.applyToStateMachine(&appliedOp)
					// PrettyDebug(dServer, "Server%d apply command %s, kv.currentState:%v", kv.me, appliedOp.String(), kv.currentState)
				}

				if kv.maxraftstate != -1 && kv.rf.GetStateSize() > kv.maxraftstate {
					snapshotData := kv.getServerPersistData()
					kv.rf.Snapshot(applyMsg.CommandIndex, snapshotData)
					// PrettyDebug(dPersist, "Server %d store snapshot with commandIndex %d, kv.currentState:%v, kv.clientSeq:%v", kv.me, applyMsg.CommandIndex, kv.currentState, kv.clientSeq)
				}

				// If the channel is existing, and the leader is still alive, send the appliedOp to the channel
				commandIndex := applyMsg.CommandIndex
				waitChan, chanExisting := kv.waitChan[commandIndex]
				if chanExisting {
					select {
					case waitChan <- appliedOp:
					case <-time.After(1 * time.Second):
						fmt.Println("Leader chan timeout")
					}
				}
				kv.mu.Unlock()
			}
		}
	}
}

func (kv *ShardKV) applyToStateMachine(appliedOp *Op) {
	// update the clientSeq
	kv.clientSeq[appliedOp.ClientID] = appliedOp.SeqNum
	// apply the command to state machine
	switch appliedOp.Optype {
	case "Put":
		kv.currentState[appliedOp.Key] = appliedOp.Value
	case "Append":
		currValue := kv.currentState[appliedOp.Key]
		kv.currentState[appliedOp.Key] = currValue + appliedOp.Value
	}
}

// get the wait channel for the index, if not exist, create one
// channel is used to send the appliedOp to the client
func (kv *ShardKV) getWaitCh(index int) (waitCh chan Op) {
	waitCh, isPresent := kv.waitChan[index]
	if isPresent {
		return waitCh
	} else {
		kv.waitChan[index] = make(chan Op, 1)
		return kv.waitChan[index]
	}
}

func (kv *ShardKV) Get(args *GetArgs, reply *GetReply) {
	// Your code here.
	_, isLeader := kv.rf.GetState()
	// Reply NOT_Leader if not leader, providing hint when available
	if !isLeader {
		reply.Err = ErrWrongLeader
		return
	}
	shardIndex := key2shard(args.Key)
	if kv.isWrongGroup(shardIndex) == true {
		reply.Err = ErrWrongGroup
		return
	}
	// Append command to log, replicate and commit it
	op := Op{args.ClientID, args.SeqNum, args.Key, "", "Get"}
	// PrettyDebug(dServer, "Server%d insert GET command to raft Log, COMMAND %s", kv.me, op.String())
	index, _, isLeader := kv.rf.Start(op)
	if !isLeader {
		reply.Err = ErrWrongLeader
		return
	}
	if kv.isWrongGroup(shardIndex) == true {
		reply.Err = ErrWrongGroup
		return
	}
	kv.mu.Lock()
	waitCh := kv.getWaitCh(index)
	kv.mu.Unlock()

	// wait for appliedOp from applyHandler
	select {
	case <-time.After(7e8):
		reply.Err = ErrWrongLeader

	case applyOp, ok := <-waitCh:
		if !ok {
			reply.Err = ErrWrongLeader
			go func() {
				kv.mu.Lock()
				close(waitCh)
				delete(kv.waitChan, index)
				kv.mu.Unlock()
			}()
			return
		}

		// PrettyDebug(dServer, "Server%d receive GET command from raft, COMMAND %s", kv.me, op.String())
		if applyOp.SeqNum == args.SeqNum && applyOp.ClientID == args.ClientID {
			reply.Value = applyOp.Value
			reply.Err = OK
		} else {
			reply.Err = ErrWrongLeader
		}
	}
	go func() {
		kv.mu.Lock()
		close(waitCh)
		delete(kv.waitChan, index)
		kv.mu.Unlock()
	}()
}

func (kv *ShardKV) PutAppend(args *PutAppendArgs, reply *PutAppendReply) {
	// Your code here.
	_, isLeader := kv.rf.GetState()

	// Reply NOT_Leader if not leader, providing hint when available
	if !isLeader {
		reply.Err = ErrWrongLeader
		return
	}

	shardIndex := key2shard(args.Key)
	if kv.isWrongGroup(shardIndex) == true {
		reply.Err = ErrWrongGroup
		return
	}

	// Append command to log, replicate and commit it
	op := Op{args.ClientID, args.SeqNum, args.Key, args.Value, args.Op}
	index, _, isLeader := kv.rf.Start(op)

	if !isLeader {
		reply.Err = ErrWrongLeader
		return
	}

	if kv.isWrongGroup(shardIndex) == true {
		reply.Err = ErrWrongGroup
		return
	}

	//PrettyDebug(dServer, "Server%d insert PUTAPPEND command to raft, COMMAND %s", kv.me, op.String())

	kv.mu.Lock()
	waitCh := kv.getWaitCh(index)
	kv.mu.Unlock()

	// If sequenceNum already processed from client, reply OK with stored response
	// Apply command in log order
	// save state machine output with SeqNum for client, discard any prior response for client (smaller than SeqNum)

	// wait for appliedOp from applyHandler

	select {
	case <-time.After(7e8):
		reply.Err = ErrWrongLeader
	case applyOp, ok := <-waitCh:
		if !ok {
			reply.Err = ErrWrongLeader
			go func() {
				kv.mu.Lock()
				close(waitCh)
				delete(kv.waitChan, index)
				kv.mu.Unlock()
			}()
			return
		}

		// PrettyDebug(dServer, "Server%d receive GET command from raft, COMMAND %s", kv.me, op.String())

		if applyOp.SeqNum == args.SeqNum && applyOp.ClientID == args.ClientID {
			reply.Err = OK
		} else {
			reply.Err = ErrWrongLeader
		}
	}
	go func() {
		kv.mu.Lock()
		close(waitCh)
		delete(kv.waitChan, index)
		kv.mu.Unlock()
	}()

}

//
// the tester calls Kill() when a ShardKV instance won't
// be needed again. you are not required to do anything
// in Kill(), but it might be convenient to (for example)
// turn off debug output from this instance.
//
func (kv *ShardKV) Kill() {
	kv.rf.Kill()
	// Your code here, if desired.
	atomic.StoreInt32(&kv.dead, 1)
}

func (kv *ShardKV) killed() bool {
	z := atomic.LoadInt32(&kv.dead)
	return z == 1
}

//
// servers[] contains the ports of the servers in this group.
//
// me is the index of the current server in servers[].
//
// the k/v server should store snapshots through the underlying Raft
// implementation, which should call persister.SaveStateAndSnapshot() to
// atomically save the Raft state along with the snapshot.
//
// the k/v server should snapshot when Raft's saved state exceeds
// maxraftstate bytes, in order to allow Raft to garbage-collect its
// log. if maxraftstate is -1, you don't need to snapshot.
//
// gid is this group's GID, for interacting with the shardctrler.
//
// pass ctrlers[] to shardctrler.MakeClerk() so you can send
// RPCs to the shardctrler.
//
// make_end(servername) turns a server name from a
// Config.Groups[gid][i] into a labrpc.ClientEnd on which you can
// send RPCs. You'll need this to send RPCs to other groups.
//
// look at client.go for examples of how to use ctrlers[]
// and make_end() to send RPCs to the group owning a specific shard.
//
// StartServer() must return quickly, so it should start goroutines
// for any long-running work.
//
func StartServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxraftstate int, gid int, ctrlers []*labrpc.ClientEnd, make_end func(string) *labrpc.ClientEnd) *ShardKV {
	// call labgob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.
	labgob.Register(Op{})

	kv := new(ShardKV)
	kv.me = me
	kv.maxraftstate = maxraftstate

	kv.make_end = make_end
	kv.gid = gid
	kv.ctrlers = ctrlers

	// Your initialization code here.
	kv.currentState = make(map[string]string, 0)
	kv.waitChan = make(map[int]chan Op, 0)
	kv.clientSeq = make(map[int64]int, 0)

	// Use something like this to talk to the shardctrler:
	kv.mck = shardctrler.MakeClerk(kv.ctrlers)
	kv.applyCh = make(chan raft.ApplyMsg)
	kv.rf = raft.Make(servers, me, persister, kv.applyCh)

	// Your code here.
	snapshot := kv.rf.GetSnapshot()
	if len(snapshot) > 0 {
		kv.mu.Lock()
		kv.readServerPersistData(snapshot)
		kv.mu.Unlock()
	}
	kv.config = shardctrler.Config{}
	kv.prevConfig = shardctrler.Config{}
	go kv.getLastConfig()
	go kv.applyHandler()
	return kv
}
