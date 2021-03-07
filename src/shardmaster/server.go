package shardmaster

import (
	"raft/src/kvraft"
	"raft/src/labgob"
	"raft/src/labrpc"
	"raft/src/raft"
	"sync"
	"time"
)

const (
	//小于平均数的
	tless = 0
	//灯具平均数的
	tok = 1
	//大于平均数的
	tplus = 2
	//远远大于平均数的
	tmore = 3

	//该日志对应的操作
	join  = "join"
	leave = "leave"
	move  = "move"
	query = "query"
)

type ShardMaster struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg

	// Your data here.
	configs []Config // indexed by config num
	//每个 group 对应的 shard 们
	g2shard map[int][]int
	//记录已经执行的clerk命令，避免重复执行
	clerkLog map[int64]int
	//消息通知
	msgCh map[int]chan struct{}
}

type Op struct {
	// Your data here.
	Clerk    int64
	CmdIndex int
	//操作 join leave move query
	Operation string
	//join
	Servers map[int][]string // new GID -> servers mappings
	//存放的是离开的的groupID
	GIDs []int
	//move
	//此时GID为GIDs[0]
	Shard int
	//query
	QueryNum int
}

func (sm *ShardMaster) Join(args *JoinArgs, reply *JoinReply) {
	// Your code here.
	raft.ShardInfo.Printf("ShardMaster:%2d join:%v\n", sm.me, args.Servers)
	op := Op{args.Clerk, args.Index, join, args.Servers, []int{}, 0, 0}
	reply.WrongLeader = sm.executeOp(op)
	return
}

func (sm *ShardMaster) Leave(args *LeaveArgs, reply *LeaveReply) {
	// Your code here.
	raft.ShardInfo.Printf("ShardMaster:%2d leave:%v\n", sm.me, args.GIDs)
	op := Op{args.Clerk, args.Index, leave, map[int][]string{}, args.GIDs, 0, 0}
	reply.WrongLeader = sm.executeOp(op)
	return
}

func (sm *ShardMaster) Move(args *MoveArgs, reply *MoveReply) {
	// Your code here.
	raft.ShardInfo.Printf("ShardMaster:%2d move shard:%v to gid:%v\n", sm.me, args.Shard, args.GID)
	op := Op{args.Clerk, args.Index, move, map[int][]string{}, []int{args.GID}, args.Shard, 0}
	reply.WrongLeader = sm.executeOp(op)
	return
}

func (sm *ShardMaster) Query(args *QueryArgs, reply *QueryReply) {
	// Your code here.
	//必须要同步
	//因为测试时，开始配置中11更新之后的query指令期待收到配置11
	//如果不同步就返回配置，可能配置11的日志还在同步中还未执行，因此返回配置10,因此出错
	op := Op{args.Clerk, args.Index, query, map[int][]string{}, []int{}, 0, args.Num}
	reply.WrongLeader = sm.executeOp(op)
	if !reply.WrongLeader {
		sm.mu.Lock()
		defer sm.mu.Unlock()
		if op.QueryNum == -1 || op.QueryNum > sm.configs[len(sm.configs)-1].Num {
			//所要求的config比拥有的config大，返回最新的config
			reply.Config = *sm.getLastCfg()
		} else {
			reply.Config = sm.configs[op.QueryNum]
		}
	}
	return
}
func (sm *ShardMaster) executeOp(op Op) (res bool) {
	sm.mu.Lock()
	//命令已经执行过了
	if index, ok := sm.clerkLog[op.Clerk]; ok {
		if index >= op.CmdIndex {
			sm.mu.Unlock()
			return false
		}
	}
	sm.mu.Unlock()
	index, _, isLeader := sm.rf.Start(op)
	if !isLeader {
		return true
	}
	//新建ch再放入msgCh的好处是，下面select直接用ch即可
	//而不是直接等待kv.msgCh[index]
	ch := make(chan struct{})
	sm.mu.Lock()
	sm.msgCh[index] = ch
	sm.mu.Unlock()
	select {
	case <-time.After(kvraft.WaitPeriod):
		raft.ShardInfo.Printf("Shard:%2d | operation %v index %d timeout!\n", sm.me, op.Operation, index)
		res = true
	case <-ch:
		raft.ShardInfo.Printf("Shard:%2d | operation %v index %d Done!\n", sm.me, op.Operation, index)
		res = false
	}
	go sm.closeCh(index)
	return
}

//
// the tester calls Kill() when a ShardMaster instance won't
// be needed again. you are not required to do anything
// in Kill(), but it might be convenient to (for example)
// turn off debug output from this instance.
//
func (sm *ShardMaster) Kill() {
	sm.rf.Kill()
	// Your code here, if desired.
	raft.ShardInfo.Printf("ShardMaster:%2d | I am died\n", sm.me)
}

// needed by shardkv tester
func (sm *ShardMaster) Raft() *raft.Raft {
	return sm.rf
}

//
// servers[] contains the ports of the set of
// servers that will cooperate via Paxos to
// form the fault-tolerant shardmaster service.
// me is the index of the current server in servers[].
//
func StartServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister) *ShardMaster {
	sm := new(ShardMaster)
	sm.me = me

	sm.configs = make([]Config, 1)
	sm.configs[0].Groups = map[int][]string{}

	labgob.Register(Op{})
	sm.applyCh = make(chan raft.ApplyMsg)
	sm.rf = raft.Make(servers, me, persister, sm.applyCh)

	// Your code here.
	sm.g2shard = make(map[int][]int)
	sm.msgCh = make(map[int]chan struct{})
	sm.clerkLog = make(map[int64]int)
	raft.ShardInfo.Printf("ShardMaster:%2d |Create a new shardmaster!\n", sm.me)
	go sm.run()
	return sm
}

//调整shard
func (sm *ShardMaster) navieAssign() {
	if len(sm.g2shard) == 0 {
		return
	}
	groupMap := [4][]int{}
	config := sm.getLastCfg()
	//存放要分配的shard
	shardToAssign := make([]int, 0)
	//shard : groupId
	for shard, gid := range config.Shards {
		if gid == 0 {
			//说明这是一个还没有倍分配的 shard
			shardToAssign = append(shardToAssign, shard)
		}
	}
	//说明 sm.g2shard = group的数量
	//这里算出来就是一个期望值呗
	share := NShards / len(sm.g2shard)
	//遍历每个group的shard数量 计算他们管理的shard  分配到不同的组
	for groupId, shards := range sm.g2shard {
		shardNum := len(shards)
		switch shardNum {
		case share:
			groupMap[tok] = append(groupMap[tok], groupId)
		case share + 1:
			groupMap[tplus] = append(groupMap[tplus], groupId)
		default:
			if shardNum < share {
				groupMap[tless] = append(groupMap[tless], groupId)
			} else {
				groupMap[tmore] = append(groupMap[tmore], groupId)
			}
		}
	}
	//先整治一下 超级多的
	////平衡shard数量分配过多的group
	for len(groupMap[tmore]) != 0 {
		//从头一个一个开始取出来
		//先取 groupId
		gid := groupMap[tmore][0]
		//再根据groupId 读取 shard
		moreShard := sm.getLastShard(gid)
		//读取后就 裁剪掉
		sm.g2shard[gid] = sm.cutG2shard(gid)
		//放到未分配
		shardToAssign = append(shardToAssign, moreShard)
		/*
			share + 1 = tplus
			因为这个循环 管理的是 tmore，tmore下一个级别是tplus,
			要一直把group 管理到 tplus 才算完。所以这里判断了一下
			只有把这个group 安排完了才截取，否则就是一直循环处理。
		*/
		if len(sm.g2shard[gid]) == share+1 {
			groupMap[tplus] = append(groupMap[tplus], gid)
			groupMap[tmore] = groupMap[tmore][1:]
		}
	}

	//开始处理待分配的shard
	for len(shardToAssign) != 0 {
		curShard := shardToAssign[0]
		shardToAssign = shardToAssign[1:]
		//先分到少的那部分
		if len(groupMap[tless]) != 0 {
			groupId := groupMap[tless][0]
			config.Shards[curShard] = groupId
			sm.g2shard[groupId] = append(sm.g2shard[groupId], curShard)
			//从 tless 里面 取出来的 groupId
			//在操作的时候只是读取，且重新分配shard （分给从tless里面读取出来的组），当分配完了到了平均值，就说明tless可以不用维护这个啦。
			if len(sm.g2shard[groupId]) == share {
				groupMap[tok] = append(groupMap[tok], groupId)
				groupMap[tless] = groupMap[tless][1:]
			}
		} else {
			//给tok的group分配shard
			//给 tok分配了 tok也就变成了 tplus 了
			groupId := groupMap[tok][0]
			config.Shards[curShard] = groupId
			sm.g2shard[groupId] = append(sm.g2shard[groupId], curShard)
			groupMap[tok] = groupMap[tok][1:]
			groupMap[tplus] = append(groupMap[tplus], groupId)
		}

	}

	//检查一下是否 还有分配不均的
	//把 plus  的分给 tless 的（如果有）
	for len(groupMap[tless]) != 0 {
		//一种极端的情况就是 所有平均分配 ，由于上面最开始就处理了 tmore
		//现在就只有 tless  和 tplus  了
		groupId := groupMap[tplus][0]
		groupMap[tplus] = groupMap[tplus][1:]
		//先取值 后裁剪
		plusShard := sm.getLastShard(groupId)
		sm.g2shard[groupId] = sm.cutG2shard(groupId)
		groupIdLess := groupMap[tless][0]
		//把这个多余的 shard 分配个空闲的 group
		config.Shards[plusShard] = groupIdLess
		sm.g2shard[groupIdLess] = append(sm.g2shard[groupIdLess], plusShard)
		if len(sm.g2shard[groupIdLess]) == share {
			groupMap[tless] = groupMap[tless][1:]
		}
	}
}
func (sm *ShardMaster) run() {
	for msg := range sm.applyCh {
		sm.mu.Lock()
		commandIndex := msg.CommandIndex
		op := msg.Command.(Op)
		if ind, ok := sm.clerkLog[op.Clerk]; ok && ind >= op.CmdIndex {
			//执行完了 不管
		} else {
			switch op.Operation {
			case join:
				needToAssign := false
				//配置文件编号自增
				sm.addConfig()
				lastConfig := sm.getLastCfg()
				for groupId, servers := range op.Servers {
					//初始化
					if _, ok := sm.g2shard[groupId]; !ok {
						//防止同样的server join两次
						needToAssign = true
						lastConfig.Groups[groupId] = servers
						sm.g2shard[groupId] = []int{}
					}
				}
				if needToAssign {
					sm.navieAssign()
					raft.ShardInfo.Printf("ShardMaster:%2d | new config:{%v}\n", sm.me, sm.getLastCfg())
				}
			case leave:
				needToAssign := false
				sm.addConfig()
				lastConfig := sm.getLastCfg()
				for _, groupId := range op.GIDs {
					if _, ok := sm.g2shard[groupId]; ok {
						needToAssign = true
						for _, shard := range sm.g2shard[groupId] {
							//0表示没有分配
							lastConfig.Shards[shard] = 0
						}
						delete(sm.g2shard, groupId)
						delete(lastConfig.Groups, groupId)
					}
				}
				if needToAssign {
					sm.navieAssign()
					raft.ShardInfo.Printf("ShardMaster:%2d | new config:{%v}\n", sm.me, sm.getLastCfg())
				}
			case move:
				sm.addConfig()
				lastConfig := sm.getLastCfg()
				oldGroupId := lastConfig.Shards[op.Shard]
				//旧集群移去这个shard
				for ind, shard := range sm.g2shard[oldGroupId] {
					if shard == op.Shard {
						sm.g2shard[oldGroupId] = append(sm.g2shard[oldGroupId][:ind], sm.g2shard[oldGroupId][ind+1:]...)
						break
					}
				}
				gid := op.GIDs[0]
				lastConfig.Shards[op.Shard] = gid
				sm.g2shard[gid] = append(sm.g2shard[gid], op.Shard)
			case query:

			}
			if ch, ok := sm.msgCh[commandIndex]; ok {
				//命令完成
				ch <- struct{}{}
			}
			sm.mu.Unlock()
		}
	}
}

//最新的配置文件
func (sm *ShardMaster) getLastCfg() *Config {
	return &sm.configs[len(sm.configs)-1]
}

//获取 group  最后一个 shard
func (sm *ShardMaster) getLastShard(gid int) int {
	//获取某个group对应的最后一个shard
	return sm.g2shard[gid][len(sm.g2shard[gid])-1]
}

//减去最后一个 shard
func (sm *ShardMaster) cutG2shard(gid int) []int {
	return sm.g2shard[gid][:len(sm.g2shard[gid])-1]
}

//配置文件 编号自增
func (sm *ShardMaster) addConfig() {
	//调用函数必须有锁
	//单纯复制config并加
	//不做额外操作
	oldCfg := sm.getLastCfg()
	newShard := oldCfg.Shards          //数组是值复制
	newGroup := make(map[int][]string) //map是引用复制
	for k, v := range oldCfg.Groups {
		newGroup[k] = v
	}
	sm.configs = append(sm.configs, Config{oldCfg.Num + 1, newShard, newGroup})
}
func (sm *ShardMaster) closeCh(index int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	close(sm.msgCh[index])
	delete(sm.msgCh, index)
}
