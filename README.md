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
7. 

##### 优化
1. 优化只读请求。目前的read是通过raft log实现的，这是课程Lab要求的实现方式。可以通过以下几种方式优化，
	1. ReadIndex：先记录当前ReadIndex，发送心跳确认自己是leader（减少复制日志的开销），等待lastApplied > readIndex时返回结果。把复制日志的开销变为发送心跳。leader刚当选时需要发送一条No-Op来提交之前的所有log。
	2. LeaseRead：Leader在发送心跳之前记录时间，在发送心跳确认自己得到大多数Follower的回应后(即大多数节点不会竞选成为新的节点)，延长它的租约，在租约期间无需再次发送heartbeat确认自己的leader身份，租约到期后自动变为follower。该方法性能最好，节省了发送心跳的过程。但该方案对时钟的准确性要求高，可能导致出现两个Leader的情况。
   ETCD的raft层基于readIndex，但可以切换至leaseRead
2. 为什么ReadIndex需要等待 本地的lastApplied >= readIndex?  会破坏线性一致性。
3. 为什么在ReadIndex中，刚选出的Leader需要写一条No-Op？(Leader apply) 
![|925](images/Pasted%20image%2020230630230537.png)
概括：Leader apply日志项并且回复了client的请求，但commitIndex还没来得及发送给follower，followers不知道leader已经commit了，不知道当前commitindex的进度，因此可能会返回旧数据。
