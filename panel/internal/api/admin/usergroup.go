package admin

// 用户分组。
//
// 分组本身很简单，值钱的是它能驱动的四件事：
//
//	套餐可见   visibility='group' 的套餐只对组内用户出现
//	专属价格   同一个套餐给不同组不同的价（代理价、老用户价）
//	优惠券     券可以限定只有某几个组能用
//	公告       通知只发给相关的人
//
// 这四处的字段在数据库里一直都在，只是没有组可填，所以全是死的。

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type userGroupRow struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Name  string `json:"name"`
	Desc  string `json:"description"`
	Users int    `json:"users"`
	// 下面三个是「这个组正在被谁用」。删组之前要让人看见影响面，
	// 而不是删完才发现有套餐从此没人看得到
	Plans   int `json:"plans"`
	Prices  int `json:"prices"`
	Coupons int `json:"coupons"`
}

func (h *handlers) listUserGroups(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	out := []userGroupRow{}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT g.id::text, g.code, g.name, COALESCE(g.description,''),
			       (SELECT count(*) FROM users u WHERE u.user_group_id = g.id),
			       (SELECT count(*) FROM plans p WHERE g.id = ANY(p.visible_group_ids)),
			       (SELECT count(*) FROM prices pr WHERE pr.user_group_id = g.id),
			       (SELECT count(*) FROM coupons c WHERE g.id = ANY(c.applicable_user_group_ids))
			  FROM user_groups g
			 WHERE g.tenant_id = $1
			 ORDER BY g.created_at`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g userGroupRow
			if err := rows.Scan(&g.ID, &g.Code, &g.Name, &g.Desc,
				&g.Users, &g.Plans, &g.Prices, &g.Coupons); err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"groups": out})
}

type userGroupReq struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Desc string `json:"description"`
}

func (h *handlers) saveUserGroup(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")

	var req userGroupReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Code = strings.TrimSpace(req.Code)
	if req.Name == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"name": "分组名必填"}))
		return
	}
	if req.Code == "" {
		req.Code = groupSlug(req.Name)
	}

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}

	var newID string
	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if id == "" {
			if err := tx.QueryRow(r.Context(), `
				INSERT INTO user_groups (tenant_id, code, name, description, policy)
				VALUES ($1,$2,$3,NULLIF($4,''),'{}'::jsonb)
				RETURNING id::text`,
				tenantID, req.Code, req.Name, req.Desc).Scan(&newID); err != nil {
				return err
			}
		} else {
			newID = id
			// code 不给改：它已经被套餐、价格、优惠券按 ID 引用，
			// 改名是运营需求，改标识只会制造对不上的引用
			tag, err := tx.Exec(r.Context(), `
				UPDATE user_groups SET name = $3, description = NULLIF($4,''), updated_at = now()
				 WHERE tenant_id = $1 AND id = $2::uuid`,
				tenantID, id, req.Name, req.Desc)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return httpx.NotFoundOrForbidden()
			}
		}
		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "user_group.saved", ResourceType: "user_group", ResourceID: &newID,
			APIDomain: "admin", Outcome: "success",
			RequestID:   httpx.RequestIDFrom(r.Context()),
			AfterDigest: map[string]any{"code": req.Code, "name": req.Name},
		})
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeConflict, "这个分组标识已存在"))
			return
		}
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"id": newID})
}

func (h *handlers) deleteUserGroup(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 还有东西挂着的组不能删。
		//
		// 尤其是套餐：删掉组之后 visibility='group' 的套餐会变成
		// 谁都看不见 —— 它还在售，订单接口也还认，但没人能找到它。
		var users, plans, prices, coupons int
		if err := tx.QueryRow(r.Context(), `
			SELECT (SELECT count(*) FROM users  WHERE user_group_id = $1::uuid),
			       (SELECT count(*) FROM plans  WHERE $1::uuid = ANY(visible_group_ids)),
			       (SELECT count(*) FROM prices WHERE user_group_id = $1::uuid),
			       (SELECT count(*) FROM coupons WHERE $1::uuid = ANY(applicable_user_group_ids))`,
			id).Scan(&users, &plans, &prices, &coupons); err != nil {
			return err
		}
		switch {
		case users > 0:
			return httpx.New(httpx.CodeConflict, "这个分组下还有用户，先把他们移出去")
		case plans > 0:
			return httpx.New(httpx.CodeConflict, "还有套餐按这个分组控制可见性，先解除")
		case prices > 0:
			return httpx.New(httpx.CodeConflict, "还有分组专属价格挂在这里，先删掉那些价格")
		case coupons > 0:
			return httpx.New(httpx.CodeConflict, "还有优惠券限定了这个分组，先解除")
		}
		tag, err := tx.Exec(r.Context(),
			`DELETE FROM user_groups WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NotFoundOrForbidden()
		}
		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "user_group.deleted", ResourceType: "user_group", ResourceID: &id,
			APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(r.Context()),
		})
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}

// assignUserGroup 把一个用户放进某个分组（传空则移出）。
func (h *handlers) assignUserGroup(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	userID := chi.URLParam(r, "id")
	var req struct {
		GroupID string `json:"group_id"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var tag interface{ RowsAffected() int64 }
		var err error
		if req.GroupID == "" {
			tag, err = tx.Exec(r.Context(), `
				UPDATE users SET user_group_id = NULL, updated_at = now()
				 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, userID)
		} else {
			tag, err = tx.Exec(r.Context(), `
				UPDATE users SET user_group_id = $3::uuid, updated_at = now()
				 WHERE tenant_id = $1 AND id = $2::uuid
				   AND EXISTS (SELECT 1 FROM user_groups g
				                WHERE g.id = $3::uuid AND g.tenant_id = $1)`,
				tenantID, userID, req.GroupID)
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.New(httpx.CodeValidationFailed, "用户或分组不存在")
		}
		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "user.group_changed", ResourceType: "user", ResourceID: &userID,
			APIDomain: "admin", Outcome: "success",
			RequestID:   httpx.RequestIDFrom(r.Context()),
			AfterDigest: map[string]any{"group_id": req.GroupID},
		})
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}

func groupSlug(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == ' ' || c == '-' || c == '_':
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		buf := make([]byte, 4)
		if _, err := rand.Read(buf); err != nil {
			return "group"
		}
		return "group-" + hex.EncodeToString(buf)
	}
	return s
}
