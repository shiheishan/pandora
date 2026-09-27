// [INPUT]: 依赖 platform/sourcetest 的 Load / Refs 逐包找读环境变量的引用
// [OUTPUT]: 对外提供 TestEnvironmentIsReadOnlyThroughConfig 与豁免表 envAccessExemptions
// [POS]: platform/config 的源码守卫：panel/internal 与 panel/cmd 下非测试 Go 文件只有本包能读进程环境，豁免逐文件登记、失效即红
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package config

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

//------------------------------------------------------------------------------
// 守卫：环境变量只在 platform/config 读
//------------------------------------------------------------------------------

// envReaders 是「读进程环境」的全部入口。取函数值也算，免得绕一层变量逃过扫描。
var envReaders = map[string][]string{
	"os":                    {"Getenv", "LookupEnv", "Environ", "ExpandEnv"},
	"syscall":               {"Getenv", "Environ"},
	"golang.org/x/sys/unix": {"Getenv", "Environ"},
}

// envAccessExemptions 是 config 之外允许读环境的文件，路径相对 panel/，逐个写明理由。
// 只收测试基础设施与「原样转交子进程」的环境，不收业务配置；文件不再读环境或已删除时
// 守卫同样变红，逼着把豁免一起删掉。
var envAccessExemptions = map[string]string{
	"internal/platform/pg18test/pg18test.go":                            "PG18 集成测试的一次性库 DSN（AEGIS_<域>_PG18_*），只被 *_pg18_test.go 引用，不进生产二进制",
	"internal/platform/releasejournal/ca42e2e_fixture_linux.go":         "CA42 e2e 夹具只在隔离容器里以 root 运行，PANDORA_CA42_E2E_ISOLATED 是它的保险栓",
	"internal/platform/clientauth/ca42runner/roots_ca42e2e.go":          "同上，CA42 e2e 构建标签下的夹具保险栓",
	"internal/platform/clientauth/ca42storage/ca42e2e_fixture_linux.go": "同上，CA42 e2e 夹具保险栓",
	"cmd/pandora-client-auth-00044-artifact-verifier/fd_linux.go":       "扫描整份环境（os.Environ），确认签名密钥没有经环境泄露（keyMaterialExposed），不读任何配置项",
	"cmd/pandora-pathtrust/main_linux.go":                               "先经 trustedChildEnv 过滤再交给子进程（os.Environ），不读任何配置项",
}

func TestEnvironmentIsReadOnlyThroughConfig(t *testing.T) {
	panel := filepath.Join("..", "..", "..")
	configDir := filepath.Join(panel, "internal", "platform", "config")

	offenders := map[string][]string{} // 相对 panel/ 的文件 → 引用
	for _, root := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(panel, root), func(dir string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if dir == configDir || !hasNonTestGo(t, dir) {
				return nil
			}
			pkg := sourcetest.Load(t, dir)
			rel, _ := filepath.Rel(panel, dir)
			for importPath, names := range envReaders {
				for _, ref := range pkg.Refs(importPath, names...) {
					file := filepath.ToSlash(filepath.Join(rel, ref.File))
					offenders[file] = append(offenders[file], fmt.Sprintf("%s:%d %s", file, ref.Line, ref.Name))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	var files []string
	for f := range offenders {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		if _, ok := envAccessExemptions[f]; ok {
			continue
		}
		t.Errorf("reads the process environment outside platform/config; add the item to config and inject it:\n  %s",
			strings.Join(offenders[f], "\n  "))
	}
	for f := range envAccessExemptions {
		if len(offenders[f]) == 0 {
			t.Errorf("exemption %s no longer reads the environment (or was removed); delete it from envAccessExemptions", f)
		}
	}
	// 扫描器坏掉会静默扫出 0 处，豁免里的已知读者兜底
	if len(offenders) < len(envAccessExemptions) {
		t.Fatalf("scanner found only %d files; it no longer sees known readers", len(offenders))
	}
}

func hasNonTestGo(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			return true
		}
	}
	return false
}
