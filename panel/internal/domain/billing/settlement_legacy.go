// [INPUT]: 无（只有注释，不参与编译出的任何声明）
// [OUTPUT]: 无
// [POS]: billing 的历史源码存档：预留图之前的旧版支付回调实现，原样保存在块注释里供对照审阅（从 checkout.go 挪来，HandlePaymentWebhook 文档里说的「above」即指这段）；现行结算主链在 settlement.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

// HandlePaymentWebhook 处理支付成功回调。
//
// PAY-003 验收「同一回调重复 100 次只产生一次业务结果」的实现路径：
// 第一步就往 payment_events 插入 (provider_id, provider_event_id)。
// 该组合有唯一约束，第 2..100 次直接撞约束返回 AlreadyHandled，
// 后面的记账与订阅激活根本不会执行。判重发生在数据库，而非应用的 if。
/* legacy pre-reservation settlement retained temporarily for review context
/*
func (s *Service) handlePaymentWebhookLegacy(ctx context.Context, tenantID string, in PaymentWebhookInput) (*PaymentWebhookOutput, error) {
	var out PaymentWebhookOutput
	scope := db.Scope{TenantID: tenantID}

	err := s.pool.InTx(ctx, scope, func(tx pgx.Tx) error {
		var providerID string
		err := tx.QueryRow(ctx,
			`SELECT id FROM payment_providers WHERE tenant_id = $1 AND code = $2`,
			tenantID, in.ProviderCode).Scan(&providerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeNotFound, "未知的支付渠道")
		}
		if err != nil {
			return err
		}

		// --- 幂等闸门 ---
		// 同样必须走 ON CONFLICT DO NOTHING：若让唯一约束直接抛错，
		// 事务会变成 aborted，连提交都会失败，重复回调就会返回 500 而不是
		// 「已处理」。用零行返回表达冲突，事务始终健康。
		var eventID string
		err = tx.QueryRow(ctx, `
			INSERT INTO payment_events
				(tenant_id, provider_id, provider_event_id, event_type,
				 provider_payment_id, raw_payload, signature_verified)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (provider_id, provider_event_id) DO NOTHING
			RETURNING id`,
			tenantID, providerID, in.ProviderEventID, in.EventType,
			nullStr(in.ProviderPaymentID), in.RawPayload, in.SignatureVerified,
		).Scan(&eventID)

		if errors.Is(err, pgx.ErrNoRows) {
			out.AlreadyHandled = true
			return nil
		}
		if err != nil {
			return err
		}

		if !in.SignatureVerified {
			_, _ = tx.Exec(ctx, `
				UPDATE payment_events
				   SET processing_status = 'ignored',
				       processing_error = '签名校验未通过',
				       processed_at = now()
				 WHERE id = $1`, eventID)
			return httpx.New(httpx.CodeUnauthorized, "回调签名校验失败")
		}

		if in.EventType != "payment.succeeded" {
			_, _ = tx.Exec(ctx,
				`UPDATE payment_events SET processing_status = 'ignored', processed_at = now()
				  WHERE id = $1`, eventID)
			out.Processed = true
			return nil
		}

		// --- 取订单并加锁 ---
		var (
			orderID        string
			userID         string
			status         string
			currency       string
			payable        int64
			orderKind      string
			balanceApplied int64
			totalAmount    int64
		)
		// 按内部 UUID 或对外短单号定位。两条路径都在事务内取行锁，
		// 保证并发回调只有一个能推进订单状态。
		if in.OrderID != "" {
			err = tx.QueryRow(ctx, `
				SELECT id, user_id, status, currency, payable_amount, balance_applied,
				       total_amount, kind
				  FROM orders WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
				tenantID, in.OrderID).Scan(&orderID, &userID, &status, &currency,
				&payable, &balanceApplied, &totalAmount, &orderKind)
		} else if in.OrderNo != "" {
			err = tx.QueryRow(ctx, `
				SELECT id, user_id, status, currency, payable_amount, balance_applied,
				       total_amount, kind
				  FROM orders WHERE tenant_id = $1 AND order_no = $2 FOR UPDATE`,
				tenantID, in.OrderNo).Scan(&orderID, &userID, &status, &currency,
				&payable, &balanceApplied, &totalAmount, &orderKind)
		} else {
			return httpx.New(httpx.CodeBadRequest, "回调未携带订单标识")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeNotFound, "订单不存在")
		}
		if err != nil {
			return err
		}
		in.OrderID = orderID

		// PAY-001：订单已完成时不重复处理，但事件仍算已消费
		if status == "paid" || status == "fulfilled" {
			_, _ = tx.Exec(ctx,
				`UPDATE payment_events SET processing_status = 'ignored', processed_at = now()
				  WHERE id = $1`, eventID)
			out.AlreadyHandled = true
			return nil
		}
		if in.Currency != currency {
			return httpx.New(httpx.CodeConflict, "回调币种与订单不一致")
		}
		if in.Amount != payable {
			return httpx.New(httpx.CodeConflict,
				fmt.Sprintf("回调金额与应付金额不符（应付 %d，收到 %d）", payable, in.Amount))
		}

		// --- 终结支付意图 ---
		// 必须在插 payments 之前做：payment_intents 上有「一个订单只允许一个
		// 未终结意图」的部分唯一索引，留着 requires_action 会让这张单永远
		// 卡在「有在途支付」的状态，用户换渠道重付时被误判为重复。
		var intentID *string
		if err := tx.QueryRow(ctx, `
			UPDATE payment_intents
			   SET status = 'succeeded',
			       provider_ref = coalesce(provider_ref, $3)
			 WHERE tenant_id = $1 AND order_id = $2
			   AND status IN ('created', 'requires_action', 'processing')
			RETURNING id`,
			tenantID, in.OrderID, nullStr(in.ProviderPaymentID),
		).Scan(&intentID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// 查询补偿路径（PAY-009）可能没有任何在途意图，属正常情况，不报错。

		// --- 记录支付 ---
		var paymentID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO payments
				(tenant_id, order_id, provider_id, provider_payment_id,
				 payment_intent_id, currency, amount, fee_amount, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'succeeded')
			RETURNING id`,
			tenantID, in.OrderID, providerID, in.ProviderPaymentID,
			intentID, currency, in.Amount, in.FeeAmount).Scan(&paymentID); err != nil {
			return err
		}
		out.PaymentID = paymentID

		// --- 记账（PAY-005）---
		// 充值不是收入，而是平台对用户的负债。它必须在这里直接生成唯一一笔
		// 充值分录，不能先按商品订单确认收入、再在履约阶段重复确认渠道现金。
		var txnID string
		if orderKind == "topup" {
			txnID, err = s.postTopupPaid(ctx, tx, tenantID, topupPaidPosting{
				OrderID:      in.OrderID,
				UserID:       userID,
				Currency:     currency,
				Amount:       in.Amount,
				FeeAmount:    in.FeeAmount,
				ProviderCode: in.ProviderCode,
			})
		} else {
			txnID, err = s.postOrderPaid(ctx, tx, tenantID, orderPaidPosting{
				OrderID:        in.OrderID,
				UserID:         userID,
				Currency:       currency,
				ChannelAmount:  in.Amount,
				BalanceApplied: balanceApplied,
				FeeAmount:      in.FeeAmount,
				TotalAmount:    totalAmount,
				ProviderCode:   in.ProviderCode,
			})
		}
		if err != nil {
			return err
		}
		out.LedgerTxnID = txnID

		// --- 订单状态推进 ---
		if _, err := tx.Exec(ctx, `
			UPDATE orders
			   SET status = 'paid', paid_amount = $3, paid_at = now()
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID, in.OrderID, totalAmount); err != nil {
			return err
		}

		// --- 履约 ---
		//
		// 充值单和购买单走的是同一条支付链路，区别只在这一步：
		// 买套餐需要履约；充值的余额增加已经包含在上面的唯一一笔支付分录中。
		// 拿充值单去跑 fulfillOrder 会因为找不到订单行上的套餐快照而失败。
		switch orderKind {
		case "topup":
			// 无额外履约；记账与余额增加已原子完成。
		case "renewal":
			// 续费落在已有订阅上：延长周期、重置周期配额，不新建订阅。
			// 走 fulfillOrder 会凭空多出第二条订阅，用户会看到两个订阅链接
			subID, err := s.fulfillRenewal(ctx, tx, tenantID, in.OrderID, userID)
			if err != nil {
				return err
			}
			out.SubscriptionID = subID
		default:
			subID, err := s.fulfillOrder(ctx, tx, tenantID, in.OrderID, userID)
			if err != nil {
				return err
			}
			out.SubscriptionID = subID
		}

		if err := plugin.EmitOrderPaid(ctx, tx, tenantID, in.OrderID, userID,
			orderKind, currency, totalAmount, out.SubscriptionID); err != nil {
			return err
		}
		if out.SubscriptionID != "" {
			if err := plugin.EmitSubscriptionProvisioned(ctx, tx, tenantID,
				out.SubscriptionID, userID, in.OrderID, orderKind); err != nil {
				return err
			}
		}

		// 充值不产生佣金：那只是把钱换个地方放，还没有产生任何消费。
		// 给充值计提等于同一笔钱在充值和下单时被算两次分成。
		if orderKind != "topup" {
			// 分销佣金按订单成交额计提，不是按渠道实收。
			// 用余额支付的部分同样是真金白银，只是先前已经进过账 ——
			// 按渠道实收算会让「用余额买」的推荐一分钱佣金都拿不到。
			if err := s.accrueCommission(ctx, tx, tenantID, in.OrderID, userID,
				currency, totalAmount); err != nil {
				return err
			}
		}

		if _, err := tx.Exec(ctx, `
			UPDATE payment_events
			   SET processing_status = 'processed', processed_at = now()
			 WHERE id = $1`, eventID); err != nil {
			return err
		}

		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "system",
			Action:    "payment.succeeded", ResourceType: "order", ResourceID: &in.OrderID,
			AfterDigest: map[string]any{
				"payment_id": paymentID, "amount": in.Amount,
				"currency": currency, "ledger_txn": txnID,
				// 充值单没有订阅，这里会是空串 —— 比塞一个假 ID 诚实
				"subscription_id": out.SubscriptionID, "order_kind": orderKind,
			},
			APIDomain: "public", RequestID: httpx.RequestIDFrom(ctx),
		}); err != nil {
			return err
		}

		out.Processed = true
		return nil
	})

	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, he
		}
		return nil, httpx.Internal(err)
	}
	return &out, nil
}

*/
