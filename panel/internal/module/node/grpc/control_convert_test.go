package grpc

import (
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/proto"

	"github.com/aegispanel/aegis/internal/module/node/service"
)

// 每个字段都取最长编码（负 UID 的 varint 10 字节、seq 取最大）时，每一块满员的全量名单都不落进 gRPC 1 MB 那一档发送缓冲
// （UsersChunkSize 的注释）。档位直接问 gRPC 的缺省缓冲池，升级 grpc-go 改了档位这里会发现。
func TestUsersFullChunkFitsSendBuffer(t *testing.T) {
	users := make([]service.RosterUser, 2*service.UsersChunkSize)
	for i := range users {
		users[i] = service.RosterUser{
			UID: math.MinInt64, UUID: uuid.New(), SpeedLimitKbps: math.MaxUint32, DeviceLimit: math.MaxUint32,
		}
	}
	r := &service.NodeRoster{NodeID: uuid.New(), Version: "u1-" + strings.Repeat("f", 64), Users: users}
	var chunks int
	for msg := range usersFullChunks(r) {
		chunks++
		msg.Seq = math.MaxUint64
		size := proto.Size(msg)
		buf := mem.DefaultBufferPool().Get(size)
		got := cap(*buf)
		mem.DefaultBufferPool().Put(buf)
		if got >= 1<<20 {
			t.Fatalf("第 %d 块编码后 %d 字节，占了 %d 字节的发送缓冲", chunks, size, got)
		}
	}
	if chunks != 2 {
		t.Fatalf("块数 %d，应为 2", chunks)
	}
}

// 空名单也发一块；中途不要了（发送失败）就不再转后面的块（迭代器在 yield 返回 false 后还调会 panic）。
func TestUsersFullChunksEmptyAndStop(t *testing.T) {
	var n int
	for msg := range usersFullChunks(&service.NodeRoster{NodeID: uuid.New(), Version: "u1-empty"}) {
		if uf := msg.GetUsersFull(); uf.GetChunkTotal() != 1 || len(uf.GetUsers()) != 0 {
			t.Fatalf("空名单块：total=%d users=%d", uf.GetChunkTotal(), len(uf.GetUsers()))
		}
		n++
	}
	if n != 1 {
		t.Fatalf("空名单发了 %d 块", n)
	}
	r := &service.NodeRoster{NodeID: uuid.New(), Version: "u1-big", Users: make([]service.RosterUser, 3*service.UsersChunkSize)}
	n = 0
	for range usersFullChunks(r) {
		n++
		break
	}
	if n != 1 {
		t.Fatalf("break 后还在出块：%d", n)
	}
}
