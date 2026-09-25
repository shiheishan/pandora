// [INPUT]: 读取 migrations 里最后一个定义 app.seed_tenant_defaults 的迁移，扫描 internal 下非测试 Go 源码里读降级开关的三种写法（FeatureSwitch、switchEnabled、feature_switches 的 SQL 字面量）
// [OUTPUT]: 对外提供 TestTenantSeedSwitchesMatchCode
// [POS]: middleware 的单元测试：建租户触发器种下的降级开关与代码真正读取的开关一一对上——代码读了却没种（新租户缺行）、种了却没人读（R102 那种未接入开关）、essential 名单变了，都会报出
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package middleware

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// essentialSwitches 永远不可关闭（CHECK 守着），所以代码不必读它们；
// 名单变化必须是有意为之，改这里的同时改建租户触发器与存量迁移。
var essentialSwitches = []string{"auth.login", "client.config_sync", "subscription.renewal"}

var (
	seedSwitchBlock = regexp.MustCompile(`(?s)INSERT INTO public\.feature_switches.*?AS c\(code, essential\)`)
	seedSwitchRow   = regexp.MustCompile(`\('([a-z_.]+)',\s*(true|false)\)`)
	goSwitchReads   = []*regexp.Regexp{
		regexp.MustCompile(`FeatureSwitch\([^,]+,\s*"([a-z_.]+)"`),
		regexp.MustCompile(`switchEnabled\(.*?"([a-z_.]+)"\)`),
		regexp.MustCompile("(?s)feature_switches[^`]*?code\\s*=\\s*'([a-z_.]+)'"),
	}
)

func TestTenantSeedSwitchesMatchCode(t *testing.T) {
	seeded := map[string]bool{} // code → essential
	block := seedSwitchBlock.FindString(latestTenantSeedMigration(t))
	for _, m := range seedSwitchRow.FindAllStringSubmatch(block, -1) {
		seeded[m[1]] = m[2] == "true"
	}
	if len(seeded) == 0 {
		t.Fatal("no feature switch rows found in the tenant seed trigger")
	}

	read := codeReadSwitches(t)
	for code := range read {
		if _, ok := seeded[code]; !ok {
			t.Errorf("code reads switch %q but the tenant seed does not create it", code)
		}
	}
	var essential []string
	for code, isEssential := range seeded {
		if isEssential {
			essential = append(essential, code)
			continue
		}
		if !read[code] {
			t.Errorf("tenant seed creates switch %q that no code reads (unwired, see R102)", code)
		}
	}
	sort.Strings(essential)
	if strings.Join(essential, ",") != strings.Join(essentialSwitches, ",") {
		t.Errorf("essential switches=%v, want %v", essential, essentialSwitches)
	}
}

// codeReadSwitches 收集非测试源码里读到的开关 code
func codeReadSwitches(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, re := range goSwitchReads {
			for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
				out[m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("found no switch reads in the source tree; the patterns are stale")
	}
	return out
}

// latestTenantSeedMigration 按文件名排序取最后一个定义 seed 函数的迁移：
// 以后若有迁移 CREATE OR REPLACE 它，核对的就是生效的那一版。
func latestTenantSeedMigration(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	latest := ""
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "FUNCTION app.seed_tenant_defaults(") {
			latest = string(raw)
		}
	}
	if latest == "" {
		t.Fatal("no migration defines app.seed_tenant_defaults")
	}
	return latest
}
