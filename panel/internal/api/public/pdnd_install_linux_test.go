//go:build linux

package public

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPDNDInstallerRestoresPreviousFilesWhenServiceFails(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "bin")
	configDir := filepath.Join(root, "etc")
	unitDir := filepath.Join(root, "units")
	fakeBin := filepath.Join(root, "fake-bin")
	for _, dir := range []string{installDir, configDir, unitDir, fakeBin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := map[string]string{
		filepath.Join(installDir, "pandora-native"):      "old-binary\n",
		filepath.Join(configDir, "config.json"):          "old-config\n",
		filepath.Join(configDir, "identity.json"):        "old-identity\n",
		filepath.Join(unitDir, "pandora-native.service"): "old-unit\n",
	}
	for path, body := range old {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	artifact := []byte("new-binary\n")
	digest := fmt.Sprintf("%x", sha256.Sum256(artifact))
	writeExecutable(t, filepath.Join(fakeBin, "curl"), `#!/bin/sh
case "$*" in
  *'/pdnd/bin/'*) for arg do out="$arg"; done; printf 'new-binary\n' > "$out" ;;
  *'/pdnd/sha256/'*) printf '%s  artifact\n' '`+digest+`' ;;
  *) exit 1 ;;
esac
`)
	writeExecutable(t, filepath.Join(fakeBin, "systemctl"), `#!/bin/sh
case "$1" in
  is-active) exit 1 ;;
  *) exit 0 ;;
esac
`)
	writeExecutable(t, filepath.Join(fakeBin, "sleep"), "#!/bin/sh\nexit 0\n")

	script := filepath.Join(root, "install.sh")
	if err := os.WriteFile(script, []byte(strings.ReplaceAll(pdndInstallTemplate, "@@PANEL@@", "https://panel.invalid")), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", script, "--node-id", "node-1", "--node-type", "vless", "--token", "runtime-token")
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"PANDORA_INSTALL_DIR="+installDir,
		"PANDORA_CONFIG_DIR="+configDir,
		"PANDORA_UNIT_DIR="+unitDir,
	)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("installer unexpectedly succeeded: %s", out)
	}
	for path, want := range old {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read restored %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s was not restored: got %q want %q", path, got, want)
		}
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
