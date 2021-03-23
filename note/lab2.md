## LAB2的实现思路

> lab2A主要是要实现leader的选举，需要通过两个测试。
>
> 一是正常启动时选出一个leader，二是发生网络分区的时候剩下的大多数节点仍然可以选举leader
>
> Lab2B 要求实现实现日志同步，包括但不限于在各种leader失联、节点宕机、网络延迟等各种情况下保持日志的一致性
>
> lab3b 持久化数据



在每个节点初始化完成后，所有的节点角色是follower有各自的随机election_time_out时间，在超时时间内没有收到来自leader的消息，自己转换角色成为candidate。开始发送**requestVoteArgs**请求为自己投票。

因为大家都可以成为**candidate**，在转换角色，发起投票，收到投票这三个时间点都会发生意外情况。

大概有三种情况。

1：正准备发送**requestVoteArgs**，我已经不是候选人了。这个时候可能是别的candidate已经竞选成功。

2：收到的**requestVoteReply**,如果收到的回复的 Term 大于自己或者自己已经不是 **candidate**，说明其他的 follower 已经选出其他的节点。则自己应该转换为 **follower**角色。如果收到选票时，其实自己已经不是 candidate 了，则这张选票也是无效的。

3：当收到的选票时大多数的时候，自己成功成为**leader**

```go
//候选人开始 竞选leader程序
func (rf *Raft) leaderElection(){
	rf.mu.Lock()
	//candidate term 已经在changeRole里+1了
	//发给每个节点的请求参数都是一样的。所以要加锁。
	requestArgs := &RequestVoteArgs{rf.currentTerm, rf.me, rf.getLastLogIndex(), rf.getLastLogTerm()}
	rf.mu.Unlock()

	voteCnt := 1 //获得的选票，自己肯定是投给自己啦
	voteFlag := true //收到过半选票时管道只通知一次
	voteL := sync.Mutex{}

	for followerId, _ := range rf.peers{
		if followerId == rf.me{
			continue
		}

		rf.mu.Lock()
		if !rf.checkState(Candidate, requestArgs.Term){
			//发送大半投票时，发现自己已经不是candidate了
			rf.mu.Unlock()
			return
		}
		rf.mu.Unlock()

		go func(server int) {
			reply := &RequestVoteReply{}
			if ok := rf.sendRequestVote(server, requestArgs, reply); ok{
				//ok仅仅代表得到回复，
				//ok==false代表本次发送的消息丢失，或者是回复的信息丢失

				rf.mu.Lock()
				defer rf.mu.Unlock()
				if reply.Term > rf.currentTerm{
					//有更高term的存在
					rf.convertRoleTo(Follower)
					return
				}

				if !rf.checkState(Candidate, requestArgs.Term){
					//收到投票时，已经不是candidate了
					return
				}

				if reply.VoteGranted{
					//收到投票
					voteL.Lock()
					defer voteL.Unlock()
					voteCnt = voteCnt + 1
					if voteFlag && voteCnt > len(rf.peers)/2{
						voteFlag = false
						rf.convertRoleTo(Leader)
						rf.dropAndSet(rf.leaderCh)
					}
				}

			}
		}(followerId)
	}
}
```

而作为一follower,收到candidate的requestVote请求应该遵循这样的几个原则 。

1. 收到了leader的信息，重置选举超时器。

2. 投票给了candidate ,重置选举超时器。

3. 定时器超时了，自己转换为candidate 角色。

4. 一轮任期只能投一次，且只能投给任期term比自己大和日志比自己新的candidate.

   ```go
   func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
   	// Your code here (2A, 2B).
   	rf.mu.Lock()
   	defer rf.mu.Unlock()
   
   	reply.Term = rf.currentTerm
   	reply.VoteGranted = false
   
   	if rf.currentTerm < args.Term{
   		//进入新一轮投票
   		//就会改变自己的currentTerm
   		//并将自己转为follower
   		rf.currentTerm = args.Term
   		rf.convertRoleTo(Follower)
   	}
   
   	//candidate日志比本节点的日志“新”
   	newerEntries := args.LastLogTerm > rf.getLastLogTerm() || (args.LastLogTerm == rf.getLastLogTerm() && args.LastLogIndex >= rf.getLastLogIndex())
   	//判断这一轮term内是否已经投票给某人
   	//投票给某人的信息可能会丢失，所以需要加上另一个判断
   	voteOrNot := rf.voteFor == VotedNull || rf.voteFor == args.CandidateId
   
   	if  newerEntries && voteOrNot{
   		rf.voteFor = args.CandidateId
   		reply.VoteGranted = true
   
   		rf.dropAndSet(rf.voteCh)
   		WarnRaft.Printf("Raft:%2d term:%3d | vote to candidate %3d\n", rf.me, rf.currentTerm, args.CandidateId)
   		rf.persist()
   	}
   }
   ```

   **leader需要做的事情**

   成为leader后，应该立即发送一次消息宣告自己的身份。

   而其他的follwer可能是如下几种情况。

   1. 缺少很多日志。
   2. 曾经是leader,包含了一些多余的提交的或者未提交的日志。

   在Raft中，通过使用Leader的日志覆盖Follower的日志的方式来解决以上情况（**强Leader**）。Leader会找到Follower和自己想通的最后一个日志条目，将该条目之后的日志全部删除并复制Leader上的日志。详细过程如下：

   - Leader维护了每个Follower节点下一次要接收的日志的索引，即nextIndex

   - Leader选举成功后将所有Follower的nextIndex设置为自己的最后一个日志条目+1

   - Leader将数据推送给Follower，如果Follower验证失败（nextIndex不匹配），则在下一次推送日志时缩小nextIndex，直到nextIndex验证通过

     ```go
     //leader广播日志
     func (rf *Raft) broadcastEntries() {
     	rf.mu.Lock()
     	curTerm := rf.currentTerm
     	rf.mu.Unlock()
     	//用余控制 超半数commit只修改一次leader的logs
     	commitFlag := true
     	//记录commit某个日志的节点数量
     	commitNum := 1
     	commitL := sync.Mutex{}
     
     	for followerId, _ := range rf.peers {
     		if followerId == rf.me {
     			continue
     		}
     
     		//发送信息
     		go func(server int) {
     			for {
     				//for循环仅针对leader和follower的日志不匹配而需要重新发送日志的情况
     				//其他情况直接返回
     				//每一个节点的请求参数都不一样
     				rf.mu.Lock()
     				if !rf.checkState(Leader, curTerm) {
     					//已经不是leader了
     					rf.mu.Unlock()
     					return
     				}
     				//假设是一个脱离很久的节点回到了集群，他需要的日志已经被快照了，那就发送快照给他好了
     				next := rf.nextIndex[server]
     				if next <= rf.lastIncludedIndex{
     					rf.sendSnapshot(server)
     					return
     				}
     
     				appendArgs := &AppendEntriesArgs{curTerm,
     					rf.me,
     					rf.getPrevLogIndex(server),
     					rf.getPrevLogTerm(server),
     					rf.logs[rf.subIdx(next):],
     					rf.commitIndex}
     
     				rf.mu.Unlock()
     				reply := &AppendEntriesReply{}
     				ok := rf.sendAppendEntries(server, appendArgs, reply);
     				if ! ok{
     					return
     				}
     
     				rf.mu.Lock()
     				//返回的term比发送信息时leader的term还要大
     				if reply.Term > curTerm {
     					rf.currentTerm = reply.Term
     					rf.convertRoleTo(Follower)
     					InfoRaft.Printf("Raft:%2d term:%3d | leader done! become follower\n", rf.me, rf.currentTerm)
     					rf.mu.Unlock()
     					return
     				}
     
     				//发送信息可能很久，所以收到信息后需要确认状态
     				//类似于发送一条信息才同步
     				//一个新leader刚开始发送心跳信号，即便和follower不一样也不修改follower的日志
     				//只有当新Leader接收新消息时，才会修改follower的值，防止figure8的情况发生
     				if !rf.checkState(Leader, curTerm) || len(appendArgs.Entries) == 0{
     					//如果server当前的term不等于发送信息时的term
     					//表明这是一条过期的信息，不要了
     
     					//或者是心跳信号且成功，也直接返回
     					//如果是心跳信号，但是append失败，有可能是联系到脱离集群很久的节点，需要更新相应的nextIndex
     					//比如一个新leader发送心跳信息，如果一个follower的日志比args.prevLogIndex小
     					//那么此时reply失败，需要更新nextIndex
     					rf.mu.Unlock()
     					return
     				}
     
     				if reply.Success {
     					//append成功
     					//考虑一种情况
     					//第一个日志长度为A，发出后，网络延迟，很久没有超半数commit
     					//因此第二个日志长度为A+B，发出后，超半数commit，修改leader
     					//这时第一次修改的commit来了，因为第二个日志已经把第一次的日志也commit
     					//所以需要忽略晚到的第一次commit
     					curCommitLen := appendArgs.PreLogIndex +  len(appendArgs.Entries)
     
     					if curCommitLen < rf.commitIndex{
     						rf.mu.Unlock()
     						return
     					}
     
     					//两者相等的时候
     					//代表follower的日志长度==match的长度，所以nextIndex需要+1
     					if curCommitLen >= rf.matchIndex[server]{
     						rf.matchIndex[server] = curCommitLen
     						rf.nextIndex[server] = rf.matchIndex[server] + 1
     					}
     
     					commitL.Lock()
     					defer commitL.Unlock()
     
     					commitNum = commitNum + 1
     					if commitFlag && commitNum > len(rf.peers)/2 {
     						commitFlag = false
     						//leader提交日志，并且修改commitIndex
     						//如果在当前任期内，某个日志已经同步到绝大多数的节点上，
     						//并且日志下标大于commitIndex，就修改commitIndex。
     						rf.commitIndex = curCommitLen
     						rf.applyLogs()
     					}
     
     					rf.mu.Unlock()
     					return
     
     				} else {
     					//prevLogIndex or prevLogTerm不匹配
     					//如果leader没有conflictTerm的日志，那么重新发送所有日志
     					//新加入的节点可能没有日志，其conflictIndex是0
     					if reply.ConflictTerm == -1{
     						//follower缺日志或者上一次发送的日志follower已经快照了
     						rf.nextIndex[server] = reply.ConflictIndex
     					}else{
     						//先假设全部日志不匹配
     						rf.nextIndex[server] = rf.addIdx(1)
     						i := reply.ConflictIndex
     						for ; i > rf.lastIncludedIndex; i--{
     							if rf.logs[rf.subIdx(i)].Term == reply.ConflictTerm{
     								rf.nextIndex[server] = i + 1
     								break
     							}
     						}
     						if i <= rf.lastIncludedIndex && rf.lastIncludedIndex != 0{
     							//leader拥有的日志都不能与follower匹配
     							//需要发送快照
     							rf.nextIndex[server] = rf.lastIncludedIndex
     						}
     					}
     
     					InfoRaft.Printf("Raft:%2d term:%3d | Msg to %3d fail,decrease nextIndex to:%3d\n",
     						rf.me, rf.currentTerm, server, rf.nextIndex[server])
     					rf.mu.Unlock()
     				}
     			}
     
     		}(followerId)
     
     	}
     }
     ```

     紧接着follwer 接收到 leader 的日志同步/心跳，需要识别这是否是一个过期的leader消息，比较日志找到冲突点，同步日志。

     ```json
     func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
     	//follower收到leader的信息
     	rf.mu.Lock()
     	//defer后进先出
     	defer rf.mu.Unlock()
     
     
     	reply.Term = rf.currentTerm
     	reply.Success = false
     	reply.ConflictIndex = -1
     	reply.ConflictTerm = -1
     
     	if args.Term < rf.currentTerm {
     		return
     	}
     
     	if args.Term > rf.currentTerm{
     		rf.currentTerm = args.Term
     		reply.Term = rf.currentTerm
     		rf.convertRoleTo(Follower)
     	}
     	//遇到心跳信号len(args.entries)==0不能直接返回
     	//因为这时可能args.CommitIndex > rf.commitIndex ,需要交付新的日志
     	//通知follower，接收到来自leader的消息
     	//即便日志不匹配，但是也算是接收到了来自leader的心跳信息。
     	rf.dropAndSet(rf.appendCh)
     
     	//args.Term >= rf.currentTerm
     	//logs从下标1开始，log.Entries[0]是占位符
     	//所以真实日志长度需要-1
     
     	//如果reply.conflictIndex == -1表示follower缺少日志或者leader发送的日志已经被快照
     	//leader需要将nextIndex设置为conflictIndex
     	if rf.getLastLogIndex() < args.PreLogIndex{
     		//如果follower日志较少
     		reply.ConflictTerm = -1
     		reply.ConflictIndex = rf.getLastLogIndex() + 1
     		InfoRaft.Printf("Raft:%2d term:%3d | receive leader:[%3d] message but lost any message! curLen:%4d prevLoglen:%4d len(Entries):%4d\n",
     			rf.me, rf.currentTerm, args.LeaderId, rf.getLastLogIndex(), args.PreLogIndex, len(args.Entries))
     		return
     	}
     
     	if rf.lastIncludedIndex > args.PreLogIndex{
     		//已经快照了
     		//让leader的nextIndex--，直到leader发送快照
     		reply.ConflictIndex = rf.lastIncludedIndex + 1
     		reply.ConflictTerm = -1
     		return
     	}
     
     	//接收者日志大于等于leader发来的日志  且 日志项不匹配
     	if args.PreLogTerm != rf.logs[rf.subIdx(args.PreLogIndex)].Term{
     		//日志项不匹配，找到follower属于这个term的第一个日志，方便回滚。
     		reply.ConflictTerm = rf.logs[rf.subIdx(args.PreLogIndex)].Term
     		for i := args.PreLogIndex; i > rf.lastIncludedIndex ; i--{
     			if rf.logs[rf.subIdx(i)].Term != reply.ConflictTerm{
     				break
     			}
     			reply.ConflictIndex = i
     		}
     		InfoRaft.Printf("Raft:%2d term:%3d | receive leader:[%3d] message but not match!\n", rf.me, rf.currentTerm, args.LeaderId)
     		return
     	}
     
     	//接收者的日志大于等于prelogindex，且在prelogindex处日志匹配
     	rf.currentTerm = args.Term
     	reply.Success = true
     
     	//修改日志长度
     	//找到接收者和leader（如果有）第一个不相同的日志
     	i := 0
     	for  ; i < len(args.Entries); i++{
     		ind := rf.subIdx(i + args.PreLogIndex + 1)
     		if ind < len(rf.logs) && rf.logs[ind].Term != args.Entries[i].Term{
     			//修改不同的日志, 截断+新增
     			rf.logs = rf.logs[:ind]
     			rf.logs = append(rf.logs, args.Entries[i:]...)
     			break
     		}else if ind >= len(rf.logs){
     			//添加新日志
     			rf.logs = append(rf.logs, args.Entries[i:]...)
     			break
     		}
     	}
     
     
     	if len(args.Entries) != 0{
     		//心跳信号不输出
     		//心跳信号不改变currentTerm、votedFor、logs，改变角色的函数有persist
     		rf.persist()
     		InfoRaft.Printf("Raft:%2d term:%3d | receive new command from leader:%3d, term:%3d, size:%3d curLogLen:%4d LeaderCommit:%4d rfCommit:%4d\n",
     			rf.me, rf.currentTerm, args.LeaderId, args.Term, len(args.Entries), len(rf.logs)-1, args.LeaderCommit, rf.commitIndex)
     	}
     	//修改commitIndex
     	if args.LeaderCommit > rf.commitIndex {
     		rf.commitIndex = min(args.LeaderCommit, rf.addIdx(len(rf.logs)-1))
     		rf.applyLogs()
     	}
     
     
     
     }
     ```

     

   **程序是如何运行的**

   利用Make函数创建好后，直接开启goroutine运行，防止阻塞主线程。goroutine内无限循环监听自己的角色，做对应的事情。

   ```go
   func Make(peers []*labrpc.ClientEnd, me int,
   	persister *Persister, applyCh chan ApplyMsg) *Raft {
   	rf := &Raft{}
   	rf.peers = peers
   	rf.persister = persister
   	rf.me = me
   
   	// Your initialization code here (2A, 2B, 2C).
   	rf.currentTerm = -1
   	rf.voteFor = VotedNull
   
   	rf.role = Follower
   
   	//log下标从1开始，0是占位符，没有意义
   	rf.logs = make([]Entries, 1)
   
   	rf.commitIndex = 0
   	rf.lastApplied = 0
   	rf.nextIndex = make([]int, len(peers))
   	rf.matchIndex = make([]int, len(peers))
   
   	rf.appendCh = make(chan bool, 1)
   	rf.voteCh = make(chan bool, 1)
   	rf.exitCh = make(chan bool, 1)
   	rf.leaderCh = make(chan bool, 1)
   
   	rf.applyCh = applyCh
   
   	rf.lastIncludedIndex = 0
   	rf.lastIncludedTerm = -1
   
   	// initialize from state persisted before a crash
   	rf.readPersist(persister.ReadRaftState())
   	//logs[0]为占位符
   	rf.logs[0] = Entries{rf.lastIncludedTerm, rf.lastIncludedIndex, -1}
   
   	//要加这个，每次的rand才会不一样
   	rand.Seed(time.Now().UnixNano())
   
   	InfoRaft.Printf("Create a new Raft:[%3d]! term:[%3d]! Log length:[%4d]\n", rf.me,rf.currentTerm, rf.getLastLogIndex())
   
   	//主程序负责创建raft实例、收发消息、模拟网络环境
   	//每个实例在不同的goroutine里运行，模拟多台机器
   	go func() {
   	Loop:
   		for{
   			select{
   			case <- rf.exitCh:
   				break Loop
   			default:
   			}
   
   			rf.mu.Lock()
   			role := rf.role
   			eTimeout := rf.electionTimeout()
   			rf.mu.Unlock()
   
   			switch role{
   			case Leader:
                   //自己是主节点开启广播，按照心跳时间的规律。
   				rf.broadcastEntries()
   				time.Sleep(heartBeat)
   			case Candidate:
   				go rf.leaderElection()
   				select{
   				//在request和append里已经修改角色为follower了
   				case <- rf.appendCh:
   				case <- rf.voteCh:
   				case <- rf.leaderCh:
   				case <- time.After(eTimeout):
   					rf.mu.Lock()
   					rf.convertRoleTo(Candidate)
   					rf.mu.Unlock()
   				}
   			case Follower:
   				select{
   				case <- rf.appendCh:
   				case <- rf.voteCh:
   				case <- time.After(eTimeout):
   					rf.mu.Lock()
   					rf.convertRoleTo(Candidate)
   					rf.mu.Unlock()
   				}
   			}
   		}
   
   	}()
   
   	return rf
   }
   ```

   本程序检测到需要转换角色的时候，总是调用**convertRoleTo**方法，是否真的需要转换角色和转换角色后需要做的事情都在本函数中。

   

