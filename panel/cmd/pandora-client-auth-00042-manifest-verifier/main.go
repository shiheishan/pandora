// [INPUT]: 依赖 platform/clientauth/ca42manifest 的 Verify 与大小上限
// [OUTPUT]: 对外提供 pandora-client-auth-00042-manifest-verifier 命令：stdin 读 manifest，校验通过向 stdout 写 JSON 回执，否则以 DENY 行与 sysexits 码退出
// [POS]: panel/cmd 的 CLIENT-AUTH 00042 manifest 校验器，只读、fail closed；校验规则在 ca42manifest，这里只做 I/O 与退出码
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
)

const exitDenied = 78

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "client_auth_00042_manifest_verifier=DENY reason=invalid_arguments")
		return 64
	}
	data, err := io.ReadAll(io.LimitReader(stdin, ca42manifest.MaxManifestBytes+1))
	if err != nil {
		fmt.Fprintln(stderr, "client_auth_00042_manifest_verifier=DENY reason=input_read_failed")
		return 74
	}
	receipt, err := ca42manifest.Verify(data)
	if err != nil {
		fmt.Fprintln(stderr, "client_auth_00042_manifest_verifier=DENY reason=verification_failed")
		return exitDenied
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		fmt.Fprintln(stderr, "client_auth_00042_manifest_verifier=DENY reason=internal_failure")
		return 70
	}
	encoded = append(encoded, '\n')
	if n, err := stdout.Write(encoded); err != nil || n != len(encoded) {
		fmt.Fprintln(stderr, "client_auth_00042_manifest_verifier=DENY reason=output_write_failed")
		return 74
	}
	return 0
}
