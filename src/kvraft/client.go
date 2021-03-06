package kvraft

import (
	"crypto/rand"
	"raft/src/labrpc"
	"raft/src/raft"
)
import "math/big"

type Clerk struct {
	servers []*labrpc.ClientEnd
	// You will have to modify this struct.
	//最新的leader
	leader int
	//我自己的编号
	me int64
	//发送的第几条消息
	cmdIndex int64
}

//随机函数
func nrand() int64 {
	max := big.NewInt(int64(1) << 62)
	bigx, _ := rand.Int(rand.Reader, max)
	x := bigx.Int64()
	return x
}

func MakeClerk(servers []*labrpc.ClientEnd) *Clerk {
	ck := new(Clerk)
	ck.servers = servers
	// You'll have to add code here.
	ck.leader = 0
	//给自己整一个编号
	ck.me = nrand()
	ck.cmdIndex = 0
	raft.InfoKV.Printf("Client: %20v | 创造了以合new Clerk ! \n", ck.me)
	return ck
}

//获取一个key的当前值
//如果不存在，就返回 ""
//遇到其他的错误就不断的尝试
// you can send an RPC with code like this:
// ok := ck.servers[i].Call("KVServer.Get", &args, &reply)
//
// the types of args and reply (including whether they are pointers)
// must match the declared types of the RPC handler function's
// arguments. and reply must be passed as a pointer.
//
func (ck *Clerk) Get(key string) string {
	// You will have to modify this function.
	ck.cmdIndex++
	args := GetArgs{Key: key, ClerkID: ck.me, CmdIndex: ck.cmdIndex}
	leader := ck.leader
	raft.InfoKV.Printf("Client:%20v cmdIndex:%4d | Begin ! Get:[%v] from server: %3d\n", ck.me, ck.cmdIndex, key, leader)

	for {
		reply := GetReply{}
		ok := ck.servers[leader].Call("KVServer.Get", &args, &reply)
		if ok && !reply.WrongLeader {
			ck.leader = leader
			//不存在就返回  ""
			if reply.Value == ErrNoKey {
				return ""
			}
			raft.InfoKV.Printf("Client:%20v cmdIndex:%4d| Successful! Get:[%v] from server:%3d value:[%v]\n", ck.me, ck.cmdIndex, key, leader, reply.Value)
			return reply.Value
		}
		//寻找下一个leader
		leader = (leader + 1) % len(ck.servers)
	}
}

//
// shared by Put and Append.
//
// you can send an RPC with code like this:
// ok := ck.servers[i].Call("KVServer.PutAppend", &args, &reply)
//
// the types of args and reply (including whether they are pointers)
// must match the declared types of the RPC handler function's
// arguments. and reply must be passed as a pointer.
//
func (ck *Clerk) PutAppend(key string, value string, op string) {
	// You will have to modify this function.
	ck.cmdIndex++
	args := PutAppendArgs{
		Key:      key,
		Value:    value,
		Op:       op,
		ClerkID:  ck.me,
		CmdIndex: ck.cmdIndex,
	}
	leader := ck.leader
	raft.InfoKV.Printf("Client:%20v cmdIndex:%4d| Begin! %6s key:[%s] value:[%s] to server:%3d\n", ck.me, ck.cmdIndex, op, key, value, leader)
	for {
		reply := PutAppendReply{}
		ok := ck.servers[leader].Call("KVServer.PutAppend", &args, &reply)
		if ok && reply.WrongLeader == false && reply.Err == OK {
			raft.InfoKV.Printf("Client:%20v cmdIndex:%4d| Successful! %6s key:[%s] value:[%s] to server:%3d\n", ck.me, ck.cmdIndex, op, key, value, leader)
			ck.leader = leader
			return
		}
		//如果不是 尝试下一个 是不是leader
		leader = (leader + 1) % len(ck.servers)
	}
}

func (ck *Clerk) Put(key string, value string) {
	ck.PutAppend(key, value, "Put")
}
func (ck *Clerk) Append(key string, value string) {
	ck.PutAppend(key, value, "Append")
}
