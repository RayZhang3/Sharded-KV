package shardkv

import "fmt"

//
// Sharded key/value server.
// Lots of replica groups, each running Raft.
// Shardctrler decides which group serves each shard.
// Shardctrler may change shard assignment from time to time.
//
// You will have to modify these definitions.
//

const (
	OK             = "OK"
	ErrNoKey       = "ErrNoKey"
	ErrWrongGroup  = "ErrWrongGroup"
	ErrWrongLeader = "ErrWrongLeader"
	ErrConfig      = "ErrConfig"
)

type Err string

// Put or Append
type PutAppendArgs struct {
	// You'll have to add definitions here.
	Key   string
	Value string
	Op    string // "Put" or "Append"
	// You'll have to add definitions here.
	// Field names must start with capital letters,
	// otherwise RPC will break.
	ClientID int64
	SeqNum   int
}

func (paa *PutAppendArgs) String() string {
	return fmt.Sprintf("PutAppendArgs{Key:%s, Value:%s, Op:%s, ClientID:%d, SeqNum:%d}", paa.Key, paa.Value, paa.Op, paa.ClientID, paa.SeqNum)
}

type PutAppendReply struct {
	Err Err
}

func (par *PutAppendReply) String() string {
	return fmt.Sprintf("PutAppendReply{Err:%s}", par.Err)
}

type GetArgs struct {
	Key string
	// You'll have to add definitions here.
	ClientID int64
	SeqNum   int
}

func (ga *GetArgs) String() string {
	return fmt.Sprintf("GetArgs{Key:%s, ClientID:%d, SeqNum:%d}", ga.Key, ga.ClientID, ga.SeqNum)
}

type GetReply struct {
	Err   Err
	Value string
}

func (gr *GetReply) String() string {
	return fmt.Sprintf("GetReply{Err:%s, Value:%s}", gr.Err, gr.Value)
}
