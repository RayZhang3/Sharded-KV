package raft

//
// this is an outline of the API that raft must expose to
// the service (or tester). see comments below for
// each of these functions for more details.
//
// rf = Make(...)
//   create a new Raft server.
// rf.Start(command interface{}) (index, term, isleader)
//   start agreement on a new log entry
// rf.GetState() (term, isLeader)
//   ask a Raft for its current term, and whether it thinks it is leader
// ApplyMsg
//   each time a new entry is committed to the log, each Raft peer
//   should send an ApplyMsg to the service (or tester)
//   in the same server.
//

import (
	//	"bytes"

	"math/rand"
	"sync"
	"sync/atomic"

	//	"6.824/labgob"
	"fmt"

	"strings"
	"time"

	"6.824/labrpc"
)

//
// as each Raft peer becomes aware that successive log entries are
// committed, the peer should send an ApplyMsg to the service (or
// tester) on the same server, via the applyCh passed to Make(). set
// CommandValid to true to indicate that the ApplyMsg contains a newly
// committed log entry.
//
// in part 2D you'll want to send other kinds of messages (e.g.,
// snapshots) on the applyCh, but set CommandValid to false for these
// other uses.
//
type ApplyMsg struct {
	CommandValid bool
	Command      interface{}
	CommandIndex int

	// For 2D:
	SnapshotValid bool
	Snapshot      []byte
	SnapshotTerm  int
	SnapshotIndex int
}

// state
const (
	UNKNOWN   = 0
	LEADER    = 1
	FOLLOWER  = 2
	CANDIDATE = 3
)

//
// A Go object implementing a single Raft peer.
//
type Raft struct {
	mu        sync.Mutex          // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // RPC end points of all peers
	persister *Persister          // Object to hold this peer's persisted state
	me        int                 // this peer's index into peers[]
	dead      int32               // set by Kill()

	// Your data here (2A, 2B, 2C).
	// Look at the paper's Figure 2 for a description of what
	// state a Raft server must maintain.

	// Lab 2A
	lastTimeHeared time.Time // the last time at which the peer heard from the leader
	state          int
	Log            []LogEntry
	currentTerm    int
	votedFor       int

	commitIndex int // index of highest log entry known to be committed
	lastApplied int // index of highest log entry applied to state machine
	// As candidate
	//getVotes    []bool // TODO: CheckVote()
	getVotesNum int
	// As leader
	nextIndex  []int // for each server, index of the next log entry to send to that server
	matchIndex []int // for each server, index of highest log entry known to be replicated on server

	//Lab 2B
	applyCh chan ApplyMsg
	// End
}

func (rf Raft) String() string {
	return fmt.Sprintf("Raft{State: %d,  CurrentTerm: %d, VotedFor: %d, CommitIndex: %d, LastApplied: %d, GetVotesNum: %d, NextIndex: %v, MatchIndex: %v, Log: %s}",
		rf.state, rf.currentTerm, rf.votedFor, rf.commitIndex, rf.lastApplied, rf.getVotesNum, rf.nextIndex, rf.matchIndex, getLogString(rf.Log))
}

func (rf *Raft) getLastLogIndex() int {
	return len(rf.Log) - 1
}

func (rf *Raft) getLastLogTerm() int {
	if len(rf.Log) == 0 {
		return 0
	} else {
		return rf.Log[rf.getLastLogIndex()].Term
	}
}

func (rf *Raft) getLogAt(index int) (bool, *LogEntry) {
	if index >= len(rf.Log) {
		return false, nil
	} else {
		return true, &rf.Log[index]
	}
}

func (rf *Raft) getSameTermFirst(index int) (first int) {
	if index == 0 {
		return 0
	}
	sameTerm := rf.Log[index].Term
	var i int
	for i = index; i > 0 && rf.Log[i-1].Term == sameTerm; i-- {
	}
	return i
}

func (entry LogEntry) String() string {
	return fmt.Sprintf("{Term: %d, Command: %T}", entry.Term, entry.Command)
}

func getLogString(logs []LogEntry) string {
	strs := make([]string, len(logs))
	for i, entry := range logs {
		strs[i] = entry.String()
	}
	return "[" + strings.Join(strs, ", ") + "]"
}

// If there exists an N such that
// N > commitIndex, a majority of matchIndex[i] ≥ N, and log[N].term == currentTerm:
// set commitIndex = N (§5.3, §5.4).

func (rf *Raft) countReplica() int {
	first := rf.getSameTermFirst(rf.getLastLogIndex())
	end := rf.getLastLogIndex()
	N := rf.commitIndex
	for i := end; i > rf.commitIndex && i >= first; i-- {
		replica := 1
		for j := 0; j < len(rf.peers); j++ {
			if j == rf.me {
				continue
			}
			if rf.matchIndex[j] >= i {
				replica++
			}
		}
		if replica > len(rf.peers)/2 {
			N = i
			break
		}
	}
	return N

}

//
// RequestVote RPC arguments structure.
//
type RequestVoteArgs struct {
	// Your data here (2A, 2B).
	// Lab 2A
	Term         int // candidate's term
	CandidateId  int // candidate requesting vote
	LastLogIndex int // index of candidate's last log entry
	LastLogTerm  int // term of candidate's last log entry
	// End
}

func (rva RequestVoteArgs) String() string {
	return fmt.Sprintf("RequestVoteArgs{Term: %d, CandidateId: %d, LastLogIndex: %d, LastLogTerm: %d}",
		rva.Term, rva.CandidateId, rva.LastLogIndex, rva.LastLogTerm)
}

//
// RequestVote RPC reply structure.
//
type RequestVoteReply struct {
	// Your data here (2A).
	// Lab 2A
	Term        int  // currentTerm, for candidate to update itself
	VoteGranted bool // true means candidate received vote
	VoterId     int  // id who votes for the candidate
	// End
}

func (rvr RequestVoteReply) String() string {
	return fmt.Sprintf("RequestVoteReply{Term: %d, VoteGranted: %v}",
		rvr.Term, rvr.VoteGranted)
}

//
// AppendEntries RPC arguments structure
//
type AppendEntriesArgs struct {
	Term         int        // leader's term
	LeaderId     int        // so follower can redirect clients
	PrevLogIndex int        // index of log entry immediately preceding new ones
	PrevLogTerm  int        // term of prevLogIndex entry
	Entries      []LogEntry // log entries to store (empty for heartbeat; may send more than one for efficiency)
	LeaderCommit int        // leader's commitIndex
}

func (aea AppendEntriesArgs) String() string {
	return fmt.Sprintf("AppendEntriesArgs{Term: %d, LeaderId: %d, PrevLogIndex: %d, PrevLogTerm: %d, Entries: %s, LeaderCommit: %d}",
		aea.Term, aea.LeaderId, aea.PrevLogIndex, aea.PrevLogTerm, getLogString(aea.Entries), aea.LeaderCommit)
}

//
// AppendEntries RPC reply structure
//
type AppendEntriesReply struct {
	Term             int  // currentTerm, for leader to update itself
	Success          bool // true if follower contained entry matching prevLogIndex and prevLogTerm
	LogInconsistency bool // true if Log mismatch, nextIndex--
}

func (aer AppendEntriesReply) String() string {
	return fmt.Sprintf("AppendEntriesReply{Term: %d, Success: %v, LogInconsistency: %v}",
		aer.Term, aer.Success, aer.LogInconsistency)
}

//
// Log RPC reply structure
//
type LogEntry struct {
	Term    int
	Command interface{}
}

func (rf *Raft) LeaderState() {
	rf.state = LEADER
	for i := 0; i < len(rf.peers); i++ {
		rf.nextIndex[i] = rf.getLastLogIndex() + 1
		rf.matchIndex[i] = -1
	}
	// reset the commit index and lastApplied
	// rf.commitIndex = 0
	// rf.lastApplied = 0
}

func (rf *Raft) FollowerState(newTerm int) {
	PrettyDebug(dVote, "S%d becomes FOLLOWERS, old term %d, new term %d", rf.me, rf.currentTerm, newTerm)
	rf.state = FOLLOWER
	rf.currentTerm = newTerm
	rf.votedFor = -1 // reset the vote
	rf.getVotesNum = 0
}

func (rf *Raft) CandidateState() {
	rf.state = CANDIDATE
	rf.currentTerm += 1
	rf.votedFor = rf.me
	rf.getVotesNum = 1 // vote for itself
	PrettyDebug(dVote, "S%d becomes CANDIDATE", rf.me)
}

// return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {
	var term int
	var isleader bool
	// Your code here (2A).
	// Lock and unlock to get state
	rf.mu.Lock()
	defer rf.mu.Unlock()
	term = rf.currentTerm
	isleader = (rf.state == LEADER)
	// End

	return term, isleader
}

//
// save Raft's persistent state to stable storage,
// where it can later be retrieved after a crash and restart.
// see paper's Figure 2 for a description of what should be persistent.
//
func (rf *Raft) persist() {
	// Your code here (2C).
	// Example:
	// w := new(bytes.Buffer)
	// e := labgob.NewEncoder(w)
	// e.Encode(rf.xxx)
	// e.Encode(rf.yyy)
	// data := w.Bytes()
	// rf.persister.SaveRaftState(data)
}

//
// restore previously persisted state.
//
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}
	// Your code here (2C).
	// Example:
	// r := bytes.NewBuffer(data)
	// d := labgob.NewDecoder(r)
	// var xxx
	// var yyy
	// if d.Decode(&xxx) != nil ||
	//    d.Decode(&yyy) != nil {
	//   error...
	// } else {
	//   rf.xxx = xxx
	//   rf.yyy = yyy
	// }
}

//
// A service wants to switch to snapshot.  Only do so if Raft hasn't
// have more recent info since it communicate the snapshot on applyCh.
//
func (rf *Raft) CondInstallSnapshot(lastIncludedTerm int, lastIncludedIndex int, snapshot []byte) bool {

	// Your code here (2D).

	return true
}

// the service says it has created a snapshot that has
// all info up to and including index. this means the
// service no longer needs the log through (and including)
// that index. Raft should now trim its log as much as possible.
func (rf *Raft) Snapshot(index int, snapshot []byte) {
	// Your code here (2D).

}

//
//
// example RequestVote RPC handler.
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	PrettyDebug(dVote, "S%d receive RequestVote From S%d", rf.me, args.CandidateId)
	// Your code here (2A, 2B).
	// Lab 2A
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// If RPC request or response contains term T > currentTerm: set currentTerm = T, convert to follower
	if args.Term > rf.currentTerm {
		rf.FollowerState(args.Term)
	}

	// Refuse to RequestVote because currentTerm is larger than the candidate's term
	if args.Term < rf.currentTerm {
		reply.Term = rf.currentTerm
		reply.VoteGranted = false
		PrettyDebug(dVote, "S%d refuse to vote for S%d, my Term: %d, candidate Term: %d",
			rf.me, args.CandidateId, rf.currentTerm, args.Term)
		return
	}

	/*
		Check if the Candidate's log is up-to-date:
			1. If the logs have last entries with different terms, then the log with the later term is more up-to-date.
			2. If the logs end with the same term, then whichever log is longer is more up-to-date.
	*/
	var upToDate bool
	currentLastLogIndex := rf.getLastLogIndex()
	currentLastLogTerm := rf.getLastLogTerm()
	upToDate = args.LastLogTerm > currentLastLogTerm ||
		(args.LastLogTerm == currentLastLogTerm && args.LastLogIndex >= currentLastLogIndex)
	if (rf.votedFor == -1 || rf.votedFor == args.CandidateId) && upToDate {
		// Set reply
		reply.Term = rf.currentTerm
		reply.VoteGranted = true
		reply.VoterId = rf.me
		// Set votedFor
		rf.votedFor = args.CandidateId
		// Reset timer
		rf.lastTimeHeared = time.Now()

		PrettyDebug(dVote, "S%d vote for S%d, candidate Term: %d", rf.me, args.CandidateId, args.Term)
	} else {
		// Set reply
		reply.Term = rf.currentTerm
		reply.VoteGranted = false

		PrettyDebug(dVote, "S%d refuse to vote for S%d because of Log, candidate Term: %d", rf.me, args.CandidateId, args.Term)
	}
	// End
}

// Lab 2A
// To implement heartbeats, define an AppendEntries RPC struct
// (though you may not need all the arguments yet), and have the leader send them out periodically.
// Write an AppendEntries RPC handler method that resets the election timeout
// so that other servers don't step forward as leaders when one has already been elected.
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {

	infoString := fmt.Sprintf("Term %d, LeaderId:%d, PrevLogIndex:%d, PrevLogTerm:%d, LeaderCommit:%d, Entries %s",
		args.Term, args.LeaderId, args.PrevLogIndex, args.PrevLogTerm, args.LeaderCommit, getLogString(args.Entries))

	PrettyDebug(dVote, "S%d receive AppendEntries from S%d: %s", rf.me, args.LeaderId, infoString)

	// Lab 2A
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// If RPC request or response contains term T > currentTerm: set currentTerm = T, convert to follower
	if args.Term > rf.currentTerm {
		rf.FollowerState(args.Term)
	}

	// Return false, Refuse to AppendEntries because currentTerm is larger than the candidate's term
	if args.Term < rf.currentTerm {
		reply.Term = rf.currentTerm
		reply.Success = false
		PrettyDebug(dVote, "S%d refuse to AppendEntries from S%d, my Term: %d, candidate Term: %d",
			rf.me, args.LeaderId, rf.currentTerm, args.Term)
		return
	}

	// Reset timer
	rf.lastTimeHeared = time.Now()

	// Receive HeartBeat, Entries == nil
	if args.Entries == nil || len(args.Entries) == 0 {
		heartBeatString := fmt.Sprintf("commitIndex %d, LastApplied %d, LogLength %d, Follower's Log %s",
			rf.commitIndex, rf.lastApplied, len(rf.Log), getLogString(rf.Log))
		PrettyDebug(dVote, "S%d receive heartBeat from S%d, info: %s", rf.me, args.LeaderId, heartBeatString)
	}

	// Log misamtch
	// Reply false if log doesn’t contain an entry at prevLogIndex whose term matches prevLogTerm
	contains, prevEntry := rf.getLogAt(args.PrevLogIndex)
	if !contains || prevEntry.Term != args.PrevLogTerm {
		reply.Term = rf.currentTerm
		reply.Success = false
		reply.LogInconsistency = true
		PrettyDebug(dLog2, "S%d mismatch at prevLogIndex %d, current log length %d",
			rf.me, args.PrevLogIndex, len(rf.Log))
		return
	}

	// If an existing entry conflicts with a new one (same index but different terms),
	// delete the existing entry and all that follow it

	for i, j := 0, args.PrevLogIndex+1; i < len(args.Entries) && j < len(rf.Log); i, j = i+1, j+1 {
		if args.Entries[i].Term != rf.Log[j].Term {
			conflitsString := fmt.Sprintf("PrevLogIndex %d, PrevLogTerm %d, Log %s, Entries %s",
				args.PrevLogIndex, args.PrevLogTerm, getLogString(rf.Log), getLogString(args.Entries))
			PrettyDebug(dLog2, "S%d conflists info: %s", rf.me, conflitsString)
			rf.Log = rf.Log[:j]
			PrettyDebug(dLog2, "S%d Log after conflits: %s", rf.me, getLogString(rf.Log))
			break
		}
	}

	// Append any new entries not already in the log

	for i, j := 0, args.PrevLogIndex+1; i < len(args.Entries); i, j = i+1, j+1 {
		// Reach the end of Log
		if j >= len(rf.Log) {
			rf.Log = append(rf.Log, args.Entries[i])
			//fmt.Println("rf.Log", rf.Log)
			continue
		}
		// Same LogEntry
		if args.Entries[i].Term == rf.Log[j].Term {
			continue
		} else { // Different log? I don't think it should be here
			PrettyDebug(dLog2, "S%d Error, has conflits in new AppendLog %s", rf.me, getLogString(rf.Log))
		}

	}

	// If leaderCommit > commitIndex, set commitIndex = min(leaderCommit, index of last new entry)
	if args.LeaderCommit > rf.commitIndex {
		if args.LeaderCommit < rf.getLastLogIndex() {
			rf.commitIndex = args.LeaderCommit
		} else {
			rf.commitIndex = rf.getLastLogIndex()
		}
	}

	PrettyDebug(dLog, "S%d match at prevLogIndex %d, current log length %d, Log: %s",
		rf.me, args.PrevLogIndex, len(rf.Log), getLogString(rf.Log))
	// Set reply
	reply.Term = rf.currentTerm
	reply.Success = true
}

//
// example code to send a RequestVote RPC to a server.
// server is the index of the target server in rf.peers[].
// expects RPC arguments in args.
// fills in *reply with RPC reply, so caller should
// pass &reply.
// the types of the args and reply passed to Call() must be
// the same as the types of the arguments declared in the
// handler function (including whether they are pointers).
//
// The labrpc package simulates a lossy network, in which servers
// may be unreachable, and in which requests and replies may be lost.
// Call() sends a request and waits for a reply. If a reply arrives
// within a timeout interval, Call() returns true; otherwise
// Call() returns false. Thus Call() may not return for a while.
// A false return can be caused by a dead server, a live server that
// can't be reached, a lost request, or a lost reply.
//
// Call() is guaranteed to return (perhaps after a delay) *except* if the
// handler function on the server side does not return.  Thus there
// is no need to implement your own timeouts around Call().
//
// look at the comments in ../labrpc/labrpc.go for more details.
//
// if you're having trouble getting RPC to work, check that you've
// capitalized all field names in structs passed over RPC, and
// that the caller passes the address of the reply struct with &, not
// the struct itself.
//
func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	ok := rf.peers[server].Call("Raft.RequestVote", args, reply)
	return ok
}

func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}

func (rf *Raft) leaderAppendEntries() {
	for i := 0; i < len(rf.peers); i++ {
		if i == rf.me {
			continue
		}
		go func(index int) {
			// Set args
			rf.mu.Lock()
			args := &AppendEntriesArgs{
				Term:     rf.currentTerm,
				LeaderId: rf.me,
				//PrevLogIndex: rf.nextIndex[index] - 1,
				//PrevLogTerm:  rf.Log[rf.nextIndex[index]-1].Term,
				PrevLogIndex: 0,
				PrevLogTerm:  0,
				Entries:      nil,
				LeaderCommit: rf.commitIndex,
			}
			//
			// args.PrevLogTerm = rf.Log[args.PrevLogIndex].Term
			// If last log index ≥ nextIndex for a follower:
			// send AppendEntries RPC with log entries starting at nextIndex
			if rf.nextIndex[index] > 1 {
				args.PrevLogIndex = rf.nextIndex[index] - 1
				args.PrevLogTerm = rf.Log[args.PrevLogIndex].Term
			}
			if rf.getLastLogIndex() >= rf.nextIndex[index] {
				args.Entries = rf.Log[rf.nextIndex[index]:]
			}

			PrettyDebug(dLog, "S%d send AppendEntries: %s", rf.me, args.String())

			// set Leader's nextIndex and matchIndex
			rf.nextIndex[rf.me] = rf.getLastLogIndex() + 1
			rf.matchIndex[rf.me] = rf.getLastLogIndex()

			reply := &AppendEntriesReply{}

			rf.mu.Unlock()

			rf.sendAppendEntries(index, args, reply)

			// If RPC request or response contains term T > currentTerm: set currentTerm = T, convert to follower
			rf.mu.Lock()
			if reply.Term > rf.currentTerm {
				rf.FollowerState(reply.Term)
				rf.mu.Unlock()
				return
			}

			// If successful: update nextIndex and matchIndex for follower (§5.3)
			// If AppendEntries fails because of log inconsistency: decrement nextIndex and retry (§5.3)
			if reply.Success {
				rf.matchIndex[index] = args.PrevLogIndex + len(args.Entries)
				rf.nextIndex[index] = rf.matchIndex[index] + 1
			} else {
				if reply.LogInconsistency {
					rf.nextIndex[index] = rf.nextIndex[index] - 1
				}
			}

			// If there exists an N such that
			// N > commitIndex, a majority of matchIndex[i] ≥ N, and log[N].term == currentTerm:
			// set commitIndex = N (§5.3, §5.4).

			rf.commitIndex = rf.countReplica()

			rf.mu.Unlock()
			// term >= currentTerm
			// If RPC request or response contains term T > currentTerm:
			// Set currentTerm = T, convert to follower
		}(i)
	}
	PrettyDebug(dLog, "S%d finished send AppendEntries: %s", rf.me, rf.String())
}

func (rf *Raft) candidateRequestVote() {
	rf.mu.Lock()
	currentTerm := rf.currentTerm
	candidateId := rf.me
	rf.mu.Unlock()

	for i := 0; i < len(rf.peers); i++ {
		if i == rf.me {
			continue
		}
		go func(index int) {
			args := &RequestVoteArgs{
				Term:         currentTerm,
				CandidateId:  candidateId,
				LastLogIndex: 0,
				LastLogTerm:  0,
			}
			if len(rf.Log) > 1 {
				args.LastLogIndex = rf.getLastLogIndex()
				args.LastLogTerm = rf.Log[args.LastLogIndex].Term
			}
			reply := &RequestVoteReply{}

			rf.sendRequestVote(index, args, reply)

			rf.mu.Lock()

			// If RPC request or response contains termT > currentTerm: set currentTerm = T, convert to follower
			if reply.Term > rf.currentTerm {
				rf.FollowerState(reply.Term)
			}

			PrettyDebug(dVote, "S%d currentTerm is %d, receiveTerm is %d", rf.me, rf.currentTerm, reply.Term)

			if reply.Term == rf.currentTerm && rf.state == CANDIDATE {
				if reply.VoteGranted {
					rf.getVotesNum += 1
					PrettyDebug(dVote, "S%d get Vote, has votes %d", rf.me, rf.getVotesNum)
					if rf.getVotesNum > len(rf.peers)/2 {
						rf.LeaderState()
						PrettyDebug(dVote, "S%d becomes LEADER", rf.me)
					}
				}
			}
			rf.mu.Unlock()
		}(i)
	}
}

//
// the service using Raft (e.g. a k/v server) wants to start
// agreement on the next command to be appended to Raft's log.
// if this server isn't the leader, returns false.
// otherwise start the agreement and return immediately.
// there is no guarantee that this command will ever be committed to the Raft log, since the leader
// may fail or lose an election.
// even if the Raft instance has been killed, this function should return gracefully.
//
// the first return value is the index that the command will appear at if it's ever committed.
// the second return value is the current term.
// the third return value is true if this server believes it is the leader.
//
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	index := -1
	term := -1
	isLeader := true
	// Test Debug
	// PrettyDebug(dClient, "S%v start a test\n", rf.me)
	// Your code here (2B).
	// even if the Raft instance has been killed, this function should return gracefully.
	if rf.killed() {
		isLeader = false
		return index, term, isLeader
	}

	rf.mu.Lock()
	defer rf.mu.Unlock()

	// if this server isn't the leader, returns false.
	index = rf.getLastLogIndex() + 1 // the index that the command will appear at if it's ever committed.
	term = rf.currentTerm
	isLeader = (rf.state == LEADER)
	if !isLeader {
		return index, term, false
	}
	entry := LogEntry{Term: term, Command: command}
	rf.Log = append(rf.Log, entry)

	return index, term, isLeader
}

//
// the tester doesn't halt goroutines created by Raft after each test,
// but it does call the Kill() method. your code can use killed() to
// check whether Kill() has been called. the use of atomic avoids the
// need for a lock.
//
// the issue is that long-running goroutines use memory and may chew
// up CPU time, perhaps causing later tests to fail and generating
// confusing debug output. any goroutine with a long-running loop
// should call killed() to check whether it should stop.
//
func (rf *Raft) Kill() {
	atomic.StoreInt32(&rf.dead, 1)
	// Your code here, if desired.
}

func (rf *Raft) killed() bool {
	z := atomic.LoadInt32(&rf.dead)
	return z == 1
}

func (rf *Raft) heartBeat() {
	// Heatbeat time out
	HeartbeatTimeout := make(chan bool)
	go func() {
		for {
			time.Sleep(1e8)
			HeartbeatTimeout <- true
		}
	}()
	//PrettyDebug(dTest, "S%d HeartbeatTimeout created", rf.me)
	for rf.killed() == false {
		if <-HeartbeatTimeout {
			//PrettyDebug(dTest, "S%d HeartbeatTimeout check", rf.me)
			rf.mu.Lock()
			if rf.state != LEADER {
				rf.mu.Unlock()
				continue
			}
			rf.mu.Unlock()
			PrettyDebug(dVote, "S%d Append Entries", rf.me)
			rf.leaderAppendEntries()
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The ticker go routine starts a new election if this peer hasn't received
// heartsbeats recently.
func (rf *Raft) ticker() {

	// Election time out
	// generate a random number from 1e9 to 2e9, type uint64
	randNum := int64(1000 + rand.Intn(1000))
	var randTime int64
	randTime = randNum * 1000000 // 1s - 2s
	rf.lastTimeHeared = time.Now()
	PeriodicTimeout := make(chan bool)
	go func() {
		for {
			time.Sleep(2e7)
			PeriodicTimeout <- true
		}
	}()
	//PrettyDebug(dTest, "S%d PeriodicTimeout created", rf.me)

	for rf.killed() == false {
		// Your code here to check if a leader election should
		// be started and to randomize sleeping time using
		// time.Sleep().
		if <-PeriodicTimeout {
			//PrettyDebug(dTest, "S%d PeriodicTimeout check", rf.me)
			rf.mu.Lock()
			currentTime := time.Now()
			if rf.state == LEADER {
				rf.mu.Unlock()
				continue
			}
			if currentTime.Sub(rf.lastTimeHeared).Nanoseconds() < randTime {
				rf.mu.Unlock()
				continue
			}
			rf.CandidateState()
			randNum = int64(1000 + rand.Intn(1000))
			randTime = randNum * 1000000

			rf.lastTimeHeared = currentTime

			rf.mu.Unlock()

			PrettyDebug(dVote, "S%d request for vote", rf.me)
			rf.candidateRequestVote()
		}
		time.Sleep(10 * time.Millisecond)
	}

}

//
// Lab 2B
// Make sure that you check for commitIndex > lastApplied either periodically,
// or after commitIndex is updated (i.e., after matchIndex is updated).
// For example, if you check commitIndex at the same time as sending out AppendEntries to peers,
// you may have to wait until the next entry is appended to the log
// before applying the entry you just sent out and got acknowledged.
func (rf *Raft) applyChecker() {
	// Heatbeat time out
	ApplyTimeout := make(chan bool)
	go func() {
		for {
			time.Sleep(1e8)
			ApplyTimeout <- true
		}
	}()
	for rf.killed() == false {
		if <-ApplyTimeout {
			rf.mu.Lock()

			// If commitIndex > lastApplied: increment lastApplied, apply log[lastApplied] to state machine (§5.3)
			if rf.commitIndex > rf.lastApplied {
				rf.lastApplied++
				PrettyDebug(dLog, "S%d Apply Entries at %d, commitIndex is %d", rf.me, rf.lastApplied, rf.commitIndex)
				applyMsg := ApplyMsg{
					CommandValid: true,
					Command:      rf.Log[rf.lastApplied].Command,
					CommandIndex: rf.lastApplied,
				}
				rf.applyCh <- applyMsg
			}
			rf.mu.Unlock()
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// End
//
// the service or tester wants to create a Raft server. the ports
// of all the Raft servers (including this one) are in peers[]. this
// server's port is peers[me]. all the servers' peers[] arrays
// have the same order.

// persister is a place for this server to
// save its persistent state, and also initially holds the most
// recent saved state, if any.

// applyCh is a channel on which the
// tester or service expects Raft to send ApplyMsg messages.
// Make() must return quickly, so it should start goroutines
// for any long-running work.
//
func Make(peers []*labrpc.ClientEnd, me int,
	persister *Persister, applyCh chan ApplyMsg) *Raft {
	rf := &Raft{
		state:       FOLLOWER,
		currentTerm: 0,
		votedFor:    -1,
		commitIndex: 0,
		lastApplied: 0,
		getVotesNum: 0,
	}
	rf.peers = peers
	rf.persister = persister
	rf.me = me

	// Your initialization code here (2A, 2B, 2C).

	// Lab 2A
	rf.lastTimeHeared = time.Now()
	// Lab 2B
	rf.Log = make([]LogEntry, 1) // Log's First index is 1
	rf.applyCh = applyCh
	// Error
	//rf.Log = append(rf.Log, LogEntry{0, nil}) // Log's First index is 1
	//fmt.Println(rf.Log)

	rf.nextIndex = make([]int, len(rf.peers)) // nextIndex for each server, initiate to leader last log index + 1
	for i := 0; i < len(rf.peers); i++ {
		rf.nextIndex[i] = 1
	}
	rf.matchIndex = make([]int, len(rf.peers))
	/*	Election time out

		The management of the election timeout is a common source of headaches.
		Perhaps the simplest plan is to
		1. Maintain a variable in the Raft struct containing the last time at which the peer heard from the leader
		2. to have the election timeout goroutine periodically check
			to see whether the time since then is greater than the timeout period.
		3. It's easiest to use time.Sleep() with a small constant argument to drive the periodic checks.
		4. Don't use time.Ticker and time.Timer. They are tricky to use correctly.
	*/

	/*	Election backround routine
		Modify Make() to create a background goroutine that will
		1. kick off leader election periodically by sending out RequestVote RPCs
			when it hasn't heard from another peer for a while.
			This way a peer will learn who is the leader, if there is already a leader,
			or become the leader itself.
		(Lab 2A) 2. A Raft instance has two time-driven activities:
			the leader must send heart-beats,
			and others must start an election if too much time has passed since hearing from the leader.
			It's probably best to drive each of these activities with a dedicated long-running goroutine,
			rather than combining multiple activities into a single goroutine.
	*/

	// End

	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	// start ticker goroutine to start elections
	go rf.ticker()
	go rf.heartBeat()
	go rf.applyChecker()

	//PrettyDebug(dTest, "S%d created", rf.me)
	return rf
}
