package kvraft

const (
	OK             = "OK"
	ErrNoKey       = "ErrNoKey"
	ErrWrongLeader = "ErrWrongLeader"
)

type Err string

// Put or Append
type PutAppendArgs struct {
	Key   string
	Value string
	Op    string // "Put" or "Append"
	// You'll have to add definitions here.
	// Field names must start with capital letters,
	// otherwise RPC will break.
	//客户端编号
	ClerkID int64
	//这个clerk发送的第几条信息
	CmdIndex int
}

//很可能请求的时候 leader 挂了，就打在了 follower 上
type PutAppendReply struct {
	WrongLeader bool
	Err         Err
}

type GetArgs struct {
	Key string
	// You'll have to add definitions here.
	//客户端编号
	ClerkID int64
	//这个clerk发送的第几条信息
	CmdIndex int
}

type GetReply struct {
	Err         Err
	Value       string
	WrongLeader bool
}
