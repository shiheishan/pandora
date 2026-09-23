//go:build linux && (amd64 || arm64)

package releasejournal

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func v3RootCapabilityFixture(t *testing.T) (*v3RootCapability, string, string) {
	t.Helper()
	policy, _ := linuxJournalFixture(t)
	parent := filepath.Join(policy.root, "canonical-parent")
	rootName := "release-journal-v3"
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(parent, rootName), 0700); err != nil {
		t.Fatal(err)
	}
	capability, err := openV3RootCapability(parent, rootName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capability.close() })
	return capability, parent, rootName
}

func TestV3RootCapabilityBootstrapSessionAndClose(t *testing.T) {
	capability, _, _ := v3RootCapabilityFixture(t)
	intent := preparedBootstrapIntent()
	created, recovered, err := capability.bootstrapPrepared(intent)
	if err != nil || recovered || created.State() != V3Prepared {
		t.Fatalf("bootstrap: recovered=%v state=%s err=%v", recovered, created.State(), err)
	}
	session, err := capability.openSession(intent.attemptID)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := session.Inspect()
	if err != nil || opened.HeadSHA256() != created.HeadSHA256() {
		t.Fatalf("session mismatch: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := capability.validate(); err != nil {
		t.Fatalf("session close damaged capability: %v", err)
	}
	if err := capability.close(); err != nil {
		t.Fatal(err)
	}
	if err := capability.close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	if err := capability.validate(); err == nil {
		t.Fatal("closed capability validated")
	}
}

func TestV3RootCapabilityRejectsRootBasenameReplacement(t *testing.T) {
	capability, parent, rootName := v3RootCapabilityFixture(t)
	original := filepath.Join(parent, rootName)
	moved := filepath.Join(parent, "retained-root")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	if err := capability.validate(); err == nil || !strings.Contains(err.Error(), "root_binding_changed") {
		t.Fatalf("root replacement accepted: %v", err)
	}
	entries, err := os.ReadDir(original)
	if err != nil || len(entries) != 0 {
		t.Fatalf("replacement root was mutated: entries=%d err=%v", len(entries), err)
	}
}

func TestV3RootCapabilityRejectsCanonicalParentReplacement(t *testing.T) {
	capability, parent, rootName := v3RootCapabilityFixture(t)
	moved := parent + ".retained"
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(parent, rootName), 0700); err != nil {
		t.Fatal(err)
	}
	if err := capability.validate(); err == nil || !strings.Contains(err.Error(), "parent_binding_changed") {
		t.Fatalf("parent replacement accepted: %v", err)
	}
}

func TestV3RootCapabilityRejectsSymlinkAndUnsafeModes(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	for _, tc := range []struct {
		name       string
		parentMode os.FileMode
		rootMode   os.FileMode
		symlink    bool
	}{
		{name: "parent-mode", parentMode: 0755, rootMode: 0700},
		{name: "root-mode", parentMode: 0700, rootMode: 0755},
		{name: "root-symlink", parentMode: 0700, rootMode: 0700, symlink: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := filepath.Join(policy.root, tc.name)
			if err := os.Mkdir(parent, tc.parentMode); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(parent, "release-journal-v3")
			if tc.symlink {
				target := filepath.Join(policy.root, tc.name+"-target")
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, root); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(root, tc.rootMode); err != nil {
				t.Fatal(err)
			}
			if capability, err := openV3RootCapability(parent, "release-journal-v3"); err == nil {
				_ = capability.close()
				t.Fatal("unsafe production root accepted")
			}
		})
	}
}

func TestV3RootCapabilityRejectsReplacementDuringOpen(t *testing.T) {
	for _, stage := range []string{"after-parent", "after-root", "before-return"} {
		t.Run(stage, func(t *testing.T) {
			policy, _ := linuxJournalFixture(t)
			parent := filepath.Join(policy.root, "canonical-parent")
			rootName := "release-journal-v3"
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(parent, rootName), 0700); err != nil {
				t.Fatal(err)
			}
			replaceParent := func() error {
				if err := os.Rename(parent, parent+".retained"); err != nil {
					return err
				}
				if err := os.Mkdir(parent, 0700); err != nil {
					return err
				}
				return os.Mkdir(filepath.Join(parent, rootName), 0700)
			}
			replaceRoot := func() error {
				root := filepath.Join(parent, rootName)
				if err := os.Rename(root, filepath.Join(parent, "retained-root")); err != nil {
					return err
				}
				return os.Mkdir(root, 0700)
			}
			hooks := v3RootCapabilityHooks{}
			switch stage {
			case "after-parent":
				hooks.afterParentOpen = replaceParent
			case "after-root":
				hooks.afterRootOpen = replaceRoot
			case "before-return":
				hooks.beforeReturn = replaceRoot
			}
			capability, err := openV3RootCapabilityWithHooks(parent, rootName, hooks)
			if capability != nil {
				_ = capability.close()
				t.Fatal("replacement race returned a capability")
			}
			if err == nil || !strings.Contains(err.Error(), "binding_changed") {
				t.Fatalf("replacement race accepted: %v", err)
			}
		})
	}
}

func TestV3ProductionOwnedSessionRevalidatesAndClosesCapability(t *testing.T) {
	capability, parent, rootName := v3RootCapabilityFixture(t)
	intent := preparedBootstrapIntent()
	if _, _, err := capability.bootstrapPrepared(intent); err != nil {
		t.Fatal(err)
	}
	session, err := capability.openOwnedSession(intent.attemptID)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, rootName)
	if err := os.Rename(root, filepath.Join(parent, "retained-root")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Inspect(); err == nil || !strings.Contains(err.Error(), "production_root_binding_changed") {
		t.Fatalf("owned session ignored root replacement: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := capability.validate(); err == nil {
		t.Fatal("session close did not close production root capability")
	}
}

func TestV3RootCapabilityDescriptorsAreCLOEXECAndFailuresDoNotLeak(t *testing.T) {
	capability, parent, rootName := v3RootCapabilityFixture(t)
	for name, fd := range map[string]int{"parent": capability.parentFD, "root": capability.rootFD,
		"mount-namespace": capability.mountNSFD, "user-namespace": capability.userNSFD} {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC == 0 {
			t.Fatalf("%s descriptor is not CLOEXEC: flags=%d err=%v", name, flags, err)
		}
	}
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 50; index++ {
		_, err := openV3RootCapabilityWithHooks(parent, rootName, v3RootCapabilityHooks{
			beforeReturn: func() error { return syscall.EIO },
		})
		if err == nil {
			t.Fatal("injected final failure accepted")
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("capability failure leaked descriptors: before=%d after=%d", len(before), len(after))
	}
}

func TestV3RootCapabilityRejectsParentSymlinkAndUnlinkedRetainedDirectories(t *testing.T) {
	policy, _ := linuxJournalFixture(t)
	target := filepath.Join(policy.root, "parent-target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(target, "release-journal-v3"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(policy.root, "parent-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if capability, err := openV3RootCapability(link, "release-journal-v3"); err == nil {
		_ = capability.close()
		t.Fatal("parent symlink accepted")
	}

	parent := filepath.Join(policy.root, "unlink-parent")
	root := filepath.Join(parent, "release-journal-v3")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	capability, err := openV3RootCapabilityWithHooks(parent, "release-journal-v3", v3RootCapabilityHooks{
		afterRootOpen: func() error {
			moved := filepath.Join(policy.root, "unlinked-root")
			if err := os.Rename(root, moved); err != nil {
				return err
			}
			if err := os.Remove(moved); err != nil {
				return err
			}
			return os.Remove(parent)
		},
	})
	if capability != nil {
		_ = capability.close()
		t.Fatal("unlinked retained directories returned a capability")
	}
	if err == nil {
		t.Fatal("unlinked retained directories accepted")
	}
}

func TestV3ProductionExportedOpenerOnDedicatedHostPath(t *testing.T) {
	if os.Getenv("PANDORA_RELEASEJOURNAL_TEST_PRODUCTION") != "1" {
		t.Skip("dedicated production-path host gate required")
	}
	capability, err := openV3RootCapability(productionV3ParentPath, productionV3RootName)
	if err != nil {
		t.Fatal(err)
	}
	intent := preparedBootstrapIntent()
	if _, _, err := capability.bootstrapPrepared(intent); err != nil {
		_ = capability.close()
		t.Fatal(err)
	}
	if err := capability.close(); err != nil {
		t.Fatal(err)
	}
	session, err := OpenProductionV3Session(intent.attemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	snapshot, err := session.Inspect()
	if err != nil || snapshot.State() != V3Prepared || snapshot.AttemptID() != intent.attemptID {
		t.Fatalf("exported production opener mismatch: state=%s err=%v", snapshot.State(), err)
	}
}

func TestV3RootCapabilityRejectsNonSupervisorNamespace(t *testing.T) {
	if os.Getenv("PANDORA_RELEASEJOURNAL_TEST_NAMESPACE_DENIAL") != "1" {
		t.Skip("isolated non-supervisor namespace gate required")
	}
	policy, _ := linuxJournalFixture(t)
	parent := filepath.Join(policy.root, "namespace-parent")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(parent, "release-journal-v3"), 0700); err != nil {
		t.Fatal(err)
	}
	if capability, err := openV3RootCapability(parent, "release-journal-v3"); err == nil {
		_ = capability.close()
		t.Fatal("non-supervisor namespace accepted")
	} else if !strings.Contains(err.Error(), "supervisor_mount_namespace_mismatch") {
		t.Fatalf("unexpected namespace denial: %v", err)
	}
}

func TestV3SessionShallowCopySharesCloseLease(t *testing.T) {
	capability, _, _ := v3RootCapabilityFixture(t)
	intent := preparedBootstrapIntent()
	if _, _, err := capability.bootstrapPrepared(intent); err != nil {
		t.Fatal(err)
	}
	session, err := capability.openOwnedSession(intent.attemptID)
	if err != nil {
		t.Fatal(err)
	}
	clone := *session
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	reused, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer reused.Close()
	if err := clone.Close(); err != nil {
		t.Fatalf("clone close was not idempotent: %v", err)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(reused.Fd()), &stat); err != nil {
		t.Fatalf("clone close closed a reused descriptor: %v", err)
	}
	if _, err := clone.Inspect(); err == nil {
		t.Fatal("closed shallow copy remained usable")
	}
}
