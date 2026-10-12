package service

import (
	"bytes"
	"context"
	"errors"
	"sort"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// maxPackBytes 是一笔流量包的上限（1 PiB），与 catalog 流量包的上限一致；后台加流量另有 10 TiB 的上限（admin.go）。
const maxPackBytes int64 = 1 << 50

// GrantTrafficPack 发一笔流量包余额，是 order、giftcard、后台共用的发放入口。
//
// 挂到订阅上（SubscriptionID 非 nil）时，订阅必须属于该用户（否则 ErrNotFound）；是否要求生效中按来源分：
//
//	PackSourceOrder      只校验归属，不校验是否生效：钱已经收了、包不过期，付款窗口里订阅刚好到期也照发，
//	                     续费后照样能用，停用后也能转走（与旧的流量包订单履约一致）。下单前用 CheckPackTarget 拦
//	PackSourceGiftCard、PackSourceAdmin  必须生效中，否则 ErrPackNeedsLive
//
// SubscriptionID 为 nil 时留作未分配（无订阅时兑换的送流量卡），下次开通唯一一份生效订阅时自动挂上。
//
// 防重：(Source, SourceID) 唯一。这里先查再插，已发过回 ErrPackDuplicate（409），调用方事务仍可用，
// 可以当作重放处理；并发的两笔同来源发放由调用方先锁订单或卡密串行，万一撞上库里的唯一约束，
// 返回同样的 409，但那时调用方事务已作废，只能整体回滚。
// 在 ctx 的事务里执行；挂到订阅上的提交后通知名单变化。
func (s *Service) GrantTrafficPack(ctx context.Context, in PackGrant) (Pack, error) {
	if in.UserID == uuid.Nil || in.SourceID == uuid.Nil {
		return Pack{}, errs.Wrap(errs.Internal, errMissing("UserID / SourceID"))
	}
	switch in.Source {
	case PackSourceOrder, PackSourceGiftCard, PackSourceAdmin:
	default:
		return Pack{}, errs.Wrap(errs.Internal, errMissing("Source"))
	}
	if in.Bytes < 1 || in.Bytes > maxPackBytes {
		return Pack{}, errs.Invalid(map[string]string{"bytes": "流量须在 1 字节到 1 PiB 之间"})
	}
	var out Pack
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if in.SubscriptionID != nil {
			m, err := s.loadSub(ctx, *in.SubscriptionID, &in.UserID, repo.ForNoKeyUpdate)
			if err != nil {
				return err
			}
			if in.Source != PackSourceOrder && StateAt(m.Status, m.CurrentPeriodEnd, m.RenewalClosedAt, s.clock()) != StateLive {
				return ErrPackNeedsLive
			}
		}
		dup, err := s.repo.PackGrantExists(ctx, in.Source, in.SourceID)
		if err != nil {
			return subErr(err)
		}
		if dup {
			return ErrPackDuplicate
		}
		g := model.PackGrant{
			UserID:         in.UserID,
			SubscriptionID: in.SubscriptionID,
			Source:         in.Source,
			SourceID:       in.SourceID,
			GrantedBytes:   in.Bytes,
		}
		if err := s.repo.InsertPackGrant(ctx, &g); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return ErrPackDuplicate
			}
			return subErr(err)
		}
		if in.SubscriptionID != nil {
			s.notifyRoster(ctx, *in.SubscriptionID)
		}
		out = toPack(g)
		return nil
	})
	return out, err
}

// CheckPackTarget 校验流量包订单的目标：订阅属于该用户且生效中；给 order 报价、下单时用（不加锁）。
func (s *Service) CheckPackTarget(ctx context.Context, userID, subID uuid.UUID) error {
	m, err := s.loadSub(ctx, subID, &userID, repo.NoLock)
	if err != nil {
		return err
	}
	if StateAt(m.Status, m.CurrentPeriodEnd, m.RenewalClosedAt, s.clock()) != StateLive {
		return ErrPackNeedsLive
	}
	return nil
}

// TransferPacks 把用户「未分配」（From 为 nil）或一份已停用订阅上还有剩余的流量包，整笔挂到同一用户一份没停用的订阅上，
// 每笔写一条转移流水；返回挪过去的剩余字节。目标已停用回 ErrTransferTarget，来源还在用回 ErrTransferSource。
// 在 ctx 的事务里执行，按 id 顺序锁两份订阅；提交后通知名单变化。
func (s *Service) TransferPacks(ctx context.Context, in Transfer) (int64, error) {
	if in.UserID == uuid.Nil {
		return 0, errs.Wrap(errs.Internal, errMissing("UserID"))
	}
	if err := validateActor(in.Actor); err != nil {
		return 0, err
	}
	if in.Actor.Kind != ActorUser && in.Actor.Kind != ActorAdmin && in.Actor.Kind != ActorSystem {
		return 0, errs.Wrap(errs.Internal, errMissing("user/admin/system Actor"))
	}
	if in.From != nil && *in.From == in.To {
		return 0, errs.Invalid(map[string]string{"to_subscription_id": "来源与目标不能是同一份"})
	}
	var moved int64
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		ids := []uuid.UUID{in.To}
		if in.From != nil {
			ids = append(ids, *in.From)
		}
		sort.Slice(ids, func(i, j int) bool { return bytes.Compare(ids[i][:], ids[j][:]) < 0 })
		locked := make(map[uuid.UUID]model.Subscription, len(ids))
		for _, id := range ids {
			m, err := s.lockOwned(ctx, id, &in.UserID)
			if err != nil {
				return err
			}
			locked[id] = m
		}
		now := s.clock()
		to := locked[in.To]
		if StateAt(to.Status, to.CurrentPeriodEnd, to.RenewalClosedAt, now) == StateEnded {
			return ErrTransferTarget
		}
		if in.From != nil {
			from := locked[*in.From]
			if StateAt(from.Status, from.CurrentPeriodEnd, from.RenewalClosedAt, now) != StateEnded {
				return ErrTransferSource
			}
		}
		n, err := s.movePacks(ctx, in.UserID, in.From, in.To, in.Actor)
		if err != nil {
			return err
		}
		moved = n
		if n > 0 {
			if in.From != nil {
				s.notifyRoster(ctx, *in.From)
			}
			s.notifyRoster(ctx, in.To)
		}
		return nil
	})
	return moved, err
}

// movePacks 把 from（nil 为未分配）下还有剩余的流量包整笔挂到 to 上并写转移流水；返回挪过去的剩余字节。
// 调用方已锁住相关订阅；这里按创建先后锁流量包行。
func (s *Service) movePacks(ctx context.Context, userID uuid.UUID, from *uuid.UUID, to uuid.UUID, actor Actor) (int64, error) {
	grants, err := s.repo.OpenPackGrants(ctx, userID, from, true)
	if err != nil {
		return 0, subErr(err)
	}
	if len(grants) == 0 {
		return 0, nil
	}
	ids := make([]uuid.UUID, 0, len(grants))
	transfers := make([]model.PackTransfer, 0, len(grants))
	var moved int64
	for _, g := range grants {
		left := g.GrantedBytes - g.ConsumedBytes
		ids = append(ids, g.ID)
		moved += left
		transfers = append(transfers, model.PackTransfer{
			GrantID:            g.ID,
			UserID:             userID,
			FromSubscriptionID: from,
			ToSubscriptionID:   to,
			RemainingBytes:     left,
			ActorKind:          actor.Kind,
			ActorID:            actor.ID,
		})
	}
	if err := s.repo.AttachPackGrants(ctx, ids, to); err != nil {
		return 0, subErr(err)
	}
	if err := s.repo.InsertPackTransfers(ctx, transfers); err != nil {
		return 0, subErr(err)
	}
	return moved, nil
}
