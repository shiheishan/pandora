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
