package migrationlint

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 规则编号。ratchet.txt 按「文件名 规则编号」登记历史违例，编号一经使用不要改名。
const (
	RuleDownMissing            = "down-missing"            // 没有 -- +goose Down 段
	RuleDownEmpty              = "down-empty"              // Down 段没有任何实际语句，又没标 irreversible
	RuleIrreversibleIncomplete = "irreversible-incomplete" // 标了 irreversible，却缺 forward-fix 或 Down 不 RAISE
	RuleLockTimeoutUp          = "lock-timeout-up"
	RuleLockTimeoutDown        = "lock-timeout-down"
	RuleStatementTimeoutUp     = "statement-timeout-up"
	RuleStatementTimeoutDown   = "statement-timeout-down"
	RuleIndexNotConcurrent     = "index-not-concurrent" // 大表上普通 CREATE INDEX
	RuleConcurrentlyInTx       = "concurrently-in-transaction"
	RuleTableRewrite           = "table-rewrite"   // 大表整表重写且文件头没写行数与耗时
	RuleExpandContract         = "expand-contract" // 删列、改名没有引用扩展阶段的迁移
	RuleBackfillUnmarked       = "backfill-unmarked"
)

// Violation 是一条违例。
type Violation struct {
	File   string
	Rule   string
	Detail string
}

// Key 是 ratchet.txt 里的登记形式。
func (v Violation) Key() string { return v.File + " " + v.Rule }

func (v Violation) String() string { return v.Key() + ": " + v.Detail }

var (
	createdTableRe = regexp.MustCompile(`(?i)\bCREATE\s+(?:UNLOGGED\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?(\w+)`)
	createIndexRe  = regexp.MustCompile(`(?i)\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+(CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?(?:[\w.]+\s+)?ON\s+(?:ONLY\s+)?(?:public\.)?(\w+)`)
	alterTableRe   = regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?(?:public\.)?(\w+)\s+(.*)$`)
	alterTypeRe    = regexp.MustCompile(`(?i)\bALTER\s+(?:COLUMN\s+)?\w+\s+(?:SET\s+DATA\s+)?TYPE\b`)
	storedGenRe    = regexp.MustCompile(`(?i)\bADD\b.*\bGENERATED\s+ALWAYS\s+AS\s*\(.*\bSTORED\b`)
	identityAddRe  = regexp.MustCompile(`(?i)\bADD\b.*(\b(?:BIG|SMALL)?SERIAL\b|\bGENERATED\s+(?:ALWAYS|BY\s+DEFAULT)\s+AS\s+IDENTITY\b)`)
	volatileDefRe  = regexp.MustCompile(`(?i)\bADD\b.*\bDEFAULT\b[^,]*\b(clock_timestamp|random|gen_random_uuid|gen_random_bytes|uuidv7|uuidv4|uuid_generate_v\d\w*|nextval|timeofday)\s*\(`)
	setStorageRe   = regexp.MustCompile(`(?i)\bSET\s+(LOGGED|UNLOGGED|TABLESPACE|ACCESS\s+METHOD)\b`)
	fullRewriteRe  = regexp.MustCompile(`(?i)\b(?:VACUUM\s+(?:\(\s*)?FULL\b[^;]*?|CLUSTER\s+)(?:public\.)?(\w+)\s*$`)
	dropColumnRe   = regexp.MustCompile(`(?i)\bDROP\s+(?:COLUMN\s+)?(?:IF\s+EXISTS\s+)?(\w+)`)
	renameRe       = regexp.MustCompile(`(?i)\bRENAME\s+(?:COLUMN\s+)?(\w+)(?:\s+TO\b)?`)
	updateRe       = regexp.MustCompile(`(?i)\bUPDATE\s+(?:ONLY\s+)?(?:public\.)?(\w+)\s+(?:(?:AS\s+)?\w+\s+)?SET\b`)
	deleteRe       = regexp.MustCompile(`(?i)\bDELETE\s+FROM\s+(?:ONLY\s+)?(?:public\.)?(\w+)`)
	insertSelectRe = regexp.MustCompile(`(?i)\bINSERT\s+INTO\s+(?:public\.)?(\w+)\b.*\bSELECT\b`)
	raiseRe        = regexp.MustCompile(`(?i)\bRAISE\s+EXCEPTION\b`)
	setStmtRe      = regexp.MustCompile(`(?i)^SET\s`)
	contractOfRe   = regexp.MustCompile(`^([0-9]{5})\b`)
)

// 删列正则命中这些词时说明是 DROP CONSTRAINT / DROP DEFAULT 之类，不是删列。
var notColumnAfterDrop = map[string]bool{
	"constraint": true, "default": true, "not": true, "identity": true, "expression": true,
}

// 改名正则命中这些词时不是改列名或表名（RENAME CONSTRAINT、RENAME TO 新表名另算）。
var notColumnAfterRename = map[string]bool{"constraint": true}

// Lint 检查一个迁移；known 是同目录全部版本号，用于核对 contract-of 引用。
func Lint(m Migration, known map[int]bool) []Violation {
	var out []Violation
	add := func(rule, format string, args ...any) {
		out = append(out, Violation{File: m.Name, Rule: rule, Detail: fmt.Sprintf(format, args...)})
	}

	upStmts := statements(m.Up)
	downStmts := statements(m.Down)

	// 1. Down 或 irreversible
	switch {
	case !m.HasDown:
		add(RuleDownMissing, "没有 `-- +goose Down` 段；goose 对没有 Down 的迁移执行 down 只删版本行、什么也不做")
	case m.Irreversible():
		fix := strings.Join(m.HeaderValue("forward-fix"), " ")
		if strings.TrimSpace(fix) == "" {
			add(RuleIrreversibleIncomplete, "标了 irreversible，文件头还要写 `-- forward-fix: <前滚补救办法>`")
		}
		if !raiseRe.MatchString(executableSQL(m.Down)) {
			add(RuleIrreversibleIncomplete, "标了 irreversible，Down 必须 RAISE EXCEPTION 拒绝，不能静默成功")
		}
	default:
		effective := 0
		for _, s := range downStmts {
			if !setStmtRe.MatchString(s) {
				effective++
			}
		}
		if effective == 0 {
			add(RuleDownEmpty, "Down 段没有实际语句；确实不能逆就在文件头写 `-- irreversible:` 并在 Down 里 RAISE")
		}
	}

	// 2. 锁等待与语句超时：段首、任何 DDL/DML 之前
	checkTimeouts(m, upStmts, "up", add)
	if m.HasDown && !m.Irreversible() {
		checkTimeouts(m, downStmts, "down", add)
	}

	// 3–6 只看 Up：线上升级跑的是 Up
	created := map[string]bool{}
	for _, s := range upStmts {
		for _, g := range createdTableRe.FindAllStringSubmatch(s, -1) {
			created[strings.ToLower(g[1])] = true
		}
	}
	isBig := func(table string) bool {
		t := strings.ToLower(table)
		return BigTables[t] && !created[t]
	}
	rewriteOK := func(table string) bool {
		for _, v := range m.HeaderValue("rewrite") {
			if containsWord(v, table) && strings.Contains(v, "rows=") && strings.Contains(v, "est=") {
				return true
			}
		}
		return false
	}
	contractOK := contractReferenceOK(m, known)
	backfillOK := false
	for _, v := range m.HeaderValue("backfill") {
		if strings.Contains(v, "batched-by=") && strings.Contains(v, "rerunnable=") {
			backfillOK = true
		}
	}

	for _, s := range upStmts {
		if g := createIndexRe.FindStringSubmatch(s); g != nil {
			concurrently := strings.TrimSpace(g[1]) != ""
			switch {
			case concurrently && !m.NoTransaction:
				add(RuleConcurrentlyInTx, "%s 上 CREATE INDEX CONCURRENTLY 要求文件带 `-- +goose NO TRANSACTION`", g[2])
			case !concurrently && isBig(g[2]):
				add(RuleIndexNotConcurrent, "大表 %s 上建索引要用 CREATE INDEX CONCURRENTLY（配 NO TRANSACTION）", g[2])
			}
		}
		if g := alterTableRe.FindStringSubmatch(s); g != nil {
			table, rest := g[1], g[2]
			if isBig(table) && !rewriteOK(table) {
				for _, r := range []struct {
					re     *regexp.Regexp
					reason string
				}{
					{alterTypeRe, "改列类型"},
					{storedGenRe, "加存储型生成列"},
					{identityAddRe, "加自增 / 标识列"},
					{volatileDefRe, "加带易变默认值的列"},
					{setStorageRe, "改存储属性"},
				} {
					if r.re.MatchString(rest) {
						add(RuleTableRewrite, "大表 %s %s会整表重写；文件头要写 `-- rewrite: %s rows=<行数> est=<预估耗时>`", table, r.reason, table)
						break
					}
				}
			}
			if !created[strings.ToLower(table)] && !contractOK {
				for _, d := range dropColumnRe.FindAllStringSubmatch(rest, -1) {
					if !notColumnAfterDrop[strings.ToLower(d[1])] {
						add(RuleExpandContract, "%s 删列 %s：收缩要在扩展发布之后单独一版，文件头写 `-- contract-of: <扩展迁移编号>`", table, d[1])
					}
				}
				for _, r := range renameRe.FindAllStringSubmatch(rest, -1) {
					if !notColumnAfterRename[strings.ToLower(r[1])] {
						add(RuleExpandContract, "%s 改名：走「加新列/新表 → 迁移数据 → 下一版收缩」，文件头写 `-- contract-of: <扩展迁移编号>`", table)
					}
				}
			}
		}
		if g := fullRewriteRe.FindStringSubmatch(s); g != nil && isBig(g[1]) && !rewriteOK(g[1]) {
			add(RuleTableRewrite, "大表 %s 上 VACUUM FULL / CLUSTER 会整表重写", g[1])
		}
		if !backfillOK {
			for _, re := range []*regexp.Regexp{updateRe, deleteRe, insertSelectRe} {
				if g := re.FindStringSubmatch(s); g != nil && isBig(g[1]) {
					add(RuleBackfillUnmarked, "大表 %s 上的回填 / 批量改写要分批、可重跑，文件头写 `-- backfill: batched-by=<分批方式>; rerunnable=<为什么重跑安全>`", g[1])
				}
			}
		}
	}
	return dedupe(out)
}

func checkTimeouts(m Migration, stmts []string, section string, add func(rule, format string, args ...any)) {
	lockRule, stmtRule := RuleLockTimeoutUp, RuleStatementTimeoutUp
	if section == "down" {
		lockRule, stmtRule = RuleLockTimeoutDown, RuleStatementTimeoutDown
	}
	// 事务内用 SET LOCAL（随事务结束失效，不漏给连接池里的下一个迁移）；
	// NO TRANSACTION 的文件里 SET LOCAL 不生效，只能用会话级 SET。
	prefix := `(?i)^SET\s+LOCAL\s+`
	how := "SET LOCAL"
	if m.NoTransaction {
		prefix = `(?i)^SET\s+(?:SESSION\s+)?`
		how = "SET（NO TRANSACTION 文件里 SET LOCAL 不生效）"
	}
	lockRe := regexp.MustCompile(prefix + `lock_timeout\b`)
	stmtRe := regexp.MustCompile(prefix + `statement_timeout\b`)
	lock, stmt := false, false
	for _, s := range stmts {
		if !setStmtRe.MatchString(s) {
			break // 第一条非 SET 语句之前必须设好
		}
		lock = lock || lockRe.MatchString(s)
		stmt = stmt || stmtRe.MatchString(s)
	}
	if !lock {
		add(lockRule, "%s 段开头（任何 DDL/DML 之前）要 %s lock_timeout", section, how)
	}
	if !stmt {
		add(stmtRule, "%s 段开头（任何 DDL/DML 之前）要 %s statement_timeout", section, how)
	}
}

func contractReferenceOK(m Migration, known map[int]bool) bool {
	for _, v := range m.HeaderValue("contract-of") {
		g := contractOfRe.FindStringSubmatch(v)
		if g == nil {
			continue
		}
		ref, _ := strconv.Atoi(g[1])
		if ref < m.Version && known[ref] {
			return true
		}
	}
	return false
}

func containsWord(text, word string) bool {
	re := regexp.MustCompile(`(?i)(^|[^\w])` + regexp.QuoteMeta(word) + `([^\w]|$)`)
	return re.MatchString(text)
}

func dedupe(in []Violation) []Violation {
	seen := map[string]bool{}
	var out []Violation
	for _, v := range in {
		if seen[v.String()] {
			continue
		}
		seen[v.String()] = true
		out = append(out, v)
	}
	return out
}

// LintAll 检查整个目录，按文件名、规则排序返回。
func LintAll(ms []Migration) []Violation {
	known := map[int]bool{}
	for _, m := range ms {
		known[m.Version] = true
	}
	var out []Violation
	for _, m := range ms {
		out = append(out, Lint(m, known)...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}
