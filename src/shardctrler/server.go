package shardctrler

import (
	"bytes"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

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
	ArgsType int              // JoinArgs, LeaveArgs, MoveArgs, QueryArgs
	Servers  map[int][]string // JoinArgs		// new GID -> servers mappings
	GIDs     []int            // LeaveArgs
	Shard    int              // MoveArgs
	GID      int              // MoveArgs
	Num      int              // QueryArgs 		// desired config number
	SeqNum   int              // for deduplication
	ClientID int64            // for deduplication
}

func (op *Op) String() string {
	switch op.ArgsType {
	case JoinArgsType:
		return fmt.Sprintf("ClientID:%d, SeqNum %d, JoinArgs: %v", op.ClientID, op.SeqNum, op.Servers)
	case LeaveArgsType:
		return fmt.Sprintf("ClientID:%d, SeqNum %d, LeaveArgs: %v", op.ClientID, op.SeqNum, op.GIDs)
	case MoveArgsType:
		return fmt.Sprintf("ClientID:%d, SeqNum %d, MoveArgs: %v %v", op.ClientID, op.SeqNum, op.Shard, op.GID)
	case QueryArgsType:
		return fmt.Sprintf("ClientID:%d, SeqNum %d, QueryArgs: %v", op.ClientID, op.SeqNum, op.Num)
	default:
		return fmt.Sprintf("ClientID:%d, SeqNum %d, UnknownArgsType: %v", op.ClientID, op.SeqNum, op.ArgsType)
	}
}

/*
ApplyMsg struct {
	CommandValid bool
	Command      interface{}
	CommandIndex int
}
*/
func (sc *ShardCtrler) applyHandler() {
	for !sc.killed() {
		select {
		case applyMsg := <-sc.applyCh:
			// Deal with the applyMsg
			if !applyMsg.CommandValid {
				continue
			}
			op, _ := applyMsg.Command.(Op)
			// PrettyDebug(dServer, "Server%d get valid command %s", sc.me, op.String())
			appliedOp := Op{op.ArgsType, op.Servers, op.GIDs, op.Shard, op.GID, op.Num, op.SeqNum, op.ClientID}

			// no check for lastIncludedIndex
			sc.mu.Lock()
			// check if the client session is existing, if not, init one with 0
			_, clientIsPresent := sc.clientSeq[appliedOp.ClientID]
			if !clientIsPresent {
				sc.clientSeq[appliedOp.ClientID] = 0
			}
			if appliedOp.ArgsType == QueryArgsType {
				// TODO: return the result at given index to the client
				// return sc.getQueryResult(appliedOp.Num)
			}
			if appliedOp.SeqNum <= sc.clientSeq[appliedOp.ClientID] {

			} else {
				sc.applyToStateMachine(&appliedOp)
			}

			// If the channel is existing, and the leader is still alive, send the appliedOp to the channel
			commandIndex := applyMsg.CommandIndex
			waitChan, chanExisting := sc.waitChan[commandIndex]
			if chanExisting {
				select {
				case waitChan <- appliedOp:
				case <-time.After(1 * time.Second):
					fmt.Println("Leader chan timeout")
				}
			}
			sc.mu.Unlock()
		}
	}
}

/*
Servers  map[int][]string // JoinArgs		// new GID -> servers mappings
GIDs     []int            // LeaveArgs
Shard    int              // MoveArgs
GID      int              // MoveArgs
Num      int              // QueryArgs 		// desired config number
*/
/*
	type Config struct {
		Num    int              // config number
		Shards [NShards]int     // shard -> gid
		Groups map[int][]string // gid -> servers[]
	}
*/
func (sc *ShardCtrler) applyToStateMachine(appliedOp *Op) {
	oldNum := sc.configs[len(sc.configs)-1].Num
	oldShards := sc.configs[len(sc.configs)-1].Shards
	oldGroups := sc.configs[len(sc.configs)-1].Groups
	switch appliedOp.ArgsType {
	case JoinArgsType:
		// Servers  map[int][]string // JoinArgs
		//PrettyDebug(dServer, "Before Server%d Apply Join, copyMap: %v", sc.me, oldGroups)
		copyMap := GetGroupMapCopy(oldGroups)
		copyNewServer := GetGroupMapCopy(appliedOp.Servers)
		for gid, values := range copyNewServer {
			_, isPresent := copyMap[gid]
			if isPresent {
				delete(copyMap, gid)
			}
			copyMap[gid] = values
		}
		// PrettyDebug(dCtrl, "SHARDCTRLER %d Apply Join, copyMap: %v", sc.me, copyMap)
		// Rebalance
		gidToShardsMap := getGIDtoShardsMap(oldShards, copyMap)
		newShards, _ := rebalance(gidToShardsMap, oldShards, copyMap)

		sc.configs = append(sc.configs, Config{Num: oldNum + 1, Shards: newShards, Groups: copyMap})

	case LeaveArgsType:
		// GIDs     []int            // LeaveArgs
		/*
			PrettyDebug(dCtrl, "Before Server%d Apply Leave, copyMap: %v, address: %p", sc.me, oldGroups, oldGroups)
			PrettyDebug(dCtrl, "appliedOp.GIDs: %v", appliedOp.GIDs)
		*/
		copyMap := GetGroupMapCopy(oldGroups)
		for _, gid := range appliedOp.GIDs {
			delete(copyMap, gid)
		}
		// PrettyDebug(dCtrl, "SHARDCTRL Apply Leave, copyMap: %v, address: %p", sc.me, copyMap, copyMap)
		// Rebalance
		gidToShardsMap := getGIDtoShardsMap(oldShards, copyMap)
		newShards, _ := rebalance(gidToShardsMap, oldShards, copyMap)

		sc.configs = append(sc.configs, Config{Num: oldNum + 1, Shards: newShards, Groups: copyMap})

	case MoveArgsType:
		// Shard    int              // MoveArgs
		// GID      int              // MoveArgs
		shardsCopy := [len(oldShards)]int{}
		for i, v := range oldShards {
			shardsCopy[i] = v
		}
		shardsCopy[appliedOp.Shard] = appliedOp.GID
		sc.configs = append(sc.configs, Config{Num: oldNum + 1, Shards: shardsCopy, Groups: oldGroups})

	case QueryArgsType:
		// Num      int              // QueryArgs
	default:
		PrettyDebug(dCtrl, "SHARDCTRL receive wrong Request: args.ArgsType error")
	}
}

/*
	Input:
		shards []int
		newMap map[int][]string
	Output:
		gitToShards map[int][]int // gid -> shards[]
*/
func getGIDtoShardsMap(shards [NShards]int, newMap map[int][]string) map[int][]int {
	gitToShards := make(map[int][]int)
	// Init the map with all the existing GID
	for gid := range newMap {
		gitToShards[gid] = make([]int, 0)
	}
	for shardIndex, gid := range shards { // shards: shardIndex -> GID
		if _, exist := newMap[gid]; exist { // the GID is alive
			// Add to gitToShards
			gitToShards[gid] = append(gitToShards[gid], shardIndex)
		}
	}
	// PrettyDebug(dCtrl, "SHARDCTRL GIDtoShardsMap: %v", gitToShards)
	return gitToShards
}

/*
	Input:
		gitToShards map[int][]int // gid -> shards[]
		oldShards 	[]int
		newMap		map[int][]string
	Output:
		newShards  		[]int
		moveShardsIndex []int
*/
func rebalance(gitToShards map[int][]int, oldShards [NShards]int, newMap map[int][]string) ([NShards]int, map[int]bool) {
	moveShards := make(map[int]bool, 0)
	newShards := [10]int{}
	liveGroup := make([]int, 0)
	// live Group determine the access order of the map
	for key, _ := range gitToShards {
		liveGroup = append(liveGroup, key)
	}
	sort.Ints(liveGroup)
	// PrettyDebug(dServer, "liveGroup: %v", liveGroup)
	// need to fill the newShards with the existing free shards
	// Get free Shards first
	freeShards := make([]int, 0)
	freeShardsIndex := 0
	for shardIndex, gid := range oldShards {
		if gid == 0 {
			freeShards = append(freeShards, shardIndex)
			continue
		}
		if _, exist := gitToShards[gid]; !exist {
			freeShards = append(freeShards, shardIndex)
		}
	}
	// PrettyDebug(dServer, "freeShards: %v", freeShards)
	// Rebalance
	for {
		maxShards := -1
		maxGID := -1
		minShards := NShards + 1
		minGID := -1
		for _, gid := range liveGroup {
			if len(gitToShards[gid]) > maxShards {
				maxShards = len(gitToShards[gid])
				maxGID = gid
			}
			if len(gitToShards[gid]) < minShards {
				minShards = len(gitToShards[gid])
				minGID = gid
			}
		}
		if maxShards-minShards <= 1 && freeShardsIndex >= len(freeShards) {
			break
		} else {
			// fill the newShards with the existing free shards
			var moveShardIndex int
			if freeShardsIndex < len(freeShards) {
				moveShardIndex = freeShards[freeShardsIndex]
				gitToShards[minGID] = append(gitToShards[minGID], moveShardIndex)
				moveShards[moveShardIndex] = true
				freeShardsIndex++
				continue
			}
			// move the shards from maxGID -> minGID
			length := len(gitToShards[maxGID])
			moveShardIndex = gitToShards[maxGID][length-1]
			gitToShards[maxGID] = gitToShards[maxGID][:length-1]
			gitToShards[minGID] = append(gitToShards[minGID], moveShardIndex)
			moveShards[moveShardIndex] = true
		}
	}
	// PrettyDebug(dCtrl, "After rebalanced, GIDtoShardsMap: %v", gitToShards)
	for _, gid := range liveGroup {
		for _, shardsIndex := range gitToShards[gid] {
			newShards[shardsIndex] = gid
		}
	}
	PrettyDebug(dCtrl, "Rebalanced newShards: %v, moveShards: %v", newShards, moveShards)
	return newShards, moveShards
}

// Groups map[int][]string // gid -> servers[]
func GetGroupMapCopy(originalMap map[int][]string) map[int][]string {
	copyMap := make(map[int][]string)
	for key, value := range originalMap {
		valueCopy := make([]string, len(value))
		copy(valueCopy, value)
		copyMap[key] = value
	}
	return copyMap
}

func GetShardsCopy(originalShards [NShards]int) [NShards]int {
	// shardsCopy := make([]int, len(originalShards))
	shardsCopy := [NShards]int{}
	for idx, item := range originalShards {
		shardsCopy[idx] = item
	}
	return shardsCopy
}

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

func (sc *ShardCtrler) RequestHandler(args *CommandArgs, reply *CommandReply) {
	// Your code here.
	/*
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
	*/
	_, isLeader := sc.rf.GetState()
	// Reply NOT_Leader if not leader, providing hint when available
	if !isLeader {
		reply.Err = ErrWrongLeader
		reply.WrongLeader = true
		return
	}
	// Append command to log, replicate and commit it
	op := Op{args.ArgsType, args.Servers, args.GIDs, args.Shard, args.GID, args.Num, args.SeqNum, args.ClientID}
	index, _, isLeader := sc.rf.Start(op)
	if !isLeader {
		reply.Err = ErrWrongLeader
		reply.WrongLeader = true
		return
	}
	// PrettyDebug(dServer, "Server%d insert command to raft, COMMAND %s", sc.me, op.String())

	sc.mu.Lock()
	waitCh := sc.getWaitCh(index)
	sc.mu.Unlock()

	// wait for appliedOp from applyHandler
	select {
	case <-time.After(7e8):
		reply.Err = ErrWrongLeader
		reply.WrongLeader = true
	case applyOp, ok := <-waitCh:
		if !ok {
			reply.Err = ErrWrongLeader
			reply.WrongLeader = true
			go func() {
				sc.mu.Lock()
				close(waitCh)
				delete(sc.waitChan, index)
				sc.mu.Unlock()
			}()
			return
		}
		if applyOp.SeqNum == args.SeqNum && applyOp.ClientID == args.ClientID {
			if applyOp.ArgsType == QueryArgsType {
				sc.mu.Lock()
				queryConfig := sc.getConfig(args.Num)
				retConfig := Config{Num: queryConfig.Num, Shards: GetShardsCopy(queryConfig.Shards), Groups: GetGroupMapCopy(queryConfig.Groups)}
				reply.Config = retConfig
				sc.mu.Unlock()
			}
			reply.Err = OK
		} else {
			reply.Err = ErrWrongLeader
			reply.WrongLeader = true
		}
	}
	go func() {
		sc.mu.Lock()
		close(waitCh)
		delete(sc.waitChan, index)
		sc.mu.Unlock()
	}()
}

func (sc *ShardCtrler) getConfig(index int) Config {
	configLength := len(sc.configs)
	lastConfig := sc.configs[configLength-1]
	if index == -1 || index >= configLength {
		return lastConfig
	} else {
		return sc.configs[index]
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
	sc.configs[0].Num = 0
	sc.configs[0].Shards = [NShards]int{}

	labgob.Register(Op{})
	sc.applyCh = make(chan raft.ApplyMsg)
	sc.rf = raft.Make(servers, me, persister, sc.applyCh)

	// Your code here.
	sc.waitChan = make(map[int]chan Op, 0)
	sc.clientSeq = make(map[int64]int, 0)

	go sc.applyHandler()

	return sc
}
