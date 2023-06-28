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

const (
	KVOperation           = 1
	ConfigOperation       = 2
	ShardRequestOperation = 3
	ShardConfirmOperation = 4
)

type CommandMsg struct {
	CommandType int
	Data        interface{}
}

type ShardRequestMsg struct {
	ConfigNum  int
	ShardIndex int
	GID        int
	ShardData  map[string]string
	// SeqNum
	ClientSeqNum map[int64]int
}

func (shardRequestMsg *ShardRequestMsg) String() string {
	return fmt.Sprintf("ShardRequestMsg: ConfigNum: %d, ShardIndex: %d, GID: %d, ShardData: %v", shardRequestMsg.ConfigNum, shardRequestMsg.ShardIndex, shardRequestMsg.GID, shardRequestMsg.ShardData)
}

type ShardConfirmMsg struct {
	ConfigNum  int
	ShardIndex int
	ShardState int
}

func (shardConfirmMsg *ShardConfirmMsg) String() string {
	return fmt.Sprintf("ShardConfirmMsg: ConfigNum: %d, ShardIndex: %d, ShardState: %d", shardConfirmMsg.ConfigNum, shardConfirmMsg.ShardIndex, shardConfirmMsg.ShardState)
}

func (commandMsg *CommandMsg) String() string {
	switch commandMsg.CommandType {
	case KVOperation:
		return fmt.Sprintf("CommandMsg: CommandType: %s, Data: %v", "KVOperation", commandMsg.Data)
	case ConfigOperation:
		return fmt.Sprintf("CommandMsg: CommandType: %s, Data: %v", "ConfigOperation", commandMsg.Data)
	case ShardRequestOperation:
		return fmt.Sprintf("CommandMsg: CommandType: %s, Data: %v", "ShardRequestOperation", commandMsg.Data)
	case ShardConfirmOperation:
		return fmt.Sprintf("CommandMsg: CommandType: %s, Data: %v", "ShardConfirmOperation", commandMsg.Data)
	}

	return fmt.Sprintf("CommandMsg: CommandType: %s, Data: %v", "Unknown", commandMsg.Data)
}

// RPC between Servers.
type ShardRequestArgs struct {
	ConfigNum  int
	ShardIndex int
	GID        int
}

func (args *ShardRequestArgs) String() string {
	return fmt.Sprintf("ShardRequestArgs: ConfigNum: %d, ShardIndex: %d, GID: %d", args.ConfigNum, args.ShardIndex, args.GID)
}

type ShardRequestReply struct {
	Err        Err
	ConfigNum  int
	ShardIndex int
	ShardData  map[string]string
	// SeqNum
	ClientSeqNum map[int64]int
}

func (reply *ShardRequestReply) String() string {
	return fmt.Sprintf("ShardRequestReply: Err: %s, ConfigNum: %d, ShardIndex: %d, ShardData: %v", reply.Err, reply.ConfigNum, reply.ShardIndex, reply.ShardData)
}

type ShardConfirmArgs struct {
	ConfigNum  int
	ShardIndex int
}

func (args *ShardConfirmArgs) String() string {
	return fmt.Sprintf("ShardConfirmArgs: ConfigNum: %d, ShardIndex: %d", args.ConfigNum, args.ShardIndex)
}

type ShardConfirmReply struct {
	Err        Err
	ConfigNum  int
	ShardIndex int
	ShardState int
}

func (reply *ShardConfirmReply) String() string {
	return fmt.Sprintf("ShardConfirmReply: Err: %s, ConfigNum: %d, ShardState: %d", reply.Err, reply.ConfigNum, reply.ShardState)
}

func (kv *ShardKV) ShardsRequester() {
	for !kv.killed() {
		_, isLeader := kv.rf.GetState()
		if !isLeader {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		kv.mu.Lock()
		pullingShards := make([]int, 0)
		for index := 0; index < shardctrler.NShards; index++ {
			if kv.shardsState[index] == PULLING {
				pullingShards = append(pullingShards, index)
			}
		}
		PrettyDebug(dInfo, "KVServer %d-%d ShardsRequester: pullingShards: %v, Shards:%v", kv.gid, kv.me, pullingShards, kv.config.Shards)

		// BUG: need to use prevConfig to locate the shards.
		serversCopy := shardctrler.GetGroupMapCopy(kv.prevConfig.Groups)
		shardsCopy := shardctrler.GetShardsCopy(kv.prevConfig.Shards)
		kv.mu.Unlock()

		for _, i := range pullingShards {
			targetGID := shardsCopy[i]
			servers, ok := serversCopy[targetGID]
			shardIndex := i
			args := ShardRequestArgs{kv.config.Num, shardIndex, kv.gid}
			PrettyDebug(dInfo, "KVServer %d-%d send to GID: %d ShardRequestArgs: %s", kv.gid, kv.me, targetGID, args.String())
			if !ok {
				continue
			}
			go func(shardIndex int) {
				for si := 0; si < len(servers); si++ {
					srv := kv.make_end(servers[si])
					var reply ShardRequestReply
					ok := srv.Call("ShardKV.ShardsRequestHandler", &args, &reply)
					kv.mu.Lock()
					if ok && reply.Err == OK && reply.ConfigNum == kv.config.Num {
						if reply.ShardIndex != shardIndex {
							panic("reply.ShardIndex != shardIndex")
						}
						shardRequestMsg := ShardRequestMsg{args.ConfigNum, args.ShardIndex, args.GID, reply.ShardData, reply.ClientSeqNum}
						msg := CommandMsg{ShardRequestOperation, shardRequestMsg}

						kv.mu.Unlock()
						_, _, _ = kv.rf.Start(msg)
						return
					}
					kv.mu.Unlock()
				}
			}(i)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (kv *ShardKV) ShardsRequestHandler(args *ShardRequestArgs, reply *ShardRequestReply) {
	_, isLeader := kv.rf.GetState()
	if !isLeader {
		reply.Err = ErrWrongLeader
	}
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if args.ConfigNum != kv.config.Num {
		PrettyDebug(dError, "KVServer %d-%d ShardsRequestHandler receive WRONG ShardRequestArgs %s", kv.gid, kv.me, args.String())
		reply.Err = ErrConfig
		return
	}
	// copy the shard data
	if kv.shardsState[args.ShardIndex] == BEPULLED {
		reply.Err = OK
		reply.ConfigNum = args.ConfigNum
		reply.ShardIndex = args.ShardIndex

		reply.ShardData = make(map[string]string, 0)
		for key, value := range kv.currentState {
			if key2shard(key) == args.ShardIndex {
				reply.ShardData[key] = value
			}
		}
		reply.ClientSeqNum = make(map[int64]int, 0)
		for key, value := range kv.clientSeq {
			reply.ClientSeqNum[key] = value
		}
		PrettyDebug(dServer, "KVServer %d-%d ShardsRequestHandler reply ShardRequestArgs %s", kv.gid, kv.me, reply.String())
		return
	}
	reply.Err = ErrWrongGroup
	return
}

func (kv *ShardKV) ShardsConfirmHandler(args *ShardConfirmArgs, reply *ShardConfirmReply) {
	_, isLeader := kv.rf.GetState()
	if !isLeader {
		reply.Err = ErrWrongLeader
		return
	}

	kv.mu.Lock()
	defer kv.mu.Unlock()

	if kv.config.Num < args.ConfigNum {
		reply.Err = ErrConfig
		PrettyDebug(dError, "KVServer %d-%d ShardsConfirmHandler receive WRONG ShardConfirmArgs %s", kv.gid, kv.me, args.String())
		return
	}

	if (kv.config.Num == args.ConfigNum && kv.shardsState[args.ShardIndex] == SERVING) || kv.config.Num > args.ConfigNum {
		reply.Err = OK
		reply.ConfigNum = kv.config.Num
		reply.ShardIndex = args.ShardIndex
		reply.ShardState = SERVING
		PrettyDebug(dServer, "KVServer %d-%d ShardsConfirmHandler reply ShardConfirmReply %s", kv.gid, kv.me, reply.String())
		return
	}
	reply.Err = ErrWrongLeader
}

// Locked
func (kv *ShardKV) mergeShardData(shardIndex int, shardData map[string]string) {
	for key, value := range shardData {
		kv.currentState[key] = value
	}
	kv.shardsState[shardIndex] = SERVING
	PrettyDebug(dServer, "KVServer %d-%d mergeShardIndex %d, kv.currentState:%v", kv.gid, kv.me, shardIndex, kv.shardsState)
}

func (kv *ShardKV) mergeClientSeqNum(clientSeqNum map[int64]int) {
	for key, value := range clientSeqNum {
		if kv.clientSeq[key] < value {
			kv.clientSeq[key] = value
		}
	}
}
func (kv *ShardKV) ShardsConfirm() {
	for !kv.killed() {
		_, isLeader := kv.rf.GetState()
		if !isLeader {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		kv.mu.Lock()
		bePulledShards := make([]int, 0)
		for index := 0; index < shardctrler.NShards; index++ {
			if kv.shardsState[index] == BEPULLED {
				bePulledShards = append(bePulledShards, index)
			}
		}
		serversCopy := shardctrler.GetGroupMapCopy(kv.config.Groups)
		shardsCopy := shardctrler.GetShardsCopy(kv.config.Shards)
		kv.mu.Unlock()

		PrettyDebug(dServer, "KVServer %d-%d ShardsConfirm bePulledShards:%v", kv.gid, kv.me, bePulledShards)
		for _, i := range bePulledShards {
			targetGID := shardsCopy[i]
			servers, ok := serversCopy[targetGID]
			shardIndex := i
			args := ShardConfirmArgs{kv.config.Num, shardIndex}
			if !ok {
				continue
			}
			go func(shardIndex int) {
				for si := 0; si < len(servers); si++ {
					srv := kv.make_end(servers[si])
					var reply ShardConfirmReply
					PrettyDebug(dInfo, "KVServer %d-%d send to GID: %d ShardConfirm: %s", kv.gid, kv.me, targetGID, args.String())
					ok := srv.Call("ShardKV.ShardsConfirmHandler", &args, &reply)
					kv.mu.Lock()
					if ok && reply.Err == OK && reply.ConfigNum >= kv.config.Num {
						shardConfirmMsg := ShardConfirmMsg{ConfigNum: args.ConfigNum, ShardIndex: args.ShardIndex, ShardState: reply.ShardState}
						msg := CommandMsg{ShardConfirmOperation, shardConfirmMsg}
						kv.mu.Unlock()
						_, _, _ = kv.rf.Start(msg)
						return
					}
					kv.mu.Unlock()
				}
			}(shardIndex)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

/*
// A configuration -- an assignment of shards to groups.
// Please don't change this.
type Config struct {
	Num    int              // config number
	Shards [NShards]int     // shard -> gid
	Groups map[int][]string // gid -> servers[]
}
*/

type ConfigMsg struct {
	Num    int                      // config number
	Shards [shardctrler.NShards]int // shard -> gid
	Groups map[int][]string         // gid -> servers[]
}

func (config *ConfigMsg) String() string {
	return fmt.Sprintf("ConfigMsg: Num: %v, Shards: %v, Groups: %v", config.Num, config.Shards, config.Groups)
}

func (op *Op) String() string {
	switch op.Optype {
	case "Get":
		return fmt.Sprintf("Optype: %s, ClientID:%d, SeqNum %d, Key: %s", op.Optype, op.ClientID, op.SeqNum, op.Key)
	case "Put":
		return fmt.Sprintf("Optype: %s, ClientID:%d, SeqNum %d, Key: %v, Value: %v", op.Optype, op.ClientID, op.SeqNum, op.Key, op.Value)
	case "Append":
		return fmt.Sprintf("Optype: %s, ClientID:%d, SeqNum %d, Key: %v, Value: %v", op.Optype, op.ClientID, op.SeqNum, op.Key, op.Value)
	}
	return fmt.Sprintf("Optype: %s, ClientID:%d, SeqNum %d, Key: %v, Value: %v", op.Optype, op.ClientID, op.SeqNum, op.Key, op.Value)
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
	waitChan          map[int]chan CommandMsg
	clientSeq         map[int64]int
	lastIncludedIndex int // for snapshot

	// Lab 4B
	mck         *shardctrler.Clerk
	config      shardctrler.Config
	prevConfig  shardctrler.Config
	shardsState [shardctrler.NShards]int
}

const (
	SERVING    = 1
	NO_SERVING = 2
	PULLING    = 3
	BEPULLED   = 4
	GCING      = 5
)

// 1.Check if there's new config. If not, sleep for 100ms
// 2.If there's new config,
// 3.Put the config into raft, Wait for the new config to be applied (Commit by raft)
// 4.If the new config is applied, set the shard state. If the new config is not applied, go to step 3
// 5.Set the shards state and wait

func (kv *ShardKV) getLastConfig() {
	for !kv.killed() {
		_, isLeader := kv.rf.GetState()
		if !isLeader {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		newConfig := kv.mck.Query(-1)
		kv.mu.Lock()
		allShardsReady := kv.shardsStateReady()
		needToUpdate := kv.config.Num < newConfig.Num
		target := kv.config.Num + 1
		if kv.config.Num == 0 {
			allShardsReady = true
		}
		PrettyDebug(dServer, "KVServer %d-%d needToUpdate: %v, kv.config.Num: %d, newConfig.Num: %d\n", kv.gid, kv.me, needToUpdate, kv.config.Num, newConfig.Num)
		kv.mu.Unlock()
		_, isLeader = kv.rf.GetState()
		if !needToUpdate || !allShardsReady || !isLeader {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		// Need to update, put the config into raft
		newConfig = kv.mck.Query(target)
		// The new config maybe applied (Commit by raft)
		configCommmand := ConfigMsg{Num: newConfig.Num, Shards: newConfig.Shards, Groups: newConfig.Groups}
		msg := CommandMsg{ConfigOperation, configCommmand}
		_, _, isLeader = kv.rf.Start(msg)
		PrettyDebug(dServer, "KVServer %d-%d insert new config to raft %s\n", kv.gid, kv.me, configCommmand.String())
	}
}

func (kv *ShardKV) shardsStateReady() bool {
	for i := 0; i < len(kv.shardsState); i++ {
		if kv.shardsState[i] != NO_SERVING && kv.shardsState[i] != SERVING {
			PrettyDebug(dServer, "KVServer %d-%d shard %d is not ready, ShardsState:%v", kv.gid, kv.me, i, kv.shardsState)
			return false
		}
	}
	return true
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
	// Lab 4
	e.Encode(kv.shardsState)
	e.Encode(kv.config)
	e.Encode(kv.prevConfig)
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
	var shardsState [shardctrler.NShards]int
	var config shardctrler.Config
	var prevConfig shardctrler.Config
	if d.Decode(&currentState) != nil || d.Decode(&clientSeq) != nil || d.Decode(&shardsState) != nil ||
		d.Decode(&config) != nil || d.Decode(&prevConfig) != nil {
		return
	} else {
		kv.currentState = currentState
		kv.clientSeq = clientSeq
		kv.shardsState = shardsState
		kv.config = config
		kv.prevConfig = prevConfig
	}
}

func (kv *ShardKV) String() {
	fmt.Printf("KVServer{GID: %d, me:%d, currentState:%v, waitChan:%v, clientSeq:%v}\n", kv.gid, kv.me, kv.currentState, kv.waitChan, kv.clientSeq)
}

func (kv *ShardKV) applyHandler() {
	for !kv.killed() {
		var msg CommandMsg
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
				PrettyDebug(dPersist, "KVServer %d-%d receive snapshot with applyMsg.SnapshotIndex %d, kv.currentState:%v, kv.clientSeq:%v",
					kv.gid, kv.me, applyMsg.SnapshotIndex, kv.currentState, kv.clientSeq)
				kv.mu.Unlock()
			} else {
				commandMsg := applyMsg.Command.(CommandMsg)
				kv.mu.Lock()
				switch commandMsg.CommandType {
				case KVOperation:
					op, _ := commandMsg.Data.(Op)
					PrettyDebug(dServer, "KVServer %d-%d APPLY HANDLER get Op command %s", kv.gid, kv.me, op.String())
					appliedOp := Op{op.ClientID, op.SeqNum, op.Key, op.Value, op.Optype}
					// Check if the applyMsg have been applied before
					if applyMsg.CommandIndex <= kv.lastIncludedIndex {
						kv.mu.Unlock()
						continue
					}
					// Check if the shard is serving
					shardIndex := key2shard(op.Key)
					if kv.shardsState[shardIndex] != SERVING {
						kv.mu.Unlock()
						continue
					}
					// Check if the client session is existing, if not, init one with 0
					_, clientIsPresent := kv.clientSeq[appliedOp.ClientID]
					if !clientIsPresent {
						kv.clientSeq[appliedOp.ClientID] = 0
					}
					// Check duplicated command, If sequenceNum already processed from client, reply OK with stored response
					if appliedOp.Optype == "Get" {
						appliedOp.Value = kv.currentState[appliedOp.Key]
					}
					if kv.clientSeq[appliedOp.ClientID] >= appliedOp.SeqNum {
						PrettyDebug(dServer, "KVServer %d-%d sequenceNum already processed from client %d, appliedOp: %s, kv.clientSeq: %v",
							kv.gid, kv.me, appliedOp.ClientID, appliedOp.String(), kv.clientSeq)
					} else {
						// If sequenceNum not processed, store response and reply OK
						kv.applyToStateMachine(&appliedOp)
						PrettyDebug(dServer, "KVServer %d-%d apply command to STATEMACHINE %s, kv.currentState:%v",
							kv.gid, kv.me, appliedOp.String(), kv.currentState)
					}
					msg = CommandMsg{CommandType: KVOperation, Data: appliedOp}

				case ConfigOperation:
					applyConfig := commandMsg.Data.(ConfigMsg)
					PrettyDebug(dServer, "KVServer %d-%d APPLY HANDLER get Config %s", kv.gid, kv.me, applyConfig.String())
					// Check if the applyMsg have been applied before
					if applyMsg.CommandIndex <= kv.lastIncludedIndex {
						kv.mu.Unlock()
						continue
					}
					if kv.config.Num == 0 || applyConfig.Num == kv.config.Num+1 {
						PrettyDebug(dServer, "KVServer %d-%d UPDATECONFIG handler get Config %s, kv.config:%v, kv.prevConfig:%v", kv.gid, kv.me, applyConfig.String(), kv.config, kv.prevConfig)
						kv.prevConfig = kv.config
						kv.config = shardctrler.Config{applyConfig.Num, applyConfig.Shards, applyConfig.Groups}
						kv.updateShardsState()
						PrettyDebug(dServer, "KVServer %d-%d updateShardsState %v", kv.gid, kv.me, kv.shardsState)
						// update the state of shards.
					}
					msg = applyMsg.Command.(CommandMsg)

				case ShardRequestOperation:
					shardRequestMsg := commandMsg.Data.(ShardRequestMsg)
					PrettyDebug(dServer, "KVServer %d-%d APPLY HANDLER get ShardRequestMsg %s", kv.gid, kv.me, shardRequestMsg.String())
					if shardRequestMsg.ConfigNum != kv.config.Num {
						PrettyDebug(dServer, "KVServer %d-%d WRONG CONFIG ShardRequestMsg %s", kv.gid, kv.me, shardRequestMsg.String())
						kv.mu.Unlock()
						continue
					}

					if kv.shardsState[shardRequestMsg.ShardIndex] == PULLING {
						kv.mergeShardData(shardRequestMsg.ShardIndex, shardRequestMsg.ShardData)
						kv.mergeClientSeqNum(shardRequestMsg.ClientSeqNum)
						// kv.shardsState[shardRequestMsg.Shard] = SERVING
						PrettyDebug(dServer, "KVServer %d-%d APPLY HANDLER get ShardRequestMsg %s", kv.gid, kv.me, shardRequestMsg.String())
					} else {
						PrettyDebug(dServer, "KVServer %d-%d SERVING STATE ShardRequestMsg %s", kv.gid, kv.me, shardRequestMsg.String())
					}
					msg = CommandMsg{CommandType: ShardRequestOperation, Data: shardRequestMsg}

				case ShardConfirmOperation:
					shardConfirmMsg := commandMsg.Data.(ShardConfirmMsg)
					PrettyDebug(dServer, "KVServer %d-%d APPLY HANDLER get ShardConfirmMsg %s", kv.gid, kv.me, shardConfirmMsg.String())
					if shardConfirmMsg.ConfigNum != kv.config.Num {
						PrettyDebug(dServer, "KVServer %d-%d WRONG CONFIG ShardConfirmMsg %s", kv.gid, kv.me, shardConfirmMsg.String())
						kv.mu.Unlock()
						continue
					}

					if kv.shardsState[shardConfirmMsg.ShardIndex] == BEPULLED {
						kv.shardsState[shardConfirmMsg.ShardIndex] = NO_SERVING
						//
						for key, _ := range kv.currentState {
							if key2shard(key) == shardConfirmMsg.ShardIndex {
								delete(kv.currentState, key)
							}
						}
						//
						PrettyDebug(dServer, "KVServer %d-%d APPLY HANDLER get ShardConfirmMsg %s", kv.gid, kv.me, shardConfirmMsg.String())
					}
					msg = CommandMsg{CommandType: ShardConfirmOperation, Data: shardConfirmMsg}
					PrettyDebug(dServer, "KVServer %d-%d REJECT ShardConfirmMsg %s", kv.gid, kv.me, shardConfirmMsg.String())

				default:
					panic("Unknown command type")
				}

				// take snapshot and send msg
				if kv.maxraftstate != -1 && kv.rf.GetStateSize() > kv.maxraftstate {
					snapshotData := kv.getServerPersistData()
					kv.rf.Snapshot(applyMsg.CommandIndex, snapshotData)
					PrettyDebug(dSnap, "Server %d store snapshot with commandIndex %d, kv.currentState:%v, kv.clientSeq:%v", kv.me, applyMsg.CommandIndex, kv.currentState, kv.clientSeq)
				}

				// If the channel is existing, and the leader is still alive, send the appliedOp to the channel
				commandIndex := applyMsg.CommandIndex
				waitChan, chanExisting := kv.waitChan[commandIndex]
				kv.mu.Unlock()
				if chanExisting {
					select {
					case waitChan <- msg:
					case <-time.After(1 * time.Second):
						fmt.Println("Leader chan timeout")
					}
				} else {
					PrettyDebug(dError, "KVServer %d-%d want to send msg %s , but no channel for commandIndex %d", kv.gid, kv.me, msg.String(), commandIndex)
				}

			}
		}
	}
}

func (kv *ShardKV) updateShardsState() {
	if kv.prevConfig.Num == 0 {
		for i := 0; i < shardctrler.NShards; i++ {
			if kv.config.Shards[i] == kv.gid {
				kv.shardsState[i] = SERVING
			}
		}
		return
	}
	for i := 0; i < shardctrler.NShards; i++ {
		if kv.prevConfig.Shards[i] == kv.gid && kv.config.Shards[i] == kv.gid {
			kv.shardsState[i] = SERVING
		} else if kv.prevConfig.Shards[i] != kv.gid && kv.config.Shards[i] == kv.gid {
			kv.shardsState[i] = PULLING
		} else if kv.prevConfig.Shards[i] == kv.gid && kv.config.Shards[i] != kv.gid {
			kv.shardsState[i] = BEPULLED
		} else {
			kv.shardsState[i] = NO_SERVING
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
func (kv *ShardKV) getWaitCh(index int) (waitCh chan CommandMsg) {
	waitCh, isPresent := kv.waitChan[index]
	if isPresent {
		return waitCh
	} else {
		kv.waitChan[index] = make(chan CommandMsg, 1)
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
	commandMsg := CommandMsg{KVOperation, op}
	PrettyDebug(dServer, "Server%d insert GET command to raft Log, COMMAND %s", kv.me, op.String())
	index, _, isLeader := kv.rf.Start(commandMsg)
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

	case commandMsg, ok := <-waitCh:
		defer func() {
			go func() {
				kv.mu.Lock()
				close(waitCh)
				delete(kv.waitChan, index)
				kv.mu.Unlock()
			}()
		}()
		if !ok || commandMsg.CommandType != KVOperation {
			reply.Err = ErrWrongLeader
			return
		}
		applyOp := commandMsg.Data.(Op)
		if applyOp.SeqNum == args.SeqNum && applyOp.ClientID == args.ClientID {
			reply.Value = applyOp.Value
			reply.Err = OK
			PrettyDebug(dServer, "Server%d receive GET command from raft, COMMAND %s", kv.me, op.String())
		} else {
			reply.Err = ErrWrongLeader
		}
	}
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
	commandMsg := CommandMsg{KVOperation, op}
	index, _, isLeader := kv.rf.Start(commandMsg)

	if !isLeader {
		reply.Err = ErrWrongLeader
		return
	}

	if kv.isWrongGroup(shardIndex) == true {
		reply.Err = ErrWrongGroup
		return
	}

	PrettyDebug(dServer, "KVServer %d-%d insert PUTAPPEND command to raft at Index %d, COMMAND %s", kv.gid, kv.me, index, op.String())

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
	case commandMsg := <-waitCh:
		PrettyDebug(dServer, "KVServer%d-%d receive PUTAPPEND command from raft, COMMAND %s", kv.gid, kv.me, commandMsg.String())

		defer func() {
			go func() {
				kv.mu.Lock()
				close(waitCh)
				delete(kv.waitChan, index)
				kv.mu.Unlock()
			}()
		}()
		if commandMsg.CommandType != KVOperation {
			reply.Err = ErrWrongLeader
			return
		}
		applyOp := commandMsg.Data.(Op)
		if applyOp.SeqNum == args.SeqNum && applyOp.ClientID == args.ClientID {
			reply.Err = OK
			PrettyDebug(dServer, "KVServer%d-%d reply PUTAPPEND command to client, COMMAND %s", kv.me, op.String())
		} else {
			reply.Err = ErrWrongLeader
		}
	}
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
	labgob.Register(CommandMsg{})
	labgob.Register(ConfigMsg{})
	labgob.Register(ShardRequestMsg{})
	labgob.Register(ShardConfirmMsg{})

	kv := new(ShardKV)
	kv.me = me
	kv.maxraftstate = maxraftstate

	kv.make_end = make_end
	kv.gid = gid
	kv.ctrlers = ctrlers

	// Your initialization code here.
	kv.currentState = make(map[string]string, 0)
	kv.waitChan = make(map[int]chan CommandMsg, 0)
	kv.clientSeq = make(map[int64]int, 0)

	// Use something like this to talk to the shardctrler:
	kv.mck = shardctrler.MakeClerk(kv.ctrlers)
	kv.applyCh = make(chan raft.ApplyMsg)
	kv.rf = raft.Make(servers, me, persister, kv.applyCh)

	kv.config = shardctrler.Config{}
	kv.prevConfig = shardctrler.Config{}
	kv.shardsState = [shardctrler.NShards]int{}
	for i := 0; i < shardctrler.NShards; i++ {
		kv.shardsState[i] = NO_SERVING
	}

	// Your code here.
	snapshot := kv.rf.GetSnapshot()
	if len(snapshot) > 0 {
		kv.mu.Lock()
		kv.readServerPersistData(snapshot)
		PrettyDebug(dServer, "KVServer%d-%d read snapshot from raft, shardsState %v, config.Num: %d, prevConfig.Num: %d, currentState %v, clientSeq %v",
			kv.gid, kv.me, kv.shardsState, kv.config.Num, kv.prevConfig.Num, kv.currentState, kv.clientSeq)
		kv.mu.Unlock()
	}
	go kv.applyHandler()
	go kv.getLastConfig()
	go kv.ShardsRequester()
	go kv.ShardsConfirm()
	return kv
}
