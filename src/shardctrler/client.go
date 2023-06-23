package shardctrler

//
// Shardctrler clerk.
//

import (
	"crypto/rand"
	"math/big"
	"time"

	"6.824/labrpc"
)

type Clerk struct {
	servers  []*labrpc.ClientEnd
	clientID int64
	seqNum   int
	leaderID int
	// Your data here.
}

func nrand() int64 {
	max := big.NewInt(int64(1) << 62)
	bigx, _ := rand.Int(rand.Reader, max)
	x := bigx.Int64()
	return x
}

func MakeClerk(servers []*labrpc.ClientEnd) *Clerk {
	ck := new(Clerk)
	ck.servers = servers
	// Your code here.
	ck.clientID = nrand()
	ck.seqNum = 1
	ck.leaderID = -1
	return ck
}

func (ck *Clerk) Query(num int) Config {
	commandArgs := &CommandArgs{ClientID: ck.clientID, SeqNum: ck.seqNum, ArgsType: QueryArgsType, Num: num}
	commandReply := ck.Request(commandArgs)
	return commandReply.Config
}

func (ck *Clerk) Join(servers map[int][]string) {
	commandArgs := &CommandArgs{ClientID: ck.clientID, SeqNum: ck.seqNum, ArgsType: JoinArgsType, Servers: servers}
	ck.Request(commandArgs)
}

func (ck *Clerk) Leave(gids []int) {
	commandArgs := &CommandArgs{ClientID: ck.clientID, SeqNum: ck.seqNum, ArgsType: LeaveArgsType, GIDs: gids}
	ck.Request(commandArgs)
}

func (ck *Clerk) Move(shard int, gid int) {
	commandArgs := &CommandArgs{ClientID: ck.clientID, SeqNum: ck.seqNum, ArgsType: MoveArgsType, Shard: shard, GID: gid}
	ck.Request(commandArgs)
}

func (ck *Clerk) getRandServer() int {
	newServer := int(nrand()) % len(ck.servers)
	return newServer
}

func (ck *Clerk) Request(args *CommandArgs) *CommandReply {
	/*var ok bool
	var leaderID int
	var lastReplyErrLeader bool
	*/
	for {
		// try each known server.
		for _, srv := range ck.servers {
			reply := &CommandReply{}
			switch args.ArgsType {
			case JoinArgsType:
				PrettyDebug(dClient, "Clerk%d send Join:", ck.clientID)
			case LeaveArgsType:
				PrettyDebug(dClient, "Clerk%d send Leave:", ck.clientID)
			case MoveArgsType:
				PrettyDebug(dClient, "Clerk%d send Move:", ck.clientID)
			case QueryArgsType:
				PrettyDebug(dClient, "Clerk%d send Query:", ck.clientID)
			default:
				PrettyDebug(dClient, "Clerk%d Request: args.ArgsType error", ck.clientID)
			}

			ok := srv.Call("ShardCtrler.RequestHandler", args, reply)
			if ok && reply.WrongLeader == false {
				ck.seqNum++
				return reply
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}
