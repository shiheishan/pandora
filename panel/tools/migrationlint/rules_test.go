package migrationlint

import (
	"sort"
	"strings"
	"testing"
)

// 一个合规的最小迁移：所有用例在它的基础上改一处，看守卫是否变红。
const goodHeaderless = `-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';
ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';
ALTER TABLE plans DROP COLUMN IF EXISTS note;
`

func rulesOf(t *testing.T, name, text string, known ...int) []string {
	t.Helper()
	m, err := Parse(name, text)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	k := map[int]bool{m.Version: true}
	for _, v := range known {
		k[v] = true
	}
	var rules []string
	for _, v := range Lint(m, k) {
		rules = append(rules, v.Rule)
	}
	sort.Strings(rules)
	return rules
}

func expectRules(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s: 规则 = %v，期望 %v", label, got, want)
	}
}

func TestLintRulesGoGreenAndRed(t *testing.T) {
	expectRules(t, "合规样例", rulesOf(t, "00200_ok.sql", goodHeaderless))

	cases := []struct {
		label string
		text  string
		want  []string
	}{
		{
			label: "没有 Down 段",
			text:  strings.Split(goodHeaderless, "\n-- +goose Down")[0] + "\n",
			want:  []string{RuleDownMissing},
		},
		{
			label: "Down 段只有 SET",
			text:  strings.Replace(goodHeaderless, "ALTER TABLE plans DROP COLUMN IF EXISTS note;\n", "", 1),
			want:  []string{RuleDownEmpty},
		},
		{
			label: "irreversible 缺 forward-fix 且 Down 不 RAISE",
			text: "-- irreversible: 修数据\n" + strings.Replace(goodHeaderless,
				"ALTER TABLE plans DROP COLUMN IF EXISTS note;\n", "SELECT 1;\n", 1),
			want: []string{RuleIrreversibleIncomplete, RuleIrreversibleIncomplete},
		},
		{
			label: "Up 缺两个超时",
			text: strings.Replace(goodHeaderless,
				"-- +goose Up\nSET LOCAL lock_timeout = '5s';\nSET LOCAL statement_timeout = '2min';\n", "-- +goose Up\n", 1),
			want: []string{RuleLockTimeoutUp, RuleStatementTimeoutUp},
		},
		{
			label: "超时设在 DDL 之后不算",
			text: strings.Replace(goodHeaderless,
				"-- +goose Up\nSET LOCAL lock_timeout = '5s';\nSET LOCAL statement_timeout = '2min';\nALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;\n",
				"-- +goose Up\nALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;\nSET LOCAL lock_timeout = '5s';\nSET LOCAL statement_timeout = '2min';\n", 1),
			want: []string{RuleLockTimeoutUp, RuleStatementTimeoutUp},
		},
		{
			label: "Down 缺超时",
			text: strings.Replace(goodHeaderless,
				"-- +goose Down\nSET LOCAL lock_timeout = '5s';\nSET LOCAL statement_timeout = '2min';\n", "-- +goose Down\n", 1),
			want: []string{RuleLockTimeoutDown, RuleStatementTimeoutDown},
		},
		{
			label: "大表普通建索引",
			text: strings.Replace(goodHeaderless, "ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;",
				"CREATE INDEX IF NOT EXISTS users_x_idx ON public.users (created_at);", 1),
			want: []string{RuleIndexNotConcurrent},
		},
		{
			label: "CONCURRENTLY 却没有 NO TRANSACTION",
			text: strings.Replace(goodHeaderless, "ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;",
				"CREATE INDEX CONCURRENTLY IF NOT EXISTS users_x_idx ON users (created_at);", 1),
			want: []string{RuleConcurrentlyInTx},
		},
		{
			label: "大表改列类型",
			text: strings.Replace(goodHeaderless, "ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;",
				"ALTER TABLE orders ALTER COLUMN amount TYPE numeric;", 1),
			want: []string{RuleTableRewrite},
		},
		{
			label: "大表加带易变默认值的列",
			text: strings.Replace(goodHeaderless, "ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;",
				"ALTER TABLE subscriptions ADD COLUMN token uuid NOT NULL DEFAULT gen_random_uuid();", 1),
			want: []string{RuleTableRewrite},
		},
		{
			label: "大表加存储型生成列",
			text: strings.Replace(goodHeaderless, "ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;",
				"ALTER TABLE users ADD COLUMN email_lower text GENERATED ALWAYS AS (lower(email)) STORED;", 1),
			want: []string{RuleTableRewrite},
		},
		{
			label: "删列",
			text: strings.Replace(goodHeaderless, "ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;",
				"ALTER TABLE plans DROP COLUMN legacy_flag;", 1),
			want: []string{RuleExpandContract},
		},
		{
			label: "改列名",
			text: strings.Replace(goodHeaderless, "ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;",
				"ALTER TABLE plans RENAME COLUMN title TO name;", 1),
			want: []string{RuleExpandContract},
		},
		{
			label: "改表名",
			text: strings.Replace(goodHeaderless, "ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;",
				"ALTER TABLE plans RENAME TO catalog_plans;", 1),
			want: []string{RuleExpandContract},
		},
		{
			label: "大表回填没有标记（DO 块里同样算）",
			text: strings.Replace(goodHeaderless, "ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;",
				"-- +goose StatementBegin\nDO $$ BEGIN UPDATE subscriptions SET note = 'x' WHERE note IS NULL; END $$;\n-- +goose StatementEnd", 1),
			want: []string{RuleBackfillUnmarked},
		},
	}
	for _, c := range cases {
		expectRules(t, c.label, rulesOf(t, "00200_case.sql", c.text), c.want...)
	}
}

func TestLintMarkersAndExemptions(t *testing.T) {
	// irreversible 写全：forward-fix + Down RAISE，Down 段不要求超时
	irreversible := `-- irreversible: 修数据，原值没留
-- forward-fix: 后台改回
-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';
UPDATE plans SET note = 'x';

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'rollback refused'; END $$;
-- +goose StatementEnd
`
	expectRules(t, "irreversible 写全", rulesOf(t, "00201_fix.sql", irreversible))

	concurrent := `-- +goose NO TRANSACTION
-- +goose Up
SET lock_timeout = '5s';
SET statement_timeout = '30min';
CREATE INDEX CONCURRENTLY IF NOT EXISTS users_x_idx ON users (created_at);

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '5min';
DROP INDEX CONCURRENTLY IF EXISTS users_x_idx;
`
	expectRules(t, "NO TRANSACTION + CONCURRENTLY", rulesOf(t, "00202_idx.sql", concurrent))
	expectRules(t, "NO TRANSACTION 里 SET LOCAL 不算",
		rulesOf(t, "00202_idx.sql", strings.ReplaceAll(concurrent, "SET lock_timeout", "SET LOCAL lock_timeout")),
		RuleLockTimeoutUp, RuleLockTimeoutDown)

	rewrite := "-- rewrite: users rows=10001 est=0.6s（5k 副本实测 546ms）\n" + strings.Replace(goodHeaderless,
		"ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;",
		"ALTER TABLE users ADD COLUMN email_lower text GENERATED ALWAYS AS (lower(email)) STORED;", 1)
	expectRules(t, "rewrite 标记写了行数与耗时", rulesOf(t, "00203_rw.sql", rewrite))
	expectRules(t, "rewrite 标记缺耗时不算",
		rulesOf(t, "00203_rw.sql", strings.Replace(rewrite, " est=0.6s", "", 1)), RuleTableRewrite)

	contract := "-- contract-of: 00150\n" + strings.Replace(goodHeaderless,
		"ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;", "ALTER TABLE plans DROP COLUMN legacy_flag;", 1)
	expectRules(t, "contract-of 引用已有的更早迁移", rulesOf(t, "00204_c.sql", contract, 150))
	expectRules(t, "contract-of 引用不存在的迁移", rulesOf(t, "00204_c.sql", contract), RuleExpandContract)

	backfill := "-- backfill: batched-by=租户×UTC 日; rerunnable=ON CONFLICT DO UPDATE 写重算值\n" + strings.Replace(goodHeaderless,
		"ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;", "UPDATE subscriptions SET note = 'x' WHERE note IS NULL;", 1)
	expectRules(t, "backfill 标记写全", rulesOf(t, "00205_b.sql", backfill))

	// 同一迁移里刚建的大表名（这里借用 users 演示）是空表，建索引与回填不受约束
	fresh := strings.Replace(goodHeaderless, "ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;",
		"CREATE TABLE IF NOT EXISTS users (id uuid);\nCREATE INDEX users_id_idx ON users (id);\nUPDATE users SET id = id;", 1)
	expectRules(t, "同迁移新建的表", rulesOf(t, "00206_new.sql", fresh))

	// 注释、字符串、函数体里的字不是语句
	noise := strings.Replace(goodHeaderless, "ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;",
		`-- ALTER TABLE users DROP COLUMN email;
/* CREATE INDEX x ON users (id); */
COMMENT ON TABLE plans IS 'ALTER TABLE users RENAME TO x; UPDATE users SET a=1';
CREATE OR REPLACE FUNCTION app.f() RETURNS void LANGUAGE sql AS $fn$ UPDATE users SET a = 1 $fn$;`, 1)
	expectRules(t, "注释、字符串、函数体", rulesOf(t, "00207_noise.sql", noise))

	// DROP CONSTRAINT / DROP DEFAULT / RENAME CONSTRAINT 不是删列、改名
	constraints := strings.Replace(goodHeaderless, "ALTER TABLE plans ADD COLUMN IF NOT EXISTS note text;",
		"ALTER TABLE plans DROP CONSTRAINT IF EXISTS c1, ALTER COLUMN note DROP DEFAULT, ALTER COLUMN note DROP NOT NULL;\nALTER TABLE plans RENAME CONSTRAINT c2 TO c3;", 1)
	expectRules(t, "约束与默认值", rulesOf(t, "00208_c.sql", constraints))
}

func TestParseRejectsMalformedFiles(t *testing.T) {
	for label, text := range map[string]string{
		"没有 Up":       "SELECT 1;\n",
		"两个 Up":       "-- +goose Up\nSELECT 1;\n-- +goose Up\n",
		"Down 在 Up 前": "-- +goose Down\nSELECT 1;\n-- +goose Up\nSELECT 1;\n",
	} {
		if _, err := Parse("00300_x.sql", text); err == nil {
			t.Errorf("%s：应当拒绝", label)
		}
	}
	if _, err := Parse("bad.sql", goodHeaderless); err == nil {
		t.Error("非 00000_name.sql 文件名应当拒绝")
	}
}

func TestUpSegmentIgnoresAppendedDown(t *testing.T) {
	before, err := Parse("00400_x.sql", "-- +goose Up\nSELECT 1;\n")
	if err != nil {
		t.Fatal(err)
	}
	after, err := Parse("00400_x.sql", "-- irreversible: x\n-- forward-fix: y\n-- +goose Up\nSELECT 1;\n\n-- +goose Down\nSELECT 2;\n")
	if err != nil {
		t.Fatal(err)
	}
	if before.UpSegment() != after.UpSegment() {
		t.Errorf("追加 Down 段与文件头后 Up 段应不变：%q vs %q", before.UpSegment(), after.UpSegment())
	}
	changed, _ := Parse("00400_x.sql", "-- +goose Up\nSELECT 3;\n")
	if changed.UpSegment() == before.UpSegment() {
		t.Error("Up 段内容变化必须能被看出来")
	}
}
