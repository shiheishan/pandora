package adminops

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 批量生成账号是后台任务（方案 A）：Argon2 只在事务外算，且一次只占 1 个全局名额、算完即还
// （generateCredentialWithSlot），名额排不上就等，不让任务失败；写库的事务里不算哈希。
func TestUserGenerationHashesOneSlotAtATimeOutsideTransactions(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	process := pkg.Decl("UserGenerationWorker.process")
	hashAt := strings.Index(process, "w.newCredential(ctx)")
	writeAt := strings.Index(process, "w.writeBatch(ctx,")
	if hashAt < 0 || writeAt < 0 || hashAt > writeAt {
		t.Fatal("the worker must hash a batch before opening its write transaction")
	}
	for _, decl := range []string{pkg.Decl("UserGenerationWorker.writeBatch"), pkg.Decl("insertGeneratedUsers")} {
		if strings.Contains(decl, "HashPassword") || strings.Contains(decl, ".Hash(") || strings.Contains(decl, "newCredential") {
			t.Fatal("no Argon2 hashing may run inside the generation transaction")
		}
	}
	gen := pkg.Decl("generateCredentialWithSlot")
	acquire := strings.Index(gen, "crypto.AcquirePasswordSlot(ctx)")
	hash := strings.Index(gen, "slot.Hash(")
	release := strings.Index(gen, "slot.Release()")
	if acquire < 0 || hash < acquire || release < hash || strings.Count(gen, "AcquirePasswordSlot") != 1 {
		t.Fatal("each credential must take exactly one global slot, hash, then release it")
	}
	if !strings.Contains(gen, "crypto.ErrPasswordHashBusy") {
		t.Fatal("a busy slot must make the worker wait, not fail the job")
	}
	if strings.Contains(gen, "crypto.HashPassword(") {
		t.Fatal("the package-level HashPassword waits without a queue timeout; use a slot")
	}
}

// 每批的用户、口令、进度与结果密文在同一个事务里写，且先确认租约还是自己这一代的。
func TestUserGenerationBatchIsAtomicWithProgress(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("UserGenerationWorker.writeBatch")
	lease := strings.Index(body, "attempts = $3 AND completed = $4")
	insert := strings.Index(body, "insertGeneratedUsers(ctx, tx,")
	progress := strings.Index(body, "SET completed = $3, result_encrypted = $4")
	if lease < 0 || insert < lease || progress < insert {
		t.Fatalf("lease=%d insert=%d progress=%d: lease check, users and progress must share one transaction in order",
			lease, insert, progress)
	}
}
