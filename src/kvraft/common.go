package kvraft

import "fmt"

const (
	OK             = "OK"
	ErrNoKey       = "ErrNoKey"
	ErrWrongLeader = "ErrWrongLeader"
)

type Err string

// Put or Append
type PutAppendArgs struct {
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
	Err        Err
	LeaderHint int //  address of new Leader, -1 if unkonwn
}

func (par *PutAppendReply) String() string {
	return fmt.Sprintf("PutAppendReply{Err:%s, LeaderHint:%d}", par.Err, par.LeaderHint)
}

type GetArgs struct {
	Key      string
	ClientID int64
	SeqNum   int
	// You'll have to add definitions here.
}

func (ga *GetArgs) String() string {
	return fmt.Sprintf("GetArgs{Key:%s, ClientID:%d, SeqNum:%d}", ga.Key, ga.ClientID, ga.SeqNum)
}

type GetReply struct {
	Err        Err
	Value      string
	LeaderHint int
}

func (gr *GetReply) String() string {
	return fmt.Sprintf("GetReply{Err:%s, Value:%s, LeaderHint:%d}", gr.Err, gr.Value, gr.LeaderHint)
}
