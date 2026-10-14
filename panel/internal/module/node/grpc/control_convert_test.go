package grpc

import (
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/aegispanel/aegis/internal/module/node/service"
)

// sendBufferTier 是 gRPC 发送缓冲在 1 MB 之下的最大一档（mem 包的缺省档位）：编码后超过它就占一块 1 MB 的缓冲。
const sendBufferTier = 32 << 10

// 每个字段都取最长编码时，一块满员的全量名单也放得进 32 KB 那一档（UsersChunkSize 的注释）。
func TestUsersFullChunkFitsSendBuffer(t *testing.T) {
	users := make([]service.RosterUser, service.UsersChunkSize+1)
	for i := range users {
		users[i] = service.RosterUser{
			UID: math.MaxInt64, UUID: uuid.New(), SpeedLimitKbps: math.MaxUint32, DeviceLimit: math.MaxUint32,
		}
	}
	r := &service.NodeRoster{NodeID: uuid.New(), Version: "u1-" + strings.Repeat("f", 64), Users: users}
	var sizes []int
	for msg := range usersFullChunks(r) {
		sizes = append(sizes, proto.Size(msg))
	}
	if len(sizes) != 2 {
		t.Fatalf("块数 %d，应为 2", len(sizes))
	}
	if sizes[0] > sendBufferTier {
		t.Fatalf("满员一块编码后 %d 字节，超过 %d", sizes[0], sendBufferTier)
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
