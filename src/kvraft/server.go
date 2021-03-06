package kvraft

import (
	"log"
	"raft/src/labgob"
	"raft/src/labrpc"
	"raft/src/raft"
	"sync"
	"time"
)

const (
	Debug      = 0
	WaitPeriod = time.Duration(1000) * time.Millisecond //请求响应的等待超时时间
)

func DPrintf(format string, a ...interface{}) (n int, err error) {
	if Debug > 0 {
		log.Printf(format, a...)
	}
	return
}

type Op struct {
	// Your definitions here.
	// Field names must start with capital letters,
	// otherwise RPC will break.
	Method   string
	Key      string
	Value    string
	Clerk    int64
	CmdIndex int64
}

type KVServer struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg
	//dead    int32 // set by Kill()

	maxraftstate int // snapshot if log grows this big

	// Your definitions here.
	//每一个clerk 执行的 任务编号 劳资就是int64
	clerkLog map[int64]int64
	//kv
	kvDB map[string]string
	//消息 通知的管道
	msgCh map[int]chan int

	persister *raft.Persister
}

func (kv *KVServer) Get(args *GetArgs, reply *GetReply) {
	op := Op{"Get", args.Key, "", args.ClerkID, args.CmdIndex}
	reply.Err = ErrNoKey
	reply.WrongLeader = true
	index, term, isLeader := kv.rf.Start(op)
	if !isLeader {
		return
	}
	kv.mu.Lock()
	if ind, ok := kv.clerkLog[args.ClerkID]; ok && ind >= args.CmdIndex {
		//该指令已经执行
		reply.Value = kv.kvDB[args.Key]
		kv.mu.Unlock()
		reply.WrongLeader = false
		reply.Err = OK
		return
	}
	//否则就是 没执行呗
	_, _ = raft.DPrintf("KVServer:%2d | leader msgIndex:%4d\n", kv.me, index)
	//新建ch再放入msgCh的好处是，下面select直接用ch即可
	//而不是直接等待kv.msgCh[index]
	ch := make(chan int)
	kv.msgCh[index] = ch
	kv.mu.Unlock()
	select {
	case <-time.After(WaitPeriod):
		//超时啊
		_, _ = raft.DPrintf("KVServer:%2d |Get {index:%4d term:%4d} failed! Timeout!\n", kv.me, index, term)
		//哥哥 回复我啦
	case msgTerm := <-ch:
		if msgTerm == term {
			kv.mu.Lock()
			_, _ = raft.DPrintf("KVServer:%2d | Get {index:%4d term:%4d} OK!\n", kv.me, index, term)
			//有值 就给你吗
			if val, ok := kv.kvDB[args.Key]; ok {
				reply.Value = val
				reply.Err = OK
			}
			kv.mu.Unlock()
			reply.WrongLeader = false
		} else {
			_, _ = raft.DPrintf("KVServer:%2d |Get {index:%4d term:%4d} failed! Not leader any more!\n", kv.me, index, term)
		}

	}
	go func() { kv.closeCh(index) }()
	return
}

func (kv *KVServer) PutAppend(args *PutAppendArgs, reply *PutAppendReply) {
	// Your code here.
	op := Op{args.Op, args.Key, args.Value, args.ClerkID, args.CmdIndex}
	reply.Err = OK
	kv.mu.Lock()

	//follower收到已经执行的put append请求，直接返回
	if ind, ok := kv.clerkLog[args.ClerkID]; ok && ind >= args.CmdIndex {
		//该指令已经执行
		kv.mu.Unlock()
		reply.WrongLeader = false
		return
	}
	kv.mu.Unlock()

	index, term, isLeader := kv.rf.Start(op)
	if !isLeader {
		reply.WrongLeader = true
		return
	}

	//start之后才做这个有问题
	//start，表示命令已经提交给raft实例
	//如果下面apply那里一直占用kv.mu.lock()
	//这里一直得不到kv.mu.Lock()，显然，当下面通过管道交付时，会发现管道根本就不存在！然后就死锁了。

	//记得改！！必须先申请管道，然后再start，如果start失败再删除管道！！
	//申请管道需要start的index，不能先申请管道
	//但是把start和申请管道放在同一个锁里，就能保证start和申请管道原子操作
	//start是调用本机的其他程序， 不是RPC调用，放在一个锁里
	kv.mu.Lock()
	_, _ = raft.DPrintf("KVServer:%2d | leader msgIndex:%4d\n", kv.me, index)
	ch := make(chan int)
	kv.msgCh[index] = ch
	kv.mu.Unlock()

	reply.WrongLeader = true
	select {
	case <-time.After(WaitPeriod):
		//超时
		_, _ = raft.DPrintf("KVServer:%2d | Put {index:%4d term:%4d} Failed, timeout!\n", kv.me, index, term)
	case msgTerm := <-ch:
		if msgTerm == term {
			//命令执行，或者已经执行过了
			_, _ = raft.DPrintf("KVServer:%2d | Put {index:%4d term:%4d} OK!\n", kv.me, index, term)
			reply.WrongLeader = false
		} else {
			_, _ = raft.DPrintf("KVServer:%2d | Put {index:%4d term:%4d} Failed, not leader!\n", kv.me, index, term)
		}
	}
	go func() { kv.closeCh(index) }()
	return
}

//
// the tester calls Kill() when a KVServer instance won't
// be needed again. for your convenience, we supply
// code to set rf.dead (without needing a lock),
// and a killed() method to test rf.dead in
// long-running loops. you can also add your own
// code to Kill(). you're not required to do anything
// about this, but it may be convenient (for example)
// to suppress debug output from a Kill()ed instance.
//
func (kv *KVServer) Kill() {
	kv.rf.Kill()
	// Your code here, if desired.
	kv.mu.Lock()
	_, _ = raft.DPrintf("KVServer:%2d | KV server is died!\n", kv.me)
	kv.mu.Unlock()
	//底层rf删掉之后，上层的kv server不再交流和更新信息，相当于挂了，所以不用做任何事情
}

/*func (kv *KVServer) killed() bool {
	z := atomic.LoadInt32(&kv.dead)
	return z == 1
}*/
func (kv *KVServer) closeCh(index int) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	close(kv.msgCh[index])
	delete(kv.msgCh, index)
}

//
// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant key/value service.
// me is the index of the current server in servers[].
// the k/v server should store snapshots through the underlying Raft
// implementation, which should call persister.SaveStateAndSnapshot() to
// atomically save the Raft state along with the snapshot.
// the k/v server should snapshot when Raft's saved state exceeds maxraftstate bytes,
// in order to allow Raft to garbage-collect its log. if maxraftstate is -1,
// you don't need to snapshot.
// StartKVServer() must return quickly, so it should start goroutines
// for any long-running work.
//
func StartKVServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxraftstate int) *KVServer {
	// call labgob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.
	labgob.Register(Op{})

	kv := new(KVServer)
	kv.me = me
	kv.maxraftstate = maxraftstate

	// You may need initialization code here.
	kv.applyCh = make(chan raft.ApplyMsg)
	kv.rf = raft.Make(servers, me, persister, kv.applyCh)
	// You may need initialization code here.

	kv.kvDB = make(map[string]string)
	//我就是 int64
	kv.clerkLog = make(map[int64]int64)
	kv.msgCh = make(map[int]chan int)
	kv.persister = persister
	kv.loadSnapshot()
	raft.DPrintf("KVServer:%2d | Create New KV server!\n", kv.me)
	go kv.receiveNewMsg()
	return kv
}
func (kv *KVServer) loadSnapshot() {
	data := kv.persister.ReadSnapshot()
	if data == nil || len(data) == 0 {
		return
	}
	kv.decodedSnapshot(data)
}
func (kv *KVServer) decodedSnapshot(data []byte) {

}
func (kv *KVServer) receiveNewMsg() {
	for msg := range kv.applyCh {
		kv.mu.Lock()
		//按需执行指令
		index := msg.CommandIndex
		term := msg.commi
	}
}
