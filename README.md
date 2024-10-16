# MIT 6.824 
[MIT 6.824](http://nil.csail.mit.edu/6.824/2021/) is a graduate-level distributed systems course offered by MIT. This course deeply explores the design and implementation principles of distributed systems, covering topics such as fault tolerance mechanisms, consensus algorithms, data replication, consistency models, distributed storage systems, and MapReduce. Through lectures, paper readings, and programming assignments, the content mainly includes how to build high-performance, scalable, and reliable distributed systems, cultivating theoretical knowledge and practical skills in the field of distributed systems.

Here is [CN version of my note](https://github.com/RayZhang3/Sharded-KV/blob/master/README_CN.md)

# ShardKV

ShardKV is an implementation of multi-raft, where each raft node corresponds to a state machine. Multiple raft nodes form a raft group, and multiple raft groups along with a configuration management server provide services. However, the concept of a physical node is missing here. In actual production systems, members of different raft groups may exist on the same physical node. A physical node owns a state machine, and different raft groups operate on the same state machine using different namespaces.

### Features

**Internal:**

- Load balancing (makes shards evenly distributed during service, but does not consider requests)
- A certain degree of fault tolerance
- Dynamic migration of shard data
- **Independent shard services**: Shards can be migrated independently without affecting read and write requests of shards not being migrated. For example, if a raft group needs to pull data from two groups, and one raft group crashes or is restarting, it does not affect data pulling from the other group.

**External:**

- Provides key-value services with [linearizability properties](https://hongilkwon.medium.com/what-is-linearizability-in-distributed-system-db8bca3d432d#:~:text=Linearizability%20is%20the%20strongest%20consistency,operations%20on%20it%20are%20atomic)


The system operates as follows: Initially, a `shardctrler` is created to handle configuration updates, shard allocation, and other tasks. Then, multiple raft groups are created to handle the read and write tasks of shards. Situations such as raft group addition or deletion, raft node crashes, raft node restarts, and network partitions may occur.

### Implementation

1. **Configuration Change Process**: Requires updating configurations and moving shards, which can be decomposed into pulling shards and confirming receipt of shards. Operations like `PUT` and `APPEND` in the key-value service involve cluster shard states and data operations. To ensure consistency within the raft group, the leader in the group needs to commit these through the raft log.

2. **Asynchronous Configuration Changes**: Implements independent shard services where a single shard can immediately provide services after transfer completion. Groups not undergoing shard migration can continue to provide services normally, offering better performance than synchronous configuration changes.

   - **Synchronous Configuration Changes**: To ensure that shard data remains unchanged during configuration updates, the original raft group needs to block the applier after obtaining the new configuration and only provide services after all shard transfers are completed.
   - **Asynchronous Configuration Changes**: Only the shard state should be changed, and the remaining work is completed by asynchronous shard transfer goroutines.

3. **Configuration Update Process**: The leader of the raft group confirms its own log state. Four log states are set: `SERVING`, `NO_SERVING`, `PULLING` during shard migration, and `BEPULLED`. To prevent shard states from being overwritten, the next configuration is pulled and shard states are updated only after the previous round of log migration is complete.

4. **Shard Transfer Process**: Two sets of RPCs are designed:

   - One responsible for requesting the corresponding shards
   - One for confirming whether the receiver has received the shards

   To achieve idempotent operations, the call parameters need to include the configuration version number. These two goroutines are awakened when the raft node is in the leader state. When detecting that a shard is in the `PULLING` state, it sends a request to the previous raft group that held the shard. The shard transfer process is essentially a state transition; the state includes not only the data of the corresponding shard but also the client sequence number mapping used for deduplication. Since it's impossible to determine which client the shard data corresponds to, in my implementation, the shard data and the map storing client IDs and sequence numbers are sent to the receiver. The receiver checks the configuration version number and shard state, receives the shard data, updates the client sequence numbers, and changes the corresponding shard state to `Serving`.

5. **Shard Receipt Confirmation Process**: When detecting that a shard is in the `BEPULLED` state, it sends a request to the raft group that should hold the shard under this configuration. The receiver confirms in two situations:

   - Its own configuration number is greater than the call parameter
   - The configuration numbers are equal, and the corresponding shard has already started processing data

   *(The situation where the receiver's configuration number is greater than the parameter occurs because, after receiving the shard, it has already started providing services, and after receiving all shards, it may start a new round of configuration updates.)*

6. **Persistence**: Raft and KVServer introduce a snapshot mechanism. When the log occupies too much space, the current state of KVServer and the raft layer's logs, along with some state variables, are encoded and saved to disk. Upon recovery, they are decoded. The state information of KVServer includes:

   - The key-value data of shards
   - Client request sequence numbers used for deduplication
   - Shard states
   - Previous and current configurations (necessary for shard transfer and confirmation)

## Shardctrler

**Concept**: A highly available cluster configuration management service that records the replicas corresponding to each raft group, including the endpoints of the nodes, and which shard is allocated to which raft group.

**Features**:

- Achieves basic load balancing based on shards
- Allows users to manually or automatically delete raft groups using built-in strategies, making more efficient use of cluster resources
- Each client request can be routed to the correct data node through `shardctrler`, similar to the role of the HDFS master

**Optimization**: Clients cache configurations—for example, saving the mapping of shards to leader servers. When a request error occurs, upon `ErrorWrongLeader`, they access other servers in the cluster. After a certain number of attempts, they retrieve the configuration again. Upon `ErrWrongGroup`, they retrieve the configuration again.

### Implementation

There are mainly four APIs: `Join`, `Leave`, `Query`, and `Move`. `Query` can get the configuration of a specified number, and `Move` assigns a shard to a specified group. Since shards remain unchanged during `Join` and `Leave`, rebalancing is needed after moving. My implementation idea is:

- First, traverse all shards to collect them into `freeShards`.
- Then enter a loop, sequentially finding the group with the most shards and the group with the least shards.
- Preferentially assign `freeShards` to the group with the least shards.
- If there are no `freeShards`, assign one shard managed by the group with the most shards to the group with the least shards.
- Loop until the maximum and minimum difference is less than or equal to 1.

One point to note here is that since `shardctrler` stores `GroupID -> servers` (a mapping from cluster IDs to the endpoints of servers in the cluster), it needs to traverse `GroupID`. According to the principle of replicated state machines, each replica is an implementation of a state machine, and they are deterministic state machines; given the same logs, the same results should be obtained on different replicas. Since the order of traversal of maps in Go is non-deterministic, and `shardctrler` itself is implemented based on Raft, it needs to ensure that after the leader and follower execute the same logs, they get the same state. Therefore, a deterministic traversal order is needed. My approach is to first traverse all `GroupID` to form a sorted order, and in this load balancing operation, use this sorted order to access different `GID` and move shards.

   
# Lab 4 (ShardKV)

## References

Architecture: https://github.com/OneSizeFitsQuorum/MIT6.824-2021/blob/master/docs/lab4.md

Optimization (mainly Lease Read): https://ray-eldath.me/programming/deep-dive-in-6824/

## Optimization

1. **Optimizing Read-Only Requests**. Currently, reads are implemented via the Raft log, which is the required implementation for the course Lab. There are several ways to optimize this:

   1. **ReadIndex**: First, record the current `ReadIndex`, send a heartbeat to confirm itself as the leader (reducing the overhead of log replication), and wait until `lastApplied > readIndex` before returning the result. This changes the overhead of log replication to sending heartbeats. When the leader is newly elected, it needs to send a No-Op to commit all previous logs.

   2. **Lease Read**: Before sending a heartbeat, the leader records the time. After sending a heartbeat and confirming that it has received responses from the majority of followers (i.e., the majority of nodes will not elect a new leader), it extends its lease. During the lease period, there is no need to send heartbeats again to confirm its leadership; after the lease expires, it automatically becomes a follower. This method has the best performance and saves the process of sending heartbeats. However, this scheme requires high clock accuracy and may lead to the situation of having two leaders.

      The Raft layer of ETCD is based on ReadIndex but can switch to Lease Read.

      **Commonality between Lease Read and ReadIndex**: When the leader receives a read request, it is at the current commit index. Once the leader ensures that the majority of nodes have reached this commit index, it can return data for this read request.

2. **Why does ReadIndex need to wait until the local `lastApplied >= readIndex`?** It would break linearizability.

3. **Why does the newly elected leader need to write a No-Op in ReadIndex? (Leader apply)**

   **Summary**: The leader applies log entries and replies to the client's request, but the `commitIndex` has not been sent to followers in time. Followers do not know that the leader has committed, nor do they know the progress of the current `commitIndex`, so they may return stale data.

# Lab 3

## Goals

Lab 3 requires implementing the interaction between the Clerk machine and the service/state machine replica. The clerk and server interact via RPCs, including `get`, `put`, and `append`.

The service provides strong consistency for these clerk commands. For concurrent requests, the return values and system state should be consistent with some sequential execution in a specific order, and RPC calls should be able to see the effects of all previous RPC calls.

**Code Components**:

- In `client.go`, implement `Put`/`Append`/`Get` send functions.
- In `server.go`, implement handlers. Handlers call `Start` to add `op` to the Raft log. It's best to lock `Start` here.
- The `kvserver` waits for Raft to reach consensus; during this period, it needs to keep reading `applyCh`.
- Handlers continue to add logs to Raft.
- When Raft sends an `Op` command through `applyCh`, the server executes the command and returns RPC.
- When the server is not part of the majority, it should not return the `Put` command. The solution is to also put the command into the log.

At this point, we can continue to modify the code to handle network and server failures. The clerk may send RPCs multiple times; `Put` and `Append` should be idempotent, which can be achieved using sequence numbers. We also need to add code to handle failures and multiple retransmissions of requests by the clerk. If the clerk fails to send the first time and times out, it should randomly select a server to send the request.

When the leader calls `Start` to add a log, but before the log is committed, its state changes and it is no longer the leader. At this point, it should inform the clerk to resend the request to the new leader. **Implementation**: Check whether the term has changed, or whether there are new logs with other terms in the log. If the previous leader is network partitioned, there is no information about the new leader, but clients in the same partition cannot find the new leader either.

We may need to modify the clerk to save the leader from the last RPC return and send service requests to that leader, which can reduce the time to find the leader.

We need to ensure that the client's operation is executed only once.

Duplicate detection should quickly release server memory; notify the client via RPC that all previous RPCs have been received.

#### Implementation 
1. modify client.go, server.go, common.go
2. Each of key/value server have an associated Raftpeer. 
3. Clerks send Put(), Append(), Get() RPC to the kvserver whose associated Raft is the peer. 
4. The kvserver code submits the operation to raft, so raft log holds a sequence of Put/Append/Get operations. 
   All of the kvserver execute operations from the Raft log in order, applying the operations to their KV database.
5. If Clerk sends an RPC to wrong kvserver, or if it cannot reach the kvServer, it should retry by sending to different KVserver.
6. If KVservice commits the operation to its Raft Log (and hence applies the operation to the key/value state machine) the leader reports the result to the Clerk by responding to its RPC.
7. If the operation failed to commit (for example, if the leader was replaced), the server reports an error, and the Clerk retries with a different server.

### Lab3A Task
1. implement a solution that works when there are no dropped messages, and no failed servers.
2. need to add **RPC-sending code** to the Clerk Put/Append/Get methods in client.go, and **implement PutAppend() and Get() RPC handlers** in server.go. These handlers should enter an Op in the Raft log using Start(); 
3. you should fill in the Op struct definition in server.go so that it describes a Put/Append/Get operation. 
4. Each server should execute Op commands as Raft commits them, i.e. as they appear on the applyCh. An RPC handler should notice when Raft commits its Op, and then reply to the RPC. You have completed this task when you reliably pass the first test in the test suite: "One client".
5. After calling Start(), your kvservers will need to **wait for Raft** to complete agreement. Commands that have been agreed upon arrive on the applyCh. Your code will need to **keep reading applyCh while PutAppend() and Get() handlers submit commands to the Raft log using Start()**. Beware of deadlock between the kvserver and its Raft library.
6. You are allowed to add fields to the Raft ApplyMsg, and to add fields to Raft RPCs such as AppendEntries, however this should not be necessary for most implementations.
7. **A kvserver should not complete a Get() RPC if it is not part of a majority** (so that it does not serve stale data). A simple solution is to enter every Get() (as well as each Put() and Append()) in the Raft log. 
8. You don't have to implement the optimization for read-only operations that is described in Section 8.
9. It's best to **add locking from the start** because the need to avoid deadlocks sometimes affects overall code design. Check that your code is race-free using go test -race.
10. Now you should modify your solution to continue in the face of network and server failures. One problem you'll face is that a Clerk may have to send an RPC multiple times until it finds a kvserver that replies positively. If a leader fails just after committing an entry to the Raft log, the Clerk may not receive a reply, and thus may re-send the request to another leader. Each call to Clerk.Put() or Clerk.Append() should result in just a single execution, so you will have to ensure that the re-send doesn't result in the servers executing the request twice.
11. Add code to handle failures, and to cope with duplicate Clerk requests, including situations where the Clerk sends a request to a kvserver leader in one term, times out waiting for a reply, and re-sends the request to a new leader in another term. The request should execute just once. Your code should pass the go test -run 3A -race tests.
12. Your solution needs to handle **a leader that has called Start() for a Clerk's RPC, but loses its leadership before the request is committed to the log.** In this case you should arrange for the Clerk to re-send the request to other servers until it finds the new leader. One way to do this is for the server to detect that it has lost leadership, by noticing that a different request has appeared at the index returned by Start(), or that Raft's term has changed. If the ex-leader is partitioned by itself, it won't know about new leaders; but any client in the same partition won't be able to talk to a new leader either, so it's OK in this case for the server and client to wait indefinitely until the partition heals.
13. You will probably have to modify your **Clerk** to **remember which server turned out to be the leader for the last RPC, and send the next RPC to that server first.** This will avoid wasting time searching for the leader on every RPC, which may help you pass some of the tests quickly enough.
14. You will need to **uniquely identify client operations** to ensure that the key/value service **executes each one just once.**
15. Your scheme for **duplicate detection** should **free server memory quickly**, for example by having each RPC imply that the client has seen the reply for its previous RPC. It's OK to assume that a client will make only one call into a Clerk at a time.

## Lab3B 
### Goal
The tester passes maxraftstate to your StartKVServer(). 
**maxraftstate** indicates the maximum allowed size of your persistent Raft state in bytes (including the log, but not including snapshots). You should compare maxraftstate to persister.RaftStateSize(). Whenever your key/value server detects that the Raft state size is approaching this threshold, it should save a snapshot using Snapshot, which in turn uses persister.SaveRaftState(). If maxraftstate is -1, you do not have to snapshot. maxraftstate applies to the GOB-encoded bytes your Raft passes to persister.SaveRaftState().

#### Hint

1. Think about when a `kvserver` should snapshot its state and what should be included in the snapshot. Raft stores each snapshot in the persister object using `SaveStateAndSnapshot()`, along with corresponding Raft state. You can read the latest stored snapshot using `ReadSnapshot()`.
2. Your `kvserver` must be able to detect duplicated operations in the log across checkpoints, so any state you are using to detect them must be included in the snapshots.
3. Capitalize all fields of structures stored in the snapshot.
4. You may have bugs in your Raft library that this lab exposes. If you make changes to your Raft implementation, make sure it continues to pass all of the Lab 2 tests.
5. A reasonable amount of time to take for the Lab 3 tests is 400 seconds of real time and 700 seconds of CPU time. Further, `go test -run TestSnapshotSize` should take less than 20 seconds of real time.

### Implementation

1. **Snapshot Trigger Monitoring**: After the `ApplyHandler` operation, compare `maxraftstate` with `persister.RaftStateSize()`. When `maxraftstate <= persister.RaftStateSize()`, prepare the snapshot. You need to confirm the index corresponding to the snapshot, so you need to modify `applyHandler` to include an index parameter to call `Snapshot`.

2. **Modifications to `applyHandler`**:
   1. **Receiving a Snapshot**: In the case where `SnapshotValid` is true in `applyMsg`, apply the snapshot unconditionally and then update `lastAppliedIndex`.
   2. **Recording the Service State Index**: Each time an operation is applied, record `lastAppliedIndex`. You should use a lock when generating a snapshot. If you receive a request before `lastAppliedIndex`, do not make any changes and discard the request.

3. **Question**: Does `lastAppliedIndex` need to be persisted? Since `ClientSeq` already stores the client session, `lastAppliedIndex` is not needed in `Get` and `PutAppend`. It is only used in `applyHandler` to determine whether to apply an operation. Raft may commit some log entries before `lastAppliedIndex`; we need to use `lastAppliedIndex` to filter out these entries. Therefore, in my implementation, I persist `lastAppliedIndex` along with the snapshot.

4. **Serialization and Deserialization of Snapshots**: Same as in Raft.

5. **Why Do We Need to Store `lastAppliedIndex`**: Requests already included in the snapshot should not be executed again.

6. **What Functionality Does the Raft Node Implement?** After generating a snapshot, the Raft node will not commit `LogEntry` entries before `lastAppliedIndex`.

7. **Why Are Log Entries Already in the Snapshot Still Being Committed?** Because both snapshots and log entries use `rf.applyCh` to pass upwards. `LogEntry` entries are applied through a goroutine that periodically checks whether there are logs between `commitIndex` and `lastApplied` that need to be applied. Snapshots are received and passed upwards through `InstallSnapshot`. We cannot control the order in which they are passed, so the upper-layer service needs to determine whether to apply them. *(Only after the snapshot is applied do `lastIncludedTerm` and `lastIncludedIndex` in Raft get updated...)*

8. **After Server Crash and Restart**: `lastIncludedTerm` and `lastIncludedIndex` still exist in the Raft peer, so they will not commit logs before these indices.

---

# Lab 2 Raft

Paper: [In Search of an Understandable Consensus Algorithm (Extended Version)](https://raft.github.io/raft.pdf)


Diagram of Raft Structure: https://pdos.csail.mit.edu/6.824/notes/raft_diagram.pdf

6.824 Lab Guidance: [Guidance](https://pdos.csail.mit.edu/6.824/labs/guidance.html)

6.824 Debugging by Printing: [Debug Guide](https://blog.josejg.com/debugging-pretty/)

Raft Lab Guide: https://thesquareplanet.com/blog/students-guide-to-raft/

Raft Q&A: https://thesquareplanet.com/blog/raft-qa/

Raft-Locking: https://pdos.csail.mit.edu/6.824/labs/raft-locking.txt

Raft-Structure: https://pdos.csail.mit.edu/6.824/labs/raft-structure.txt

## Lab 2A

### Implementation

1. **Election Restriction**

   [GitBook 7.2 Election Restriction(CN)](https://mit-public-courses-cn-translatio.gitbook.io/mit6-824/lecture-07-raft2/7.2-xuan-ju-yue-shu-election-restriction)

   A node can only vote for a candidate who meets one of the following conditions, based on the logs and regardless of the candidate's term:

   - The candidate's last log entry's term is greater than the term of the voter's last log entry;
   - Or, the candidate's last log entry's term is equal to the term of the voter's last log entry, and the candidate's log is at least as long as the voter's log.

2. **Log Backup**

   [GitBook 7.1 Log Recovery(CN)](https://mit-public-courses-cn-translatio.gitbook.io/mit6-824/lecture-07-raft2/7.1)

   The leader always has the complete log.

   The `AppendEntries` RPC that the leader sends to the follower includes `prevLogIndex` and `prevLogTerm`, which provide information about the log entry immediately preceding the new entries. The follower will only accept the new entries if both match. If they match and the new entry position already contains logs of other terms, those logs and all subsequent logs will be replaced. [Conflict](obsidian://booknote?type=annotation&book=MIT%206.824/raft-extended.pdf&id=df293ef7-f1ae-4f12-96a4-17ee0318836b&page=4&rect=110.316,173.242,191.385,181.355)

   If they do not match, the leader will decrement the corresponding `nextIndex[]` by 1. In the new RPC, `prevLogIndex` and `prevLogTerm` will be updated backward, including all entries after the previous `prevLogIndex`.

3. **Fast Backup**

   [GitBook 7.3 Backup Acceleration(CN)](https://mit-public-courses-cn-translatio.gitbook.io/mit6-824/lecture-07-raft2/7.3-hui-fu-jia-su-backup-acceleration)

   For the mismatch situation described above, the follower can return enough information to the leader so that the leader can backtrack by term units instead of one log entry at a time. This way, the leader only needs to send one `AppendEntries` for each different term, rather than one for each different log entry.

   **Implementation**: The leader searches backward from the current log until the term changes, effectively skipping all entries within the same term in one go.

   **Implementation Method in the Course**: The follower returns relevant information upon failure.

   **Follower**:

   - **XTerm**: (The term of the log at the `prevLogIndex` in the follower's log) This is the term of the conflicting log in the follower that conflicts with the leader. Previously (Section 7.1), it was mentioned that the leader includes the term of the previous log in `prevLogTerm`. If the follower's term at the corresponding position does not match, it will reject the leader's `AppendEntries` message and put its own term into `XTerm`. If the follower does not have a log at the corresponding position, it returns `-1` here.

   - **XIndex**: (If `prevLogIndex` exists, the index of the first log of the same term) This is the index of the first log entry in the follower with term equal to `XTerm`.

   - **XLen**: (If `prevLogIndex` does not exist) If the follower does not have a log at the corresponding position, `XTerm` will return `-1`. `XLen` indicates the number of empty log slots (including the log at `prevLogIndex`, but not including the new log the leader wants to synchronize).

## Lab 2B
### Implementation
1. Your first goal should be to pass TestBasicAgree2B(). Start by implementing Start(), 
2. then write the code to send and receive new log entries via AppendEntries RPCs, following Figure 2.
3. You will need to implement the election restriction(section 5.4.1 in the paper)
4. One way to fail to reach agreement in the early Lab 2B tests is to hold repeated elections even though the leader is alive. Look for bugs in election timer management, or not sending out heartbeats immediately after winning an election.
5. Your code may have loops that repeatedly check for certain events. Don't have these loops execute continuously without pausing, since that will slow your implementation enough that it fails tests. Use Go's condition variables, or insert a time.Sleep(10 * time.Millisecond) in each loop iteration. [cond](https://golang.org/pkg/sync/#Cond)
6. If you fail a test, look over the code for the test in config.go and test_test.go to get a better understanding what the test is testing. config.go also illustrates how the tester uses the Raft API.

## Lab 2B

### Implementation

1. Your first goal should be to pass `TestBasicAgree2B()`. Start by implementing `Start()`.

2. Then write the code to send and receive new log entries via `AppendEntries` RPCs, following Figure 2.

3. You will need to implement the election restriction (section 5.4.1 in the paper).

4. One way to fail to reach agreement in the early Lab 2B tests is to hold repeated elections even though the leader is alive. Look for bugs in election timer management or not sending out heartbeats immediately after winning an election.

5. Your code may have loops that repeatedly check for certain events. Don't have these loops execute continuously without pausing, as that will slow your implementation enough that it fails tests. Use Go's condition variables, or insert a `time.Sleep(10 * time.Millisecond)` in each loop iteration. [cond](https://golang.org/pkg/sync/#Cond)

6. If you fail a test, look over the code for the test in `config.go` and `test_test.go` to get a better understanding of what the test is testing. `config.go` also illustrates how the tester uses the Raft API.

### Fast Backup

#### Summary

1. Raft's log replication is not completely reliable. When the leader sends log entries during its term, failures or partial failures may occur, causing the follower not to receive them.

2. Raft's logs ensure that the leader possesses all committed logs.

3. A candidate only needs to have logs that are at least as up-to-date as those of other nodes.

#### Ensuring the Leader Does Not Modify Already Committed Parts

1. The follower sends the next index after all committed logs and the term corresponding to that log.

2. This term must exist in the leader; it will set `nextIndex` to the next log entry after all logs of that term in the leader.

3. When the leader sends logs to the follower next time, `PrevLogIndex` and `PrevLogTerm` will be set to `XIndex - 1` and the term of the corresponding log entry, respectively.

4. The above step effectively ensures that `PrevLogIndex >= commitIndex`; that is, if the leader has more logs with the same term, `PrevLogIndex` will increase.

5. According to the above rules, `PrevLogIndex` continues to decrease until it finds a term that matches the follower's, or reaches the `commitIndex`.

#### When the Follower's Log Length Is Insufficient, First Set `nextIndex` to the Follower's Log Length, Then Skip One Term at a Time

1. The leader sends `prevLogIndex` and `prevLogTerm`.

2. If there is a log term conflict, the follower returns the term and the log index of the first log of that term.

3. If the leader also has this term, set `nextIndex` to the next log entry after all logs of that term in the leader.

   - This situation usually occurs when a new leader is elected, and the new leader tries to replicate logs to a follower. At this time, the new leader does not have some logs from the old leader.
   - Generally, this is because the old leader has sent some logs to the follower, causing inconsistency between the two.
   - Or the old leader generated some logs but hadn't replicated them to other followers before being replaced by the new leader and becoming a follower. In this case, the new leader should send all the logs it has for the corresponding term to the follower. For logs it doesn't have, they need to be overwritten. Therefore, `nextIndex` is set to the next log entry after all log entries of that term in the leader's log.
   - The next time the new leader sends logs, it will overwrite all logs after that.

4. If the leader does not have this term and knows that the log at `prevLogIndex` does not match, set `nextIndex` to `XIndex`.

   - Since `XIndex` returned by the follower corresponds to the log index of the first log of the conflicting term, this skips an entire term in the follower. Next time, `PrevLogIndex` will point to the log of the previous term.
   - This usually occurs in network partition situations. After the network partition recovers, followers in the isolated partition will receive log information from the old leader in the isolated partition, which does not exist in the new leader.

## Lab 2C

### Implementation

1. A real implementation would write Raft's persistent state to disk each time it changed and would read the state from disk when restarting after a reboot. Your implementation won't use the disk; instead, it will save and restore persistent state from a `Persister` object (see `persister.go`).

2. Whoever calls `Raft.Make()` supplies a `Persister` that initially holds Raft's most recently persisted state (if any).

3. Raft should initialize its state from that `Persister`, and should use it to save its persistent state each time the state changes. **Use** the `Persister`'s `ReadRaftState()` and `SaveRaftState()` methods.

4. **Task**: Complete the functions `persist()` and `readPersist()` in `raft.go` by adding code to save and restore persistent state. **You will need to encode (or "serialize") the state as an array of bytes in order to pass it to the `Persister`.** Use the `labgob` encoder; **see the comments** in `persist()` and `readPersist()`. `labgob` is like Go's `gob` encoder but prints error messages if you try to encode structures with lower-case field names.

5. **Task**: Insert calls to `persist()` at the points where your implementation changes persistent state. Once you've done this, you should pass the remaining tests.

   - `currentTerm`
   - `votedFor`
   - `log[]`

### Tips

1. **LeaderAppendEntries()** & **CandidateRequestVote()**

   - Note that before and after each RPC, you need to check the current `rf.state`, `rf.currentTerm`, and `rf.killed()` to avoid sending outdated RPCs. The checks after the RPC need to be placed after term checks.

2. In the Lab 2C tests, there are situations where votes are lost, especially when the network is partitioned into `N+1` and `N` parts, making it difficult to elect a leader. Therefore, I adopted a retransmission mechanism and set the sending interval to 0.2s, so that within a timeout interval, 5-6 attempts can be made to avoid failing to elect a leader. The retransmission mechanism is limited to the current term; as the term ends, the goroutine naturally terminates.

   - (The timeout is set to 1-1.5s, `TimeOutChecker` interval is 0.03s, `ApplyChecker` interval is 0.02s). To improve performance, you need to add `time.Sleep(10 * time.Millisecond)` in loops that run continuously.
   - Since the leader sends heartbeats at intervals of 0.05s, there's no need to set a retransmission mechanism for `LeaderAppendEntries()`.

3. When a candidate receives an `AppendEntries` from a leader with the same term, it needs to convert to a follower.

4. If `AppendEntries.PrevLogIndex < rf.commitIndex`, the leader cannot modify logs that have already been committed in the follower. Therefore, you need to set `reply.Success` and `reply.LogInconsistency` appropriately.

5. In `ApplyChecker`, apply as many `LogEntry` entries as possible that are currently committed. When writing to `applyCh`, you need to release the lock.

6. When too many tests are run in parallel, it may trigger `signal: killed` or `exit status 1` exceptions and exit.

## Lab2D
ou'll modify Raft to cooperate to save space: from time to time a service will persistently store a "snapshot" of its current state, and Raft will discard log entries that precede the snapshot. When a service falls far behind the leader and must catch up, the service first installs a snapshot and then replays log entries from after the point at which the snapshot was created.

To support snapshots, we need an interface between the service and the Raft library. 
To allow for a simple implementation, we decided on the following interface between service and Raft

```
Snapshot(index int, snapshot []byte)
CondInstallSnapshot(lastIncludedTerm int, lastIncludedIndex int, snapshot []byte) bool
```

1. Snapshot(): A service calls **Snapshot()** to communicate the snapshot of its state to Raft. The snapshot **includes all info up to and including index**. This means the corresponding Raft peer no longer needs the log through (and including) index. Your Raft implementation should trim its log as much as possible. You must revise your Raft code to operate while storing only the tail of the log.
2. InstallSnapshot RPC
	Invokes **CondInstallSnapshot** with the snapshot to tell Raft that the service is switching to the passed-in snapshot state, and that **Raft should update its log at the same time**.
   
3. Note that **InstallSnapshot RPCs are sent between Raft peers**, whereas the provided skeleton functions **Snapshot/CondInstallSnapshot** **are used by the service** to communicate to Raft.	
	1. Follower
		1. When a follower receives and handles an InstallSnapshot RPC, it must hand the included snapshot to the service using Raft. 
		2. **The InstallSnapshot handler** can use the applyCh to send the snapshot to the service, by putting the snapshot in ApplyMsg. 
		3. **The service** reads from applyCh, and invokes **CondInstallSnapshot** with the snapshot to tell Raft that the service is switching to the passed-in snapshot state, and that **Raft should update its log at the same time**. (See applierSnap() in config.go to see how the tester service does this)
		4. **CondInstallSnapshot should refuse to install a snapshot if it is an old snapshot** (i.e., if Raft has **processed** entries after the snapshot's lastIncludedTerm/lastIncludedIndex). 
		5. This is because Raft may handle other RPCs and send messages on the applyCh after it handled the InstallSnapshot RPC, and before CondInstallSnapshot was invoked by the service. It is not OK for Raft to go back to an older snapshot, so older snapshots must be refused. When your implementation refuses the snapshot, CondInstallSnapshot should just return false so that the service knows it shouldn't switch to the snapshot.
		6. If the snapshot is recent, then Raft should trim its log, persist the new state, return true, and the service should switch to the snapshot before processing the next message on the applyCh.
		7. CondInstallSnapshot is one way of updating the Raft and service state; other interfaces between service and raft are possible too. This particular design allows your implementation to do the check whether an snapshot must be installed or not in one place and atomically switch both the service and Raft to the snapshot. You are free to implement Raft in a way that CondInstallSnapShot can always return true; if your implementation passes the tests, you receive full credit.	 
	2. Leader
		1. Raft leaders must sometimes tell lagging Raft peers to update their state by installing a snapshot. 
		2. You need to implement InstallSnapshot RPC senders and handlers for installing snapshots when this situation arises. 
		3. This is in contrast to AppendEntries, which sends log entries that are then applied one by one by the service. 

### Ideas and Implementation

**Experimental Version**: The function of `AppendEntries` is to send heartbeats and the logs that the follower needs. The main point is to confirm which part of the logs the follower requires. After several rounds of `AppendEntries` RPC interactions, a matching point is determined, and the Leader copies all the logs after this point to the Follower. After introducing the snapshot mechanism, when the Leader sends `AppendEntries` RPC to the Follower, the Leader optimistically starts searching from its last log entry, assuming that their snapshot information is consistent or the logs overlap. Then the Leader can quickly locate the match point and send the remaining logs.

After introducing the snapshot mechanism, the Leader must have all committed logs. In the case where `prevIndex < Leader.lastIncludedIndex`, the Leader does not have the corresponding log entry and cannot send it to the follower; it can only send a snapshot to the follower first.

Therefore, every time the Leader sends `AppendEntries`, if it detects that the current `leader.prevIndex[follower] < leader.lastIncludedIndex`, it no longer sends `AppendEntries` but sends `InstallSnapshot` instead. The `InstallSnapshot` here needs to reset the election timer.

I feel that this design has flaws. The `InstallSnapshot` RPC is often large and can easily fail to transmit under poor network conditions. Perhaps an `AppendEntries` without entries can be sent at the same time.

When the Follower receives the Snapshot, it first checks the term, resets the election timer, compares and saves the current snapshot, and then trims the log. It needs to find a `LogEntry` that satisfies `LogEntry.Term == Leader.LastIncludedTerm && args.LastIncludedIndex`, replaces and deletes all logs before this log (note to keep the log entry at `Index = 0`). After applying the log, the follower needs to update its `commitIndex` and `lastApplied`, then save the snapshot and state, and send an `applyMsg` through `applyCh` to inform the Server to start applying the snapshot, ensuring that no locks are held.

The Leader needs to update `nextIndex` based on the reply, using `XTerm` and `XIndex` as the return values of `AppendEntries`.

**Implementation of InstallSnapshot**

1. **Check the term**: If the term is greater than its own, convert to Follower and reset votes.
2. **Determine if the Snapshot is up to date** based on the RPC and its own `LastIncludedIndex`. If not accepted, return directly.
3. **Save the Snapshot**.
4. **Find the corresponding log entry** according to the Snapshot's `lastIncludedIndex`, check whether the term of the corresponding log entry matches, and trim the log.
5. **Update** its own `LastIncludedIndex`, `LastIncludedTerm` (and `FirstIndex`, whose value is always equal to `LastIncludedIndex`; this extra variable should be omitted).
6. **Save the service's State and Snapshot**.
7. **Send the Snapshot** to the upper-layer service through `rf.applyCh`.

**Implementation of Leader Sending InstallSnapshot**

1. **Only send one of AppendEntries or InstallSnapshot**: Since `AppendEntries` and `InstallSnapshot` are essentially the Leader synchronizing information to the Follower, I designed it to send only one of them.

   After introducing the snapshot mechanism, because the logs are trimmed, to allow the Leader and Follower to continue matching log entries through `AppendEntries` RPC, the index and term of the last entry in the snapshot need to be saved, i.e., `lastIncludedIndex` and `lastIncludedTerm`, as the `prevLogIndex` and `prevLogTerm` before the first log entry in the follower, participating in the process.

2. **Before sending AppendEntries RPC**, the Leader first checks its own log to determine whether there are log entries to be sent to the Follower. If so, it sends `AppendEntries` RPC; otherwise, it sends `InstallSnapshot` RPC. Log entries with `index` greater than `rf.LastIncludedIndex` all exist.

   In the case without the snapshot mechanism, the `prevLogIndex` and `prevLogTerm` in the `AppendEntries` RPC sent to the follower should be derived from the log at index `rf.nextIndex[followerID] - 1`, that is, the previous log entry of the log at `nextIndex[server]`. Therefore, if the Leader finds when preparing the RPC parameters that `args.prevLogIndex < rf.lastIncludedIndex`, it means that `nextIndex[server] <= rf.lastIncludedIndex`, and the `nextLogEntry` to be sent is included in the snapshot; the follower is too far behind and can only receive `InstallSnapshot`. Otherwise, it means that the log entry at `nextIndex[server]` exists in the Leader's log, and `AppendEntries` is sent.

3. **Indexing in Logs with Snapshots**: Raft peers' logs use 1-based indexing; the log entry at `index = 0` is meaningless. Without log trimming, the array index of the log array `rf.Log` and the actual index are the same. After introducing the snapshot mechanism, when `arrayIndex > 0`, the index corresponding to the log entry `rf.Log[arrayIndex]` is `index = rf.lastIncludedIndex + arrayIndex`. But when we need to get the information of the log entry at `arrayIndex = 0`, we should return `rf.lastIncludedIndex` and `rf.lastIncludedTerm`.

   For example, when the Leader sends `AppendEntries` RPC to the Follower, it needs to set `args.prevLogIndex = rf.nextIndex[server] - 1`, but at this time `Leader.nextIndex[followerID] = Follower.Log[1]`. After the Follower receives the `AppendEntries` RPC, it looks for the `LogEntry` corresponding to `args.PrevLogIndex` and compares `args.PrevLogTerm` with `Entry.Term`. Therefore, the Follower will calculate `arrayIndex = args.prevLogIndex - rf.lastIncludedIndex = 0` and find `rf.Log[0]`. But `rf.Log[0]` is meaningless. At this time, it should return the data of the previous entry of `rf.Log[1]`, i.e., `rf.prevLogIndex` and `rf.prevLogTerm`, to match.

   **Implementation**: In Lab 2B, it's best not to use `rf.Log[index]` to get the log at a specific index. Instead, implement a method `rf.getLogAt(arrayIndex)`, first calculating the array index `arrayIndex` of the corresponding log entry in the log list `rf.Log[]` by `arrayIndex = TargetIndex - rf.lastIncludedIndex`, then use `rf.getLogAt(arrayIndex)` to get information. When `arrayIndex = 0`, return `rf.lastIncludedIndex` and `rf.lastIncludedTerm`.

4. **Compatibility of InstallSnapshot and Fast Backup**

   Before introducing the snapshot mechanism, the method introduced in the course is used to add `XTerm`, `XIndex`, `XLen` information to `AppendEntriesReply`.

   When receiving `AppendEntriesArgs` from the Follower, it contains `args.PrevLogIndex`, `args.PrevLogTerm`.

   1. **Follower**:

      1. **LogEntry at `args.PrevLogIndex` exists**:

         1. If `LogEntry.Term == args.PrevLogTerm`, it's a match.
         2. If `LogEntry.Term != args.PrevLogTerm`, the term does not match. Find the first log entry in `rf.Log[]` that satisfies `entry.Term == LogEntry.Term`, and return its index and term (`XTerm = entry.Term`, `XIndex = entry.Index`). This means the Follower pessimistically believes that the logs of the corresponding term are incomplete and needs the Leader to synchronize from the beginning.

      2. **LogEntry at `args.PrevLogIndex` does not exist**:

         1. Next time, try from the last log entry, return `XTerm = -1`, `XLen = len(Follower.Log)`

   2. **Leader**:

      1. If `XTerm = -1`, set `nextIndex[FollowerID] = XLen`.
      2. If `XTerm != -1`, find the last log entry in `rf.Log[]` that satisfies `entry.Term == XTerm`, set `nextIndex[FollowerID] = entry.Index + 1`. This means the Leader optimistically believes that the logs of the corresponding term exist in the Follower and only needs to send the logs after that term.

   It can be simplified to use only `XTerm` and `XIndex` as parameters. When `XTerm = -1` is returned, the Leader receives the Follower's reply and sets `nextIndex` to `XIndex`.

   1. **Follower**:

      1. **LogEntry at `args.PrevLogIndex` exists**:

         1. If `LogEntry.Term == args.PrevLogTerm`, it's a match.
         2. If `LogEntry.Term != args.PrevLogTerm`, the term does not match. Find the first log entry in `rf.Log[]` that satisfies `entry.Term == LogEntry.Term`, and return its index and term (`XTerm = entry.Term`, `XIndex = entry.Index`). This means the Follower pessimistically believes that the logs of the corresponding term are incomplete and needs the Leader to synchronize from the beginning.

      2. **LogEntry at `args.PrevLogIndex` does not exist**:

         1. Next time, try from the last log entry, return `XTerm = -1`, `XIndex = len(Follower.Log)`

   2. **Leader**:

      1. If `XTerm = -1`, set `nextIndex[FollowerID] = XIndex`.
      2. If `XTerm != -1`, find the last log entry in `rf.Log[]` that satisfies `entry.Term == XTerm`, set `nextIndex[FollowerID] = entry.Index + 1`. This means the Leader optimistically believes that the logs of the corresponding term exist in the Follower and only needs to send the logs after that term.

   **After introducing the snapshot mechanism**, the following situations may occur:

   1. **Follower is too far behind**: `args.prevLogIndex` is greater than the index of the Follower's last log entry. In this case, it cannot match, and starts from the next entry after the last log entry.

   2. **Follower receives outdated RPCs** from a legitimate Leader, trying to match log entries that are already included in the snapshot in the Follower. This situation was not observed in tests.

   **Implementation**

   1. **Follower**:

      1. **Determine if the Leader's matching log entry is beyond the Follower's last log entry**: If `args.PrevLogIndex > lastLogIndex`, return `XTerm = -1`, `XIndex = rf.lastLogIndex() + 1`; otherwise, continue.
      2. **Check if the LogEntry at `args.PrevLogIndex` exists**:

         1. If `LogEntry.Term == args.PrevLogTerm`, it's a match.
         2. If `LogEntry.Term != args.PrevLogTerm`, the term does not match. Find the first log entry in `rf.Log[]` that satisfies `entry.Term == LogEntry.Term`, and return its index and term (`XTerm = entry.Term`, `XIndex = entry.Index`). This means the Follower pessimistically believes that the logs of the corresponding term are incomplete and needs the Leader to synchronize from the beginning.

      3. **LogEntry at `args.PrevLogIndex` does not exist**:

         1. Next time, try from the last log entry, return `XTerm = -1`, `XIndex = rf.lastLogIndex() + 1`

   2. **Leader**:

      1. If `XTerm = -1`, set `nextIndex[FollowerID] = XIndex`.
      2. If `XTerm != -1`, find the last log entry in `rf.Log[]` that satisfies `entry.Term == XTerm`, set `nextIndex[FollowerID] = entry.Index + 1`. This means the Leader optimistically believes that the logs of the corresponding term exist in the Follower and only needs to send the logs after that term.
