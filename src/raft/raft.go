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

	"bytes"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"

	//	"6.824/labgob"
	"fmt"

	"time"

	"6.824/labgob"
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
	// persist state:
	// currentTerm, votedFor, Log, lastIncludedIndex, lastIncludedTerm, firstIndex
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

	// Lab2D
	lastIncludedIndex int
	lastIncludedTerm  int
	snapShot          []byte
	firstIndex        int
	// End
}

func (rf Raft) String() string {
	return fmt.Sprintf("Raft{State: %d,  CurrentTerm: %d, VotedFor: %d, CommitIndex: %d, LastApplied: %d, GetVotesNum: %d, NextIndex: %v, MatchIndex: %v, Log: %s}",
		rf.state, rf.currentTerm, rf.votedFor, rf.commitIndex, rf.lastApplied, rf.getVotesNum, rf.nextIndex, rf.matchIndex, getLogString(rf.Log))
}

func (rf *Raft) getRealLogIndex(logicalIndex int) int {
	return logicalIndex - rf.firstIndex
}

func (rf *Raft) getLastLogIndex() int {
	length := len(rf.Log)
	switch length {
	case 0:
		PrettyDebug(dError, "S %dlog length is 0", rf.me)
		return 0
	case 1:
		if rf.lastIncludedIndex == -1 && rf.lastIncludedTerm == -1 {
			return 0
		} else {
			return rf.lastIncludedIndex
		}
	default:
		return (len(rf.Log) - 1) + rf.firstIndex
	}
}

func (rf *Raft) getLastLogTerm() int {
	length := len(rf.Log)
	switch length {
	case 0:
		PrettyDebug(dError, "S %dlog length is 0", rf.me)
		return 0
	case 1:
		if rf.lastIncludedIndex == -1 && rf.lastIncludedTerm == -1 {
			return 0
		} else {
			return rf.lastIncludedTerm
		}
	default:
		return rf.Log[len(rf.Log)-1].Term
	}
}

func (rf *Raft) getLogAt(realIndex int) (bool, *LogEntry) {
	if realIndex >= len(rf.Log) {
		return false, nil
	} else {
		return true, &rf.Log[realIndex]
	}
}

func (rf *Raft) getSameTermFirst(realIndex int) (first int) {
	if realIndex == 0 {
		return 0
	}
	sameTerm := rf.Log[realIndex].Term
	var i int
	for i = realIndex; i > 0 && rf.Log[i-1].Term == sameTerm; i-- {
	}
	return i
}

func (entry LogEntry) String() string {
	return fmt.Sprintf("{Term: %d, Index %d, Command: %T}", entry.Term, entry.Index, entry.Command)
}

func getLogString(logs []LogEntry) string {
	//return " "

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
	//first := rf.getSameTermFirst(rf.getLastLogIndex())
	// not Log in this term.
	if rf.getLastLogTerm() != rf.currentTerm {
		if rf.lastIncludedIndex == -1 && rf.lastIncludedTerm == -1 {
			return 0
		} else {
			if rf.lastIncludedIndex >= rf.commitIndex {
				return rf.lastIncludedIndex
			} else {
				return rf.commitIndex
			}
		}
	}

	realIndexStart := len(rf.Log) - 1
	for i := 0; i < len(rf.Log); i++ {
		if rf.Log[i].Term == rf.currentTerm {
			realIndexStart = i
			break
		}
	}
	realIndexEnd := len(rf.Log) - 1

	N := rf.commitIndex
	for i := realIndexEnd; i+rf.firstIndex > rf.commitIndex && i >= realIndexStart; i-- {
		replica := 1
		for j := 0; j < len(rf.peers); j++ {
			if j == rf.me {
				continue
			}
			if rf.matchIndex[j] >= i+rf.firstIndex {
				replica++
			}
		}
		if replica > len(rf.peers)/2 {
			N = i + rf.firstIndex
			break
		}
	}

	if N < rf.lastIncludedIndex {
		N = rf.lastIncludedIndex
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

type InstallSnapshotArgs struct {
	Term              int
	LeaderId          int
	LastIncludedIndex int
	LastIncludedTerm  int
	Data              []byte
}

func (rvr InstallSnapshotArgs) String() string {
	return fmt.Sprintf("InstallSnapshotArgs{Term: %d, LeaderId: %d, LastIncludedIndex: %d, LastIncludedTerm: %d, Data: %s}",
		rvr.Term, rvr.LeaderId, rvr.LastIncludedIndex, rvr.LastIncludedTerm, string(rvr.Data))
}

type InstallSnapshotReply struct {
	Term int
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
	/*
		Follower
			1. (Safety) If PrevLogIndex < commitIndex, set XTerm to Log[commitIndex].Term, XIndex to commitIndex + 1
			2. (Log Unconsistency) If PrevLogIndex > len(rf.Log), set XTerm to -1, XIndex to len(rf.Log)
			3. (Log Unconsistency) Else, set XTerm to Log[PrevLogIndex].Term, XIndex to rf.getSameTermFirst(args.PrevLogIndex)

		Leader
			1.If xTerm is -1, follower's Log is shorter than leader's Log, set nextIndex = XLen
			2.Leader should first search its log for conflictTerm.
			3.If it finds an entry in its log with that term, set nextIndex to getSameTermLast() + 1 (the one beyond the index of the last entry in that term in its log)
			4.If it does not find, set nextIndex = XIndex.
	*/
	// Lab 2B Fast Backup
	XTerm  int // Term of conflicting Log, Log[PrevLogIndex].Term or -1
	XIndex int // Index of first log in conflicting term, valid when XTerm != -1
	XLen   int // Length of log
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
	Index   int
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
	rf.persist()
}

func (rf *Raft) CandidateState() {
	rf.state = CANDIDATE
	rf.currentTerm += 1
	rf.votedFor = rf.me
	rf.getVotesNum = 1 // vote for itself
	rf.persist()
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
	/*
		w := new(bytes.Buffer)
		e := labgob.NewEncoder(w)
		e.Encode(rf.currentTerm)
		e.Encode(rf.votedFor)
		e.Encode(rf.Log)
		e.Encode(rf.lastIncludedIndex)
		e.Encode(rf.lastIncludedTerm)
		e.Encode(rf.firstIndex)
	*/
	data := rf.getStatePersistData()
	rf.persister.SaveRaftState(data)
}

func (rf *Raft) getStatePersistData() []byte {
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)
	e.Encode(rf.currentTerm)
	e.Encode(rf.votedFor)
	e.Encode(rf.Log)
	e.Encode(rf.lastIncludedIndex)
	e.Encode(rf.lastIncludedTerm)
	e.Encode(rf.firstIndex)
	data := w.Bytes()
	return data
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
	r := bytes.NewBuffer(data)
	d := labgob.NewDecoder(r)
	var currentTerm int
	var votedFor int
	var Log []LogEntry
	var lastIncludedIndex int
	var lastIncludedTerm int
	var firstIndex int
	if d.Decode(&currentTerm) != nil || d.Decode(&votedFor) != nil || d.Decode(&Log) != nil ||
		d.Decode(&lastIncludedIndex) != nil || d.Decode(&lastIncludedTerm) != nil || d.Decode(&firstIndex) != nil {
		PrettyDebug(dPersist, "S%d Persist error", rf.me)
		return
	} else {
		rf.mu.Lock()
		rf.currentTerm = currentTerm
		rf.votedFor = votedFor
		rf.Log = Log
		rf.lastIncludedIndex = lastIncludedIndex
		rf.lastIncludedTerm = lastIncludedTerm
		rf.firstIndex = firstIndex
		rf.lastApplied = lastIncludedIndex
		rf.commitIndex = lastIncludedIndex
		rf.mu.Unlock()
	}
}

//
// A service wants to switch to snapshot.  Only do so if Raft hasn't
// have more recent info since it communicate the snapshot on applyCh.
//
func (rf *Raft) CondInstallSnapshot(lastIncludedTerm int, lastIncludedIndex int, snapshot []byte) bool {

	// Your code here (2D).

	return true
}

// the service says it has created a snapshot that has all info up to and including index. this means the
// service no longer needs the log through (and including) that index.
// Raft should now trim its log as much as possible.
func (rf *Raft) Snapshot(index int, snapshot []byte) {
	// Your code here (2D).
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if index <= rf.lastIncludedIndex {
		return
	}
	// Raft should now trim its log as much as possible.
	realIndex := len(rf.Log)
	for i := 0; i < len(rf.Log); i++ {
		if i+rf.firstIndex == index {
			realIndex = i
			break
		}
	}
	rf.lastIncludedIndex = index
	rf.lastIncludedTerm = rf.Log[realIndex].Term
	rf.firstIndex = rf.lastIncludedIndex

	logHead := make([]LogEntry, 1)
	rf.Log = rf.Log[realIndex+1:]
	rf.Log = append(logHead, rf.Log...)

	data := rf.getStatePersistData()
	rf.persister.SaveStateAndSnapshot(data, snapshot)
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
		rf.persist()
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
		rf.persist()
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

func (rf *Raft) debugAppendEntries(args *AppendEntriesArgs) {
	infoString := fmt.Sprintf("Term %d, LeaderId:%d, PrevLogIndex:%d, PrevLogTerm:%d, LeaderCommit:%d, Entries %s",
		args.Term, args.LeaderId, args.PrevLogIndex, args.PrevLogTerm, args.LeaderCommit, getLogString(args.Entries))
	PrettyDebug(dVote, "S%d receive AppendEntries from S%d: %s", rf.me, args.LeaderId, infoString)
}

func (rf *Raft) debugHeartBeat(args *AppendEntriesArgs) {
	heartBeatString := fmt.Sprintf("commitIndex %d, LastApplied %d, LogLength %d, Follower's Log %s",
		rf.commitIndex, rf.lastApplied, len(rf.Log), getLogString(rf.Log))
	PrettyDebug(dVote, "S%d receive heartBeat from S%d, info: %s", rf.me, args.LeaderId, heartBeatString)
}

func (rf *Raft) debugConflictsLog(args *AppendEntriesArgs) {
	conflitsString := fmt.Sprintf("PrevLogIndex %d, PrevLogTerm %d, Log %s, Entries %s",
		args.PrevLogIndex, args.PrevLogTerm, getLogString(rf.Log), getLogString(args.Entries))
	PrettyDebug(dLog2, "S%d conflists info: %s", rf.me, conflitsString)
}

// Lab 2A
// To implement heartbeats, define an AppendEntries RPC struct
// (though you may not need all the arguments yet), and have the leader send them out periodically.
// Write an AppendEntries RPC handler method that resets the election timeout
// so that other servers don't step forward as leaders when one has already been elected.
// Lab 2A
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	rf.debugAppendEntries(args)

	// If RPC request or response contains term T > currentTerm: set currentTerm = T, convert to follower
	// If AppendEntries RPC received from new leader: convert to follower
	if args.Term > rf.currentTerm || (args.Term == rf.currentTerm && rf.state == CANDIDATE) {
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
		rf.debugHeartBeat(args)
	}

	// we can't modify committed Log, this condition happens When
	// 1. Follower receive AppendEntries RPC from Old Leader (partitioned Leader or poor network)
	// 2. Leader tries to send AppendEntries, decrement nextIndex and resend the AppendEntries request
	//    because of poor network, the nextIndex is finally less than the commitIndex of the follower.
	// So, we need to set the conflits Log Entry after committed Log Entries.

	// Log misamtch
	// Reply false if log doesn’t contain an entry at prevLogIndex whose term matches prevLogTerm

	firstLogIndex := rf.firstIndex
	lastLogIndex := rf.firstIndex + len(rf.Log) - 1

	if args.PrevLogIndex > lastLogIndex {
		reply.Term = rf.currentTerm
		reply.Success = false
		reply.XTerm = -1
		reply.XIndex = lastLogIndex + 1
		reply.LogInconsistency = true
		PrettyDebug(dError, "S%d mismatch at prevLogIndex %d > lastLogIndex, FirstLogIndex: %d, LastLogIndex: %d, set XIndex = %d",
			rf.me, args.PrevLogIndex, firstLogIndex, lastLogIndex, reply.XIndex)
		return
	}

	if args.PrevLogIndex < firstLogIndex {
		reply.Success = false
		reply.Term = rf.currentTerm
		reply.XTerm = -1
		reply.XIndex = firstLogIndex + 1
		reply.LogInconsistency = true
		PrettyDebug(dError, "S%d mismatch at prevLogIndex %d, < firstLogIndex, FirstLogIndex: %d, LastLogIndex: %d, set XIndex = %d",
			rf.me, args.PrevLogIndex, firstLogIndex, lastLogIndex, reply.XIndex)
		return
	}

	realIndex := args.PrevLogIndex - rf.firstIndex

	contains, prevEntry := rf.getLogAt(realIndex)
	if !contains || prevEntry.Term != args.PrevLogTerm {
		reply.Term = rf.currentTerm
		reply.Success = false
		reply.LogInconsistency = true
		// fast backup
		if !contains {
			reply.XTerm = -1
			reply.XIndex = lastLogIndex + 1
		} else {
			reply.XTerm = prevEntry.Term
			reply.XIndex = rf.getSameTermFirst(realIndex) + rf.firstIndex
		}
		PrettyDebug(dLog2, "S%d mismatch at prevLogIndex %d, current log length %d",
			rf.me, args.PrevLogIndex, len(rf.Log))
		return
	}

	needPersist := false
	// If an existing entry conflicts with a new one (same index but different terms),
	// delete the existing entry and all that follow it

	for i, j := 0, realIndex+1; i < len(args.Entries) && j < len(rf.Log); i, j = i+1, j+1 {
		if args.Entries[i].Term != rf.Log[j].Term {
			rf.debugConflictsLog(args)
			rf.Log = rf.Log[:j]
			PrettyDebug(dLog2, "S%d Log after conflits: %s", rf.me, getLogString(rf.Log))
			needPersist = true
			break
		}
	}

	// Append any new entries not already in the log

	startAppendIndex := realIndex + 1
	// Check if we've reached the end of Log
	if startAppendIndex >= len(rf.Log) {
		rf.Log = append(rf.Log, args.Entries...)
		needPersist = true
	} else {
		// Same LogEntry, do nothing
		// Different log, should not happen due to prior conflict resolution
		for i := 0; i < len(args.Entries); i++ {
			if startAppendIndex+i >= len(rf.Log) {
				rf.Log = append(rf.Log, args.Entries[i])
				needPersist = true
				continue
			}
			if args.Entries[i].Term != rf.Log[startAppendIndex+i].Term {
				PrettyDebug(dLog2, "S%d Error, has conflits in new AppendLog %s", rf.me, getLogString(rf.Log))
				break
			}
		}
	}

	if needPersist {
		rf.persist()
	}

	// If leaderCommit > commitIndex, set commitIndex = min(leaderCommit, index of last new entry)
	if args.LeaderCommit > rf.commitIndex {
		if args.LeaderCommit < lastLogIndex {
			rf.commitIndex = args.LeaderCommit
		} else {
			rf.commitIndex = lastLogIndex
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

func (rf *Raft) sendInstallSnapshot(server int, args *InstallSnapshotArgs, reply *InstallSnapshotReply) bool {
	ok := rf.peers[server].Call("Raft.InstallSnapshot", args, reply)
	return ok
}

func (rf *Raft) InstallSnapshot(args *InstallSnapshotArgs, reply *InstallSnapshotReply) {
	rf.mu.Lock()
	if args.Term > rf.currentTerm {
		rf.FollowerState(args.Term)
	}

	upToDate := args.LastIncludedTerm > rf.lastIncludedTerm ||
		(args.LastIncludedTerm == rf.lastIncludedTerm && args.LastIncludedIndex > rf.lastIncludedIndex)
	if !upToDate {
		reply.Term = rf.currentTerm
		rf.mu.Unlock()
		return
	}

	snapShopCopy := make([]byte, len(args.Data))
	copy(snapShopCopy, args.Data)
	rf.snapShot = snapShopCopy
	reply.Term = rf.currentTerm

	// 6. If existing log entry has same index and term as snapshot’s
	// 	  last included entry, retain log entries following it and reply
	// 7. Discard the entire log

	// Trim log
	index := len(rf.Log)
	for i := len(rf.Log) - 1; i >= 0; i-- {
		if (rf.Log[i].Term == args.LastIncludedTerm) && (i+rf.firstIndex == args.LastIncludedIndex) {
			index = i + 1
			break
		}
	}
	newLog := make([]LogEntry, 1)
	newLogTail := make([]LogEntry, len(rf.Log[index:]))
	copy(newLogTail, rf.Log[index:])
	rf.Log = append(newLog, newLogTail...)

	rf.lastIncludedIndex = args.LastIncludedIndex
	rf.lastIncludedTerm = args.LastIncludedTerm

	rf.firstIndex = rf.lastIncludedIndex
	if rf.commitIndex < rf.lastIncludedIndex {
		rf.commitIndex = rf.lastIncludedIndex
	}
	if rf.lastApplied < rf.lastIncludedIndex {
		rf.lastApplied = rf.lastIncludedIndex
	}

	// Reset state machine using snapshot contents
	data := rf.getStatePersistData()
	rf.persister.SaveStateAndSnapshot(data, snapShopCopy)

	applyMsg := ApplyMsg{
		SnapshotValid: true,
		Snapshot:      snapShopCopy,
		SnapshotTerm:  args.LastIncludedTerm,
		SnapshotIndex: args.LastIncludedIndex,
	}
	rf.mu.Unlock()
	rf.applyCh <- applyMsg

}

func (rf *Raft) leaderAppendEntries() {
	for i := 0; i < len(rf.peers); i++ {
		if rf.killed() {
			return
		}
		if i == rf.me {
			continue
		}
		go func(index int) {
			// Set args

			rf.mu.Lock()
			// output a string of raft
			stateString := fmt.Sprintf("Raft{State: %d,  CurrentTerm: %d, VotedFor: %d, CommitIndex: %d, LastApplied: %d, GetVotesNum: %d, NextIndex: %v, MatchIndex: %v, FirstIndex: %v, LastIncludedIndex: %v, LastIncludedTerm: %v, Log: %s}",
				rf.state, rf.currentTerm, rf.votedFor, rf.commitIndex, rf.lastApplied, rf.getVotesNum, rf.nextIndex, rf.matchIndex, rf.firstIndex, rf.lastIncludedIndex, rf.lastIncludedTerm, getLogString(rf.Log))
			PrettyDebug(dSnap, "S%d state %s", rf.me, stateString)

			PrevLogIndex := rf.nextIndex[index] - 1
			if PrevLogIndex < rf.lastIncludedIndex {
				InstallSnapshotArgs := &InstallSnapshotArgs{
					Term:              rf.currentTerm,
					LeaderId:          rf.me,
					LastIncludedIndex: rf.lastIncludedIndex,
					LastIncludedTerm:  rf.lastIncludedTerm,
					Data:              rf.persister.ReadSnapshot(),
				}
				reply := &InstallSnapshotReply{}
				rf.mu.Unlock()
				ok := false
				retry := 0
				for !ok && retry < 3 {
					ok = rf.sendInstallSnapshot(index, InstallSnapshotArgs, reply)
					retry++
					if !ok {
						time.Sleep(100 * time.Millisecond)
					}
				}
				rf.mu.Lock()
				// send InstallSnapShot RPC
				// need to set nextIndex and matchIndex
				PrettyDebug(dSnap, "S%d send installSnapShot RPC to S%d %s", rf.me, index, InstallSnapshotArgs.String())
				stateString := fmt.Sprintf("Raft{State: %d,  CurrentTerm: %d, VotedFor: %d, CommitIndex: %d, LastApplied: %d, GetVotesNum: %d, NextIndex: %v, MatchIndex: %v, FirstIndex: %v, LastIncludedIndex: %v, LastIncludedTerm: %v, Log: %s}",
					rf.state, rf.currentTerm, rf.votedFor, rf.commitIndex, rf.lastApplied, rf.getVotesNum, rf.nextIndex, rf.matchIndex, rf.firstIndex, rf.lastIncludedIndex, rf.lastIncludedTerm, getLogString(rf.Log))
				PrettyDebug(dSnap, "S%d state %s", rf.me, stateString)
				if reply.Term > rf.currentTerm {
					rf.FollowerState(reply.Term)
					rf.mu.Unlock()
					return
				}
				if rf.state != LEADER || !ok {
					rf.mu.Unlock()
					return
				}
				if reply.Term == rf.currentTerm {
					if rf.matchIndex[index] < InstallSnapshotArgs.LastIncludedIndex {
						rf.matchIndex[index] = InstallSnapshotArgs.LastIncludedIndex
						rf.nextIndex[index] = rf.matchIndex[index] + 1
					}
				}
				rf.commitIndex = rf.countReplica()
				PrettyDebug(dLog, "S%d finished send installSnapShot, couting replica is %d", rf.me, rf.commitIndex)
				PrettyDebug(dSnap, "S%d finished installSnapShot RPC to S%d %s", rf.me, index, InstallSnapshotArgs.String())
				stateString = fmt.Sprintf("Raft{State: %d,  CurrentTerm: %d, VotedFor: %d, CommitIndex: %d, LastApplied: %d, GetVotesNum: %d, NextIndex: %v, MatchIndex: %v, FirstIndex: %v, LastIncludedIndex: %v, LastIncludedTerm: %v, Log: %s}",
					rf.state, rf.currentTerm, rf.votedFor, rf.commitIndex, rf.lastApplied, rf.getVotesNum, rf.nextIndex, rf.matchIndex, rf.firstIndex, rf.lastIncludedIndex, rf.lastIncludedTerm, getLogString(rf.Log))
				PrettyDebug(dSnap, "S%d state %s", rf.me, stateString)
				rf.mu.Unlock()
				return // return here
			}
			args := &AppendEntriesArgs{
				Term:         rf.currentTerm,
				LeaderId:     rf.me,
				PrevLogIndex: rf.nextIndex[index] - 1,
				PrevLogTerm:  rf.Log[rf.getRealLogIndex(rf.nextIndex[index]-1)].Term,
				Entries:      nil,
				LeaderCommit: rf.commitIndex,
			}

			//
			// args.PrevLogTerm = rf.Log[args.PrevLogIndex].Term
			// If last log index ≥ nextIndex for a follower:
			// send AppendEntries RPC with log entries starting at nextIndex

			if rf.getLastLogIndex() >= rf.nextIndex[index] {
				entries := rf.Log[rf.getRealLogIndex(rf.nextIndex[index]):]
				logCopy := make([]LogEntry, len(entries))
				copy(logCopy, entries)
				args.Entries = logCopy
			}

			PrettyDebug(dLog, "S%d send AppendEntries to S%d : %s, Log%s", rf.me, index, args.String(), getLogString(rf.Log))

			// set Leader's nextIndex and matchIndex
			rf.nextIndex[rf.me] = rf.getLastLogIndex() + 1
			rf.matchIndex[rf.me] = rf.getLastLogIndex()

			reply := &AppendEntriesReply{}
			/*
				sendAppendSuccess := false
				for !rf.killed() && !sendAppendSuccess && rf.state == LEADER && rf.currentTerm == args.Term {
					if rf.state != LEADER || rf.currentTerm != args.Term {
						rf.mu.Unlock()
						return
					}
					rf.mu.Unlock()
					ok := rf.sendAppendEntries(index, args, reply)
					sendAppendSuccess = ok
					if !ok {
						PrettyDebug(dLog2, "S%d send AppendEntries error", args.LeaderId)
						time.Sleep(2e8)
					}
					rf.mu.Lock()
				}
			*/
			PrettyDebug(dLog, "S%d send AppendEntries success", args.LeaderId)

			if rf.state != LEADER || rf.currentTerm != args.Term {
				rf.mu.Unlock()
				return
			}
			rf.mu.Unlock()

			ok := rf.sendAppendEntries(index, args, reply)
			if !ok {
				PrettyDebug(dLog, "S%d send AppendEntries error", args.LeaderId)
			}
			rf.mu.Lock()

			// If RPC request or response contains term T > currentTerm: set currentTerm = T, convert to follower

			if reply.Term > rf.currentTerm {
				rf.FollowerState(reply.Term)
				rf.mu.Unlock()
				return
			}

			if rf.state != LEADER || rf.currentTerm != args.Term {
				rf.mu.Unlock()
				return
			}

			// If successful: update nextIndex and matchIndex for follower (§5.3)
			// If AppendEntries fails because of log inconsistency: decrement nextIndex and retry (§5.3)
			if reply.Success {
				rf.matchIndex[index] = args.PrevLogIndex + len(args.Entries)
				rf.nextIndex[index] = rf.matchIndex[index] + 1
			} else {
				//
				if reply.LogInconsistency {
					if reply.XTerm == -1 {
						rf.nextIndex[index] = reply.XIndex
					} else {
						rf.handleLogInconsistency(index, reply)
					}
				}
			}

			// If there exists an N such that
			// N > commitIndex, a majority of matchIndex[i] ≥ N, and log[N].term == currentTerm:
			// set commitIndex = N (§5.3, §5.4).

			rf.commitIndex = rf.countReplica()
			PrettyDebug(dLog, "S%d finished send AppendEntries, couting replica is %d", rf.me, rf.commitIndex)
			stateString = fmt.Sprintf("Raft{State: %d,  CurrentTerm: %d, VotedFor: %d, CommitIndex: %d, LastApplied: %d, GetVotesNum: %d, NextIndex: %v, MatchIndex: %v, FirstIndex: %v, LastIncludedIndex: %v, LastIncludedTerm: %v, Log: %s}",
				rf.state, rf.currentTerm, rf.votedFor, rf.commitIndex, rf.lastApplied, rf.getVotesNum, rf.nextIndex, rf.matchIndex, rf.firstIndex, rf.lastIncludedIndex, rf.lastIncludedTerm, getLogString(rf.Log))
			PrettyDebug(dSnap, "S%d state %s", rf.me, stateString)
			rf.mu.Unlock()
			// term >= currentTerm
			// If RPC request or response contains term T > currentTerm:
			// Set currentTerm = T, convert to follower

		}(i)
	}

}

//
// handleLogInconsistency handles the case where AppendEntries fails due to log inconsistency
//
// If xTerm is -1, it means that the follower does not have any entry in its log with the same term as the leader.
// The leader should first search its log for conflictTerm.
// If it finds an entry in its log with that term, it should set nextIndex to be the one beyond the index of the last entry in that term in its log.
// If it does not find, it should set nextIndex = conflictIndex.

func (rf *Raft) handleLogInconsistency(index int, reply *AppendEntriesReply) {
	if reply.XTerm == -1 {
		rf.nextIndex[index] = reply.XIndex
	} else {
		target := reply.XIndex
		for i := len(rf.Log) - 1; i > 0; i-- {
			if rf.Log[i].Term == reply.XTerm {
				target = (i + rf.firstIndex) + 1
				break
			}
		}
		rf.nextIndex[index] = target
	}
}

func (rf *Raft) candidateRequestVote() {

	for i := 0; i < len(rf.peers); i++ {
		if i == rf.me {
			continue
		}
		rf.mu.Lock()
		if rf.killed() && rf.state != CANDIDATE {
			return
		}
		rf.mu.Unlock()
		go func(index int) {
			rf.mu.Lock()
			args := &RequestVoteArgs{
				Term:         rf.currentTerm,
				CandidateId:  rf.me,
				LastLogIndex: rf.getLastLogIndex(),
				LastLogTerm:  rf.getLastLogTerm(),
			}

			reply := &RequestVoteReply{}
			requestSuccess := false
			for !rf.killed() && !requestSuccess && rf.state == CANDIDATE && rf.currentTerm == args.Term {
				rf.mu.Unlock()
				ok := rf.sendRequestVote(index, args, reply)
				requestSuccess = ok
				if !requestSuccess {
					PrettyDebug(dVote, "S%d RequestVote error", args.CandidateId)
					time.Sleep(2e8)
				}
				rf.mu.Lock()
			}

			// If RPC request or response contains termT > currentTerm: set currentTerm = T, convert to follower
			if reply.Term > rf.currentTerm {
				rf.FollowerState(reply.Term)
				rf.mu.Unlock()
				return
			}

			if rf.state != CANDIDATE || rf.currentTerm != args.Term {
				rf.mu.Unlock()
				return
			}

			// PrettyDebug(dVote, "S%d currentTerm is %d, receiveTerm is %d", rf.me, rf.currentTerm, reply.Term)

			if reply.Term == rf.currentTerm && rf.state == CANDIDATE {
				if reply.VoteGranted {
					rf.getVotesNum += 1
					if rf.getVotesNum > len(rf.peers)/2 {
						rf.LeaderState()
						PrettyDebug(dVote, "S%d becomes LEADER, has votes %d", rf.me, rf.getVotesNum)
					}
				}
			}
			rf.mu.Unlock()
		}(i)
	}
}

//
// the service using Raft (e.g. a k/v server) wants to start agreement on the next command to be appended to Raft's log.
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
		return -1, -1, false
	}
	entry := LogEntry{Term: term, Command: command, Index: rf.firstIndex + len(rf.Log)} // Index is the index of the log entry in the log
	rf.Log = append(rf.Log, entry)

	rf.persist()

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
			time.Sleep(5e7)
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
			//PrettyDebug(dVote, "S%d tries to Append Entries, State %s", rf.me, rf.String())
			rf.mu.Unlock()

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
	randNum := int64(1000 + rand.Intn(500))
	var randTime int64
	randTime = randNum * 1000000 // 1- 1.5s
	rf.mu.Lock()
	rf.lastTimeHeared = time.Now()
	rf.mu.Unlock()
	PeriodicTimeout := make(chan bool)
	go func() {
		for {
			time.Sleep(3e7)
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

			randNum = int64(1000 + rand.Intn(500)) //1-1.5s
			randTime = randNum * 1000000

			currentTime = time.Now() // add this line

			// reset timer
			rf.lastTimeHeared = currentTime
			PrettyDebug(dVote, "S%d request for vote", rf.me)

			rf.mu.Unlock()
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
			//time.Sleep(1e8)
			time.Sleep(2e7)
			ApplyTimeout <- true
		}
	}()
	for rf.killed() == false {
		if <-ApplyTimeout {
			rf.mu.Lock()
			commmitIndex := rf.commitIndex
			LastLogIndex := rf.getLastLogIndex()
			// If commitIndex > lastApplied: increment lastApplied, apply log[lastApplied] to state machine (§5.3)
			if commmitIndex > rf.lastApplied {
				for (rf.lastApplied < commmitIndex) && (rf.lastApplied < LastLogIndex) {
					PrettyDebug(dLog, "S%d Apply Entries at %d, commitIndex is %d, Log is %s ", rf.me, rf.lastApplied+1, rf.commitIndex, getLogString(rf.Log))
					rf.lastApplied++
					applyMsg := ApplyMsg{
						CommandValid: true,
						Command:      rf.Log[rf.getRealLogIndex(rf.lastApplied)].Command,
						CommandIndex: rf.lastApplied,
					}
					rf.mu.Unlock()
					rf.applyCh <- applyMsg
					rf.mu.Lock()
				}
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

	// Lab 2D
	rf.lastIncludedIndex = -1
	rf.lastIncludedTerm = -1
	rf.snapShot = nil
	rf.firstIndex = 0
	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	// start ticker goroutine to start elections
	go rf.ticker()
	go rf.heartBeat()
	go rf.applyChecker()

	return rf
}
