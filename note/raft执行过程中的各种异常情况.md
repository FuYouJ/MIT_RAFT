> 默认你已经熟悉了raft选举和执行命令的过程。

raft协议强依赖leader节点的可用性来确保集群数据的一致性。数据的流向只能从Leader节点向Follower节点转移。当客户端向leader节点发送请求命令时，Leader接收到命令此时处于未提交的状态，然后leader将这个命令发送给我所有的Follower节点并等待响应。当接收到超过大多数节点Follower节点的响应，Leader回复客户端确认请求成功。此时leader的这一条数据处于已提交状态，然后广播给所有的Follower节点告知此命令已经被提交。

> 既然leader节点是如此的重要，那么leader节点在处理客户端请求的任意一个阶段都会挂掉。简单来说，会有以下几种异常情况。

**1.客户端发送的请求尚未到达leader节点，leader节点挂掉 **

此时因为leader节点挂掉，集群内部正在或者正准备在进行选举新的leader，客户端发出的请求因为无人响应而超时，此时客户端要不重发请求，要不不发。因为命令都还没有到达集群，所以不影响集群内部数据的一致性。

**2.请求数据到达leader，leader还未将未提交的数据发送给follower，此时leader节点挂掉**

此时数据处于未提交的状态，因为leader节点挂掉无人响应，客户端继续因为请求超时重发请求。此时集群内部的follower都是没有这个未提交的数据的，其中一个从节点成为leader后，就可以响应客户端的请求了。同样的，在成为leader后发起的心跳信息，其他follower从这里同步数据，继续保持数据的一致性。

**3.请求数据到达leader，leader将数据同步给了*所有*follower，follower还未响应的时候，此时leader节点挂掉**

此时所有的节点的数据都是未提交的状态，重新选举一个leader会将本次修改同步到其余未接到修改请求的follower。因为raft要求client的修改请求拥有幂等性，也就是会自动去除重复的请求，所以leader直接commit本次修改即可。

**4.请求数据到达leader，leader将数据同步给了*部分*follower,follower还未响应的时候，此时leader节点挂掉**

此时因该分析一下。同步到了部分节点到底是大多数还是少数部分。假设10个节点里面有3个节点接收到了数据，7个节点没有收到数据。此时就算follower响应，leader也不会执行这个命令。虽然集群数据看起来是不一致的，然后选举出来的leader可能是7个节点里面的，可能是3个节点里面的.

如果是旧节点呢，就会要求新节点回滚日志，如果是新节点，会将本次修改同步到follower，因为raft实现了幂等性，leader直接提交本次修改即可。

**5.leader同步数据到达多数follower，follower还未响应挂掉**

此时选举出来的leader一定是新日志的，同全部节点无差别。处理同 3

**6.数据到达了leader节点，成功复制给了大多数follower节点，此时也得到了大多数节点的响应，此时leader挂掉**

这个时候选出来的leader一定是之前响应leader的节点，客户端继续重试就行了。

**网络分区**

网络分区，可能会发生脑裂的情况。假设有5个节点，ABCDE,A是leader,此时发生网络分区。AB|CDE。由于C接收不到leader的消息选举超时器超时，此时发生选举，通过自己这一票加上DE成为了新的leader。此时整个集群有两个leader。此时如果有请求发送给A，A需要同步数据给follower,和A连接的只有B，所以发送给A的请求不会得到执行，反之发送给C的请求会得到执行。在网络分区恢复后，A因为发现了集群中有更加新的leader（C），会自动降级，从新leader同步数据达成数据一致。

## 网络分区的脏读问题

很明显，发生网络分区的时候，有可能发生脏读问题。在读请求发送到旧leader时，会发生脏读。

依然以AB|CDE为例，A是旧leader，C是另外一个分区的leader.此时一个写请求发送给C，C能够得到大多数节点的响应，是可以正常的更新数据的。但是与此同时，一个相同的读请求发送给A，就会读到脏数据。

PingCap联合创始人、CTO 黄东旭写的一篇解决Raft网络分区的一种方案: [通过 raft 的 leader lease 来解决集群脑裂时的 stale read 问题](https://pingcap.com/blog-stale-read-zh)。这里简单说一下。

引入一个新的概念，region leader,region leader是一个逻辑上的概念，任意时刻对于一个region来说，只有一个region leader.每个region leader在任期时间内用心跳更新一下自己的租约时间。所有的读写请求都必须经过region leader 完成，可能region leader 和 raft leader 可能不是同一个节点。当他们不是同一个节点的时候，raft leader 就要转发请求给 region leader.所以在网络分区出现的时候，会出现以下四种情况。

region leader 在多数派，老 raft leader 在多数派

因为region leader 在多数派，region leader 的租约不会到期，而老raft leader 也在多数派，数据也能发送到大多数节点上。此时不会有少数派选举成为新的leader,也就不会出现读到过期的数据问题。

region leader 在多数派，老 raft leader 在少数派

此时老 raft leader 在少数派，多数派那边选举出了新的 raft leader，如果此时的 region leader 在多数派这边，因为所有的读写请求都会找到 region leader 处理，然后由region leader 转发给 raft leader,此时多数派这边有一个raft leader,此时读写请求就全部在多数派正常进行，就没有 steal read的问题。

region leader 在少数派，老 raft leader 在多数派

此时的客户端请求发动给了 region leader,此时 region  leader 在少数派这边联系不上 raft  leader ,而且少数派这边也选举不出来新的 raft leader，请求会失败，直到 region leader 由于在少数派无法正常的续约，同时新的 region leader 会在多数派产生，在此之前，发送给 region leader 读请求无法被执行，所以就不胡 steal read 的问题。代价就是 再下一个多数派的region leader 选举出来时系统的可用性。

region leader 在少数派，老 raft leader 在少数派

同上，多数派会选举出来新的 region leader 和 raft  leader 。 

## PreVote 解决Term无限增加的问题

当一个 follower 节点因为网络分区与leader没有了联系，在 选举超时器超时后，这个follower会转换成candidate发起选举，因为网络隔离的原因，他不会选举成功，也不会收到leader的消息，但是他会一直奴段的尝试选举，term不断增大。虽然这个节点不会选举成功，但是他的Term太大了，在网络分区恢复后，这个节点把他的term传播给其他节点，导致其他的节点修改自己的term,变为follower,重新选举。但是这个candidate日志不是最新的，他不会成为leader，但是整个集群都被他搞乱了，又要重新选举，这个情况是需要避免以下的。



在PreVote 算法里面，Candidate首先要确认自己能够赢得集群大多数节点的同意，才能将自己的Term增加，然后发起一次真正的选举。而其他节点同意发起选举的规则是。

- 没有收到 leader 的心跳，至少有一次选举超时。
- Candidate 的日志足够新。Term更大，或者Term相同 index更大。

Pre Vote 解决了，网络分区节点重新加入会扰乱集群的问题，在此算法中，网络分区节点由于无法获得大多数节点的同意，无法增加term,当他重新加入集群，依然无法递增term,然后收到了leader的心跳后，恢复自己follower的角色。

