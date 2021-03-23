> 虽然已经基于raft构造了一个简单的分布式数据库，实现了多节点之间的数据一致性，支持增删改查，数据同步和快照。然而当数据增长到一定程度的时候，若仍然使用单一集群服务所有数据，将会使大量的访问落在一个单一的leader上面，增加集群压力，演唱请求响应时间。这是因为，我么们虽然分布式缓存了数据，确保leader宕机，数据依然可用，但是没有考虑到集群负载的问题。一个非常显而易见的办法就是将数据按照某种方式分开存储到不同的集群上，降低单一集群的压力。

简单来说，需要实现分库分表，讲不通的数据划分到不同的集群上，抱枕你好好相应的数据引流到对应的集群。这里将互不相交且合力组成完整数据库的每一个数据库子集称为shard.在同一阶段，shard与集群的关系称之为配置，随着时间的迁移，新集群的加入，或者现有集群的离去，shard需要在不用的集群之中进行迁移。

## ShardMaster

这里主要解决配置更新，重新划分shard的逻辑，要求尽可能移动少的shard方式尽可能平均分配到提供服务的集群中。用一个线程监听各种配置更新，包括集群的加入和退出，迁移，查询配置信息。

这里分配shard的算法比较简单，将shard总数/集群数量 得到一个平均值n.

要求尽可能的划分shard，每个集群拥有的shard不是n个就是n＋1个，那么如果这里使用大小为4的切片数组来保存四种类型的集群。第一种是集群现有的shard数量小于N个，存放在tless中。第二种是正好等于N个的集群，存放在tok中，第三种是N+1个，存放在tplus种，第四种就是超多的，存放在tmore中。

扫描的逻辑如下：

- 扫描新配置Shards数组中值为0的shard，代表还没有分配，存放在待分配切片中。
- 扫描Group2Shards，计算出每个集群已经分配到的shard数量，并将扫描的结果存放在相应的地方保存起来。
- 扫描tmore切片，这部分保存的shard数量远远超负荷的，应该减少这部分集群持有的shard。并且分配给其他的group。直到tmore的数量为shard+1为止。
- 遍历shardToAssign（需要分配的shard切片），分配给tless的group。直到这些集群的shard集群数量到N为止。如果没有tless，就分配给tok。
- 继续遍历tless，如果仍然还有group负责的节点小于N，此时应该将tmore里面的迁移到tless,出现这种情况有可能是集群离去了，或者是shard数量正好可以整除集群数量。
