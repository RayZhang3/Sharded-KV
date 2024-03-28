### Lab 4
#### 参考 
Architecture: https://github.com/OneSizeFitsQuorum/MIT6.824-2021/blob/master/docs/lab4.md
优化部分(主要是Lease Read): https://ray-eldath.me/programming/deep-dive-in-6824/ 

Lease Read 和ReadIndex的共同点：是Leader接收到读请求的时候，当前的commit Index。Leader在保证大多数节点到达该commit Index后，就可以向这个读请求返回数据了。
Read Index：
#### shardctrler
概念：高可用集群配置管理服务，记录了当前每个raft组对应的副本，其中节点的endpoint, 以及每个shard被分配到了哪个Raft组。
功能：
基于分片实现基本的负载均衡，
用户手动或内置策略自动的方式来删除raft组，更有效地利用集群资源。 
客户端的每个请求都可以通过shardctrler路由到正确的数据节点，类似于HDFS master的角色。
优化：客户端缓存配置，例如，保存对应的shard到leader 服务器的映射，在请求出错时，ErrorWrongLeader时访问集群内的其他服务器，一定次数后重新获取配置，ErrWrongGroup则重新获取配置。
##### 实现
主要有Join、Leave、Query、Move四个api，Query可以获取指定标号的配置，Move则是将Shard分配给指定的Group，Join和Leave，由于shards不变，因此在移动后需要进行rebalance。我的实现思路是：首先遍历所有Shards，收集成freeShards，然后开始进入循环，依次找到shards数量最多的组和shards数量最少的组，优先把freeShards分配给最少的组，如果没有freeShards，就把shards最多的组管理的一个shards分配给shards最少的组，循环直到最大和最小的差值小于等于1。这里需要注意的点是，由于shardctrler存储的是GroupID -> servers（集群号到集群各个服务器终端地址的映射），需要遍历GroupID。根据复制状态机的原理，每个副本都是状态机的实现，而且都是确定状态机，给定相同的日志，在不同的副本上应该得到一样的结果。由于go中遍历map的次序是不确定的，而shardctrler本身也是基于raft实现，需要保证leader和follwer执行相同日志后得到同样的状态，因此需要确定一个遍历顺序，我的做法是，首先遍历所有GroupID，形成一个排序，在这一次负载均衡的操作中均使用这个排序去访问不同的GID并移动分片。

#### ShardKV
shardKV 是multi-raft的实现，一个raft节点对应一个状态机，多个raft节点组成一个raft组，多个raft组和配置管理服务器共同提供服务。但这里缺少了物理节点的概念，在实际的生产系统中，不同raft组的成员可能存在于同一个物理节点上，一个物理节点拥有一个状态机，不同raft组使用不同的命名空间操作同一个状态机。
##### 功能
对内：
	负载均衡（使切片在服务期间均匀分布，但未考虑请求）
	一定程度的容错
	分片数据的动态迁移
	分片独立提供服务: 可以独立地迁移分片，在分片迁移时，不影响未迁移分片的读写请求，比如，raft组需要从两个组拉取数据，其中一个raft组挂了或者正在重启，不会影响另一个组的数据拉取）
对外： 
	提供键值服务，具有[线性一致性的特性](https://zhuanlan.zhihu.com/p/42239873)
系统的运行方式是：一开始创建一个shardctrler负责配置更新、分片分配等任务，紧接着创建多个raft组承载分片的读写任务，可能有raft组增删、raft节点宕机、raft节点重启、网络分区的情况出现。
##### 实现
1. 变更配置的过程：需要更新配置、移动分片，移动分片可以分解为拉取分片和确认收到分片。键值服务中的PUT、APPEND，这些都是涉及到集群分片状态和数据的操作，为保证raft组内的状态一致，都需要组内的Leader通过raft日志的方式提交。 
2. 异步变更配置：实现分片的独立服务，单个分片传输完成后可以立即提供服务，不需要进行分片迁移的分组可以正常提供服务，比同步变更配置的方式性能更好。（同步变更配置：为了保证分片的数据在更新配置期间不变，原raft组在获取到新配置后需要阻塞applier，直到分片全部传输完毕后再提供服务）异步变更配置时，只应该变更分片状态，由异步的分片传输协程完成剩余的工作。
3. 更新配置的过程：raft组的leader确认自身的日志状态，这里设置了四种日志状态，不涉及分片迁移的SERVING, NO_SERVING, 分片迁移状态下的PULLING, BEPULLED。为了防止分片状态被覆盖，仅当上一轮日志迁移已经完成时，再拉取当前配置的下一个配置，并更新分片状态。
4. 分片传输过程：设计了两组RPC，一组负责请求对应的分片，一组用于确认接收方是否接收到分片，为了实现操作的幂等性，调用参数需要附上配置的版本号。这两个协程在raft节点处于leader状态时唤醒。检测到分片处于PULLING状态时，向上一个持有该分片的raft组发送请求。这里的分片传输过程实际上是状态转移，状态不仅包括对应分片的数据，也包括用于去重的客户端序列号映射，这里无法判断分片数据对应的写入客户端，因此我的实现中是发送分片数据、存储客户端和序列号映射的map给接收方，接收方检查配置版本号和分片状态，接收分片数据，并更新客户端序列号，把对应的分片状态变更为Serving。
5. 确认分片接收过程：检测到分片处于BEPULLED的状态时，向此配置下应持有该分片的raft组发送请求，接收方在两种情况下确认，一是自身配置号大于调用参数，二是配置号相等，且对应分片已经开始处理数据。（出现自身配置号大于参数的情况是因为，接收方接收到分片以后，已经开始提供服务，接收所有分片后可能开始新一轮的配置更新）
6. 持久化：raft和kvserver引入了快照机制，在检测到日志占用空间过大时，将保存当前KVServer的状态和raft层的日志及一部分状态变量Encode并保存到磁盘中，恢复时再Decode。KVServer的状态信息包括了分片的键值数据、用于去重的客户端请求序列号、分片状态、此前配置和当前配置（需要依靠配置进行分片传输和确认）。

##### 优化
1. 优化只读请求。目前的read是通过raft log实现的，这是课程Lab要求的实现方式。可以通过以下几种方式优化，
	1. ReadIndex：先记录当前ReadIndex，发送心跳确认自己是leader（减少复制日志的开销），等待lastApplied > readIndex时返回结果。把复制日志的开销变为发送心跳。leader刚当选时需要发送一条No-Op来提交之前的所有log。
	2. LeaseRead：Leader在发送心跳之前记录时间，在发送心跳确认自己得到大多数Follower的回应后(即大多数节点不会竞选成为新的节点)，延长它的租约，在租约期间无需再次发送heartbeat确认自己的leader身份，租约到期后自动变为follower。该方法性能最好，节省了发送心跳的过程。但该方案对时钟的准确性要求高，可能导致出现两个Leader的情况。
   ETCD的raft层基于readIndex，但可以切换至leaseRead
2. 为什么ReadIndex需要等待 本地的lastApplied >= readIndex?  会破坏线性一致性。
3. 为什么在ReadIndex中，刚选出的Leader需要写一条No-Op？(Leader apply) 
概括：Leader apply日志项并且回复了client的请求，但commitIndex还没来得及发送给follower，followers不知道leader已经commit了，不知道当前commitindex的进度，因此可能会返回旧数据。


# Lab3

# Lab 2 Raft
Paper: [In Search of an Understandable Consensus Algorithm(Extended Version)](obsidian://booknote?type=annotation&book=MIT%206.824/raft-extended.pdf&id=854b8c2c-6088-8e6d-244c-d126d6bec6d6&page=1&rect=111.240,678.770,508.139,716.107)
Diagram of Raft Structure: https://pdos.csail.mit.edu/6.824/notes/raft_diagram.pdf
6.824 Lab Guidance: [Guidance](https://pdos.csail.mit.edu/6.824/labs/guidance.html)
6.824 Debugging by printing: [Debug Guide](https://blog.josejg.com/debugging-pretty/)
Raft Lab Guide: https://thesquareplanet.com/blog/students-guide-to-raft/
Raft Q&A: https://thesquareplanet.com/blog/raft-qa/
Raft-Locking: https://pdos.csail.mit.edu/6.824/labs/raft-locking.txt
Raft-Structure: https://pdos.csail.mit.edu/6.824/labs/raft-structure.txt
## Lab 2A
### Implementation
1. Election resrtiction
   [5.4.1 Election restriction](obsidian://booknote?type=annotation&book=MIT%206.824/raft-extended.pdf&id=f9beb2cf-c15a-6e5b-1a19-c02ed5ff2c56&page=8&rect=72.000,182.865,183.499,191.833) 
   [gitbook 7.2 选举约束（Election Restriction）](https://mit-public-courses-cn-translatio.gitbook.io/mit6-824/lecture-07-raft2/7.2-xuan-ju-yue-shu-election-restriction)
	节点只能向满足以下条件之一的候选人投出赞成票：以Log为准，和候选人持有的term无关。
	候选人最后一条Log条目的任期号大于本地最后一条Log条目的任期号；
	或者，候选人最后一条Log条目的任期号等于本地最后一条Log条目的任期号，且候选人的Log记录长度大于等于本地Log记录的长度
2. Log Backup
   [gitbook 7.1 日志恢复](https://mit-public-courses-cn-translatio.gitbook.io/mit6-824/lecture-07-raft2/7.1)
   Leader总是有完整的记录
   Leader发送给Follwer的AppendEntries RPC包含了prevLogIndex和prevLogTerm字样，就是包括了新Entry写入位置的前一个槽位的信息，只有两者都匹配的情况下Follwer才会接收，在匹配的情况下，如果新Entry写入位置已经存有其他任期号的Log，则会该Log和之后的所有Log都会被替换。[Conflit](obsidian://booknote?type=annotation&book=MIT%206.824/raft-extended.pdf&id=df293ef7-f1ae-4f12-96a4-17ee0318836b&page=4&rect=110.316,173.242,191.385,181.355)
	不匹配时，Leader会将自己维护的nextIndex[]中对应的nextIndex - 1，新的RPC中prevLogindex和prevLogTerm将会往前更新，包含了之前prevLogIndex之后的所有条目。
3. Fast Backup
	[gitbook 7.3 快速恢复](https://mit-public-courses-cn-translatio.gitbook.io/mit6-824/lecture-07-raft2/7.3-hui-fu-jia-su-backup-acceleration)
	对于上述不匹配的情况，可以让Follower返回足够的信息给Leader，这样Leader可以以任期（Term）为单位来回退，而不用每次只回退一条Log条目。这样，Leader只需要对每个不同的任期发送一条AppendEntries，而不用对每个不同的Log条目发送一条AppendEntries。
	实现：Leader从当前日志开始往前搜索，直到任期号改变，一次往前搜索相同任期号内的所有日志。
	**课程中的实现方法**：由Follower fail返回相关信息
	Follower：
	XTerm：（与prevLogIndex对应的，Follower对应Index中的日志）这个是Follower中与Leader冲突的Log对应的任期号。在之前（7.1）有介绍Leader会在prevLogTerm中带上本地Log记录中，前一条Log的任期号。如果Follower在对应位置的任期号不匹配，它会拒绝Leader的AppendEntries消息，并将自己的任期号放在XTerm中。如果Follower在对应位置没有Log，那么这里会返回 -1。
	XIndex：（prevLogIndex对应的Log存在，相同term的第一条log）这个是Follower中，对应任期号为XTerm的第一条Log条目的槽位号。
	XLen：（prevLogIndex对应的Log不存在）如果Follower在对应位置没有Log，那么XTerm会返回-1，XLen表示空白的Log槽位数（包括PrevLogIndex对应的Log，不包括Leader想同步的最新Log）

## Lab 2B
### Implementation
1. Your first goal should be to pass TestBasicAgree2B(). Start by implementing Start(), 
2. then write the code to send and receive new log entries via AppendEntries RPCs, following Figure 2.
3. You will need to implement the election restriction(section 5.4.1 in the paper)
4. One way to fail to reach agreement in the early Lab 2B tests is to hold repeated elections even though the leader is alive. Look for bugs in election timer management, or not sending out heartbeats immediately after winning an election.
5. Your code may have loops that repeatedly check for certain events. Don't have these loops execute continuously without pausing, since that will slow your implementation enough that it fails tests. Use Go's condition variables, or insert a time.Sleep(10 * time.Millisecond) in each loop iteration. [cond](https://golang.org/pkg/sync/#Cond)
6. If you fail a test, look over the code for the test in config.go and test_test.go to get a better understanding what the test is testing. config.go also illustrates how the tester uses the Raft API.

### Fast BackUp
#### 总结
1. Raft的日志复制不完全可靠，当Leader在任期内发送日志条目时，可能出现发送失败、发送部分后失败等情况，导致Follower没有接收到
2. Raft的日志保证了Leader拥有已经commit的全部日志
3. Candidate只需要拥有的日志和至少和其他节点一样新即可

#### 需要先保证Leader不会改动已经commit的部分
1. Follower发送所有commit日志之后的下一位，和该日志对应的Term
2. Leader中一定存在这个Term，它会将nextIndex设置为这个Leader中该Term对应的所有日志之后的下一条日志
3. Leader下一次向Follower发送日志时，PrevLogIndex和PrevLogTerm会分别为Follower返回的XIndex - 1?和对应的日志条目的Term
4. 上一步相当于使得PrevLogIndex >= commitIndex，即，如果Leader中的相同term的日志更多，PrevLogIndex会增加
5. 按照以上规则，PrevLogIndex继续减少，直到找到一个Term和Follower中的相同，或者到达CommitIndex

#### 在Follower Log长度不足的前提下，先将nextIndex设置为Follower Log的长度在这个基础上，每次跳过一个任期，
1. Leader发送prevLogIndex和prevLogTerm,
2. 如果日志任期冲突，Follower返回任期和该任期对应的第一个日志的Log Index
3. Leader中也有这个任期，将nextIndex设置为该任期对应所有日志之后的下一条日志
		这个情况通常出现在新Leader被选出后，新Leader尝试复制日志给Follower，此时新Leader没有旧Leader的部分日志
		通常来说是由于旧Leader已经发送了一部分给Follower，导致二者不一致
		或者是旧Leader自己产生了一些日志，还没把日志复制给其他Follower，被新Leader取代了转化为Follower，这时候，新Leader应该把它拥有的对应任期的日志全部发送给Follower，它没有的则需要取代，因此将nextIndex设置为Leader的日志中，该任期对应所有日志条目之后的下一条
		下一次新Leader再发送日志时，就会把这之后的日志全部取代了
4. Leader中没有这个任期，已知prevLogIndex位置的日志不匹配，将nextIndex设置成XIndex
	(由于Follower返回的XIndex对应任期的第一条日志的Log Index，跳过Follower的一整个任期，下一次PrevLogIndex就会指向前一个任期的日志)
	通常出现在网络分区的情况下，网络分区恢复后，隔离区内的Follower会收到隔离区内旧Leader的日志信息，这些日志信息在新Leader中不存在

## Lab 2C
### Implementation
1. A real implementation would write Raft's persistent state to disk each time it changed, and would read the state from disk when restarting after a reboot. Your implementation won't use the disk; instead, it will save and restore persistent state from a Persister object (see persister.go). 

2. Whoever calls Raft.Make() supplies a Persister that initially holds Raft's most recently persisted state (if any). 

3. Raft should initialize its state from that Persister, and should use it to save its persistent state each time the state changes. **Use** the Persister's **ReadRaftState()** and **SaveRaftState()** methods.
   
4. Task: **Complete** the functions **persist()** and **readPersist()** in raft.go by adding code to save and restore persistent state. Y**ou will need to encode (or "serialize") the state as an array of bytes in order to pass it to the Persister.** Use the labgob encoder; **see the comments** in persist() and readPersist(). **labgob** is like Go's gob encoder but prints error messages if you try to encode structures with lower-case field names.
   
5. Task: **Insert calls to persist() at the points where your implementation changes persistent state**. Once you've done this, you should pass the remaining tests.
	1. currentTerm
	2. votedFor
	3. log[]

### Tips
1. LeaderAppendEntries() && CandidateRequestVote()
   注意在每个rpc前后都需要检查当前的rf.state, rf.currentTerm, rf.killed(), 避免发送过时的rpc，rpc后的检查需要放在Term检查之后
2. Lab2C的test中，存在丢失vote的情况，尤其是在网络分区成N+1，N两个部分时，难以选出Leader，因此我这里采用了重传机制，同时设置发送间隔时间0.2s，在一个超时间隔内可以发送5-6次，避免选不出Leader的情况。重传机制仅限于当前任期，随着任期结束，协程自然终止。
   （超时时间设置为1-1.5s，TimeOutChecker的时间间隔为0.03s，ApplyChecker的时间间隔为0.02s），为了提高性能，需要在始终运行的循环中添加time.Sleep(10 * time.Millisecond)
   由于Leader发送心跳的间隔为0.05s，对于LeaderAppendEntries就没必要设置重传机制了。
3. Candidate接收到同任期Leader的AppendEntries，需要转换为follower
4. AppendEntries.PrevLogIndex < rf.commitIndex，Leader不能修改Follower中已经提交的日志，因为需要设置reply.Success和reply.LogInconsistency
5. 在ApplyChecker中尽可能多的Apply当前已经Commit的所有LogEntry，写入applyCh时需要释放锁。
6. 并行测试数量过多时会触发signal: killed 或者exit status 1异常并退出

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
实验版：AppendEntries的功能是发送心跳和发送follower需要的日志，最主要的是确认follower需要哪部分日志，通过AppendEntries RPC交互几轮以后，确定一个match的点，Leader把这之后的日志都复制给Follower。引入了快照机制以后，Leader给Follower发送AE RPC时，Leader乐观的从它的最后一项日志开始搜索，假设他们的快照信息一致或者Log有交集，那么Leader很快能定位到match point，并发送剩下的日志。
引入了快照机制以后，Leader中一定有已经commit的全部日志，在prevIndex < Leader.lastIncludedIndex的情况下，Leader没有对应的日志项，无法向follower发送，只能先发送snapShot到follower。
因此，在每次Leader发送AppendEntries时，如果检测到当前的leader.prevIndex[follower] < leader.lastIncludedIndex, 就不再发送AppendEntries，而是向其发送InstallSnapShot，这里的InstallSnapShot需要重置选举计时器。
感觉这个设计是有缺陷的，InstallSnapShot RPC往往很大，在网络条件较差时容易传输失败，或许可以同时发送一个不含Entry的AppendEntries。
Follower接收到SnapShot后，先判断任期，再重置选举计时器，与当前的snapshot对比并保存，随后修剪日志，需要找到一个LogEntry满足LogEntry.Term == Leader.LastIncludedTerm && args.LastIncludedIndex，替换并删除包括这个日志之前的所有日志（注意保留Index = 0处的日志项），在应用日志后需要更新follower的commitIndex和lastApplied，随后保存快照和状态，并通过applyCh发送applyMsg告知Server开始应用快照，注意不能持有锁。

Leader根据reply需要更新nextIndex，采用XTerm和XIndex作为AppendEntries的返回值

InstallSnapShot的实现
1. 判断任期，任期大于自己就转为Follower并重置选票
2. 根据RPC和自身的LastIncludeIndex判断Snapshot是否Uptodate，如果不接受，直接返回
3. 保存Snapshot
4. 根据Snapshot的lastIncludedIndex寻找对应的日志项，检查对应日志项的任期是否匹配，并裁剪日志
5. 更新自身的LastIncludedIndex, LastIncludedTerm, (以及FirstIndex，它的值一直等于LastIncludedIndex，应该省略掉这个额外的变量)
6. 保存service的State和Snapshot
7. 通过rf.applyCh向上层服务发送Snapshot

Leader发送InstallSnapShot的实现
1. AppendEntries和InstallSnapShot本质上都是Leader向Follower同步信息，因此我设计成仅发送其中一种。
   引入快照机制以后，由于日志被裁剪，为了Leader和Follower能够通过AppendEntries RPC继续匹配日志项，需要保存快照最后一项的index和term，即lastIncludedIndex和lastIncludedTerm，作为follower中第一个日志项之前的prevLogIndex和prevLogIndex，参与到过程中。
2. 在发送AppendEntries RPC之前，Leader首先检查自己的日志，判断是否存在应发往Follower的日志项，如果存在就发送AppendEntries RPC，否则发送InstallSnapshotRPC，index大于rf.LastIncludedIndex的日志项都存在。 在没有快照机制的情况下，发往follower的AppendEntries RPC中prevLogIndex、prevLogTerm应该根据下标为rf.nextIndex[followerID] -1的日志，即nextIndex[server]下标所在日志项的前一项得出。因此，假如Leader准备RPC的参数时发现，args.prevLogIndex <  rf.lastIncludedIndex，说明nextLogIndex[server] <= rf.lastIncludedIndex, 想要发送的nextLogEntry包含在快照中，follower的进度太慢了只能发送InstallSnapshot。否则，说明nextIndex[server]的日志项存在于Leader的日志中，发送AppendEntries。
3. Raft peer的日志都是1-based index，index = 0处的日志项无意义，在不裁剪日志的情况下，日志数组rf.Log的下标arrayIndex和实际的下标index相同。 引入快照机制以后，当arrayIndex > 0 时，日志项rf.Log[arrayIndex]对应的下标index = rf.lastIncludedIndex + arrayIndex，但当我们需要获取arrayindex = 0处日志项的信息时，应该返回rf.lastLogIndex和rf.lastLogTerm。
   例如，（Leader发送AppendEntries RPC给Follower，需要设置args.prevLogIndex = rf.nextIndex[server] - 1，但此时Leader.nextIndex[followerID] = Follower.Log[1]），Follower接收到AppendEntries RPC后，寻找args.PrevLogIndex对应的日志项Entry，比较args.PrevLogTerm和Entry.Term。因此Follower会根据arrayIndex = args.prevLogIndex - rf.lastIncludedIndex得到arrayIndex = 0，找到rf.Log[0] ，但rf.Log[0]是无意义的，此时应该返回rf.Log[1]日志项前一项的数据，即rf.prevLogIndex和rf.prevLogTerm来匹配。
   实现：在Lab2B中，最好就不要使用rf.Log[index]来获取下标为index的日志，转而实现一个rf.getLogAt(arrayIndex)的方法，先获取对应日志项在日志列表rf.Log[]中的数组下标arrayIndex，根据arrayIndex = TargetIndex - rf.lastIncludedIndex计算，再用ra.getLogAt(arrayIndex)获取信息，当arrayIndex = 0时，返回rf.lastIncludedIndex, rf.lastIncludedTerm
   ```go
   // Input: arrayIndex of LogEntry
   // Output: exist, LogEntry.Index, LogEntry.Term
	func (rf *Raft) getLogAt(realIndex int) (bool, int, int) {
		if realIndex >= len(rf.Log) {
			return false, -1, -1
		} else if realIndex == 0 {
			return true, rf.lastIncludedIndex, rf.lastIncludedTerm
		} else {
			return true, rf.Log[realIndex].Index, rf.Log[realIndex].Term
		}
	}
```
4. InstallSnapshot和fast back-up的兼容 
   未引入快照机制时，使用课上介绍的方法，向AppendEntriesReply中添加XTerm, XIndex, XLen信息
   当接收到来自Follower的AppendEntriesArgs时，包含args.PrevLogIndex, args.PrevLogTerm
   1. Follower:
	   1. args.PrevLogIndex对应下标的日志项LogEntry存在
		   1. LogEntry.Term == args.PrevLogTerm，匹配
		   2. LogEntry.Term != args.PrevLogTerm, 任期不匹配， 找到rf.Log[]中第一个满足entry.Term == LogEntry.Term的日志项，并返回它的下标和任期, XTerm = entry.Term, XIndex = entry.Index, 
		      即Follower悲观地认为对应任期的日志是不完整的，需要Leader从头同步
	   2. args.PrevLogIndex对应下标的日志项LogEntry不存在
		   1. 下一次尝试从日志最后一项开始，返回 XTerm = -1, XLen = len(Follower.Log)
   2. Leader
	   1. XTerm = -1，设置nextIndex[FollowerID] = XLen
	   2. XTerm != -1，找到rf.Log[]中最后一个满足entry.Term == XTerm的日志项，设置nextIndex[FollowerID] = entry.Index + 1，即Leader乐观地认为对应任期的日志在Follower中都存在，只需要发送对应任期以后的日志
	      
	可以简化为，只使用XTerm和XIndex两个参数，返回XTerm=-1时，Leader接收Follower的reply并设置nextIndex为XIndex
	1. Follower:
	   1. args.PrevLogIndex对应下标的日志项LogEntry存在
		   1. LogEntry.Term == args.PrevLogTerm，匹配
		   2. LogEntry.Term != args.PrevLogTerm, 任期不匹配， 找到rf.Log[]中第一个满足entry.Term == LogEntry.Term的日志项，并返回它的下标和任期, XTerm = entry.Term, XIndex = entry.Index, 
		      即Follower悲观地认为对应任期的日志是不完整的，需要Leader从头同步
	   2. args.PrevLogIndex对应下标的日志项LogEntry不存在
		   1. 下一次尝试从日志最后一项开始，返回 XTerm = -1, XIndex = len(Follower.Log)
	2. Leader
	   1. XTerm = -1，设置nextIndex[FollowerID] = XIndex
	   2. XTerm != -1，找到rf.Log[]中最后一个满足entry.Term == XTerm的日志项，设置nextIndex[FollowerID] = entry.Index + 1，即Leader乐观地认为对应任期的日志在Follower中都存在，只需要发送对应任期以后的日志
	
	引入快照机制后，会产生以下几种情况
	1. Follower过于落后，args.prevLogIndex 大于Follower中最后一项日志的下标，此时肯定无法匹配，从最后一个日志项的下一项开始。
	2. Follower接收到的RPC来自于合法的Leader，但它是过时的，试图和Follower中已经包含在快照中的日志项匹配，测试中没有发现这个情况。
	   
	实现
	1. Follower:
	   1. 判断Leader匹配的日志项是否大于Follower的最后一个日志项args.PrevLogIndex > lastLogIndex，如果大于，返回XTerm= -1, XIndex = rf.lastLogIndex()+1，否则继续
	   2. args.PrevLogIndex对应下标的日志项LogEntry是否存在
		   1. LogEntry.Term == args.PrevLogTerm，匹配
		   2. LogEntry.Term != args.PrevLogTerm, 任期不匹配， 找到rf.Log[]中第一个满足entry.Term == LogEntry.Term的日志项，并返回它的下标和任期, XTerm = entry.Term, XIndex = entry.Index, 
		      即Follower悲观地认为对应任期的日志是不完整的，需要Leader从头同步
	   2. args.PrevLogIndex对应下标的日志项LogEntry不存在
		   1. 下一次尝试从日志最后一项开始，返回 XTerm = -1, XIndex = rf.lastLogIndex() + 1
  
	2. Leader
	   1. XTerm = -1，设置nextIndex[FollowerID] = XIndex
	   2. XTerm != -1，找到rf.Log[]中最后一个满足entry.Term == XTerm的日志项，设置nextIndex[FollowerID] = entry.Index + 1，即Leader乐观地认为对应任期的日志在Follower中都存在，只需要发送对应任期以后的日志
