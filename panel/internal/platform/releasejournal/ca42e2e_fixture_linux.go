//go:build ca42e2e && linux && (amd64 || arm64)

package releasejournal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// CA42E2EJournalInput contains only the identities needed to build the
// pre-admission LAYOUT_SWITCHED journal boundary. It is compiled exclusively
// into the disposable ca42e2e test binary.
type CA42E2EJournalInput struct {
	JournalID                 string
	AttemptID                 string
	ReleaseContractCoreSHA256 string
	ControllerSHA256          string
	CreatedAtEpoch            int64
}

// ProvisionCA42E2ELayoutSwitched writes the exact fixed production Journal v3
// root inside the already-established disposable namespace. It deliberately
// accepts no path and refuses to run outside the isolated E2E harness.
func ProvisionCA42E2ELayoutSwitched(input CA42E2EJournalInput) (_ V3Snapshot, resultErr error) {
	if err := requireCA42E2EJournalIsolation(); err != nil {
		return V3Snapshot{}, err
	}
	root := filepath.Join(productionV3ParentPath, productionV3RootName)
	if stat, err := os.Lstat(root); err == nil {
		var identity unix.Stat_t
		entries, readErr := os.ReadDir(root)
		if !stat.IsDir() || stat.Mode().Perm() != 0o700 || unix.Stat(root, &identity) != nil || identity.Uid != 0 || identity.Gid != 0 ||
			readErr != nil || len(entries) != 0 {
			return V3Snapshot{}, errors.New("release journal CA42 E2E precreated root invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return V3Snapshot{}, err
	} else if err := os.Mkdir(root, 0o700); err != nil {
		return V3Snapshot{}, err
	}
	records, snapshot, err := ca42E2ELayoutSwitchedRecords(input)
	if err != nil {
		return V3Snapshot{}, err
	}
	completed := false
	defer func() {
		if !completed {
			resultErr = errors.Join(resultErr, os.RemoveAll(root))
		}
	}()
	directory := filepath.Join(root, snapshot.AttemptID()+"."+snapshot.JournalID()+".release-journal-v3")
	if err := os.Mkdir(directory, 0o700); err != nil {
		return V3Snapshot{}, err
	}
	for _, state := range v3NormalOrder[:v3StateSequence(V3LayoutSwitched)+1] {
		name := v3Segments[state]
		data := records[name]
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, data, 0o400); err != nil {
			return V3Snapshot{}, err
		}
		if err := os.Chmod(path, 0o400); err != nil {
			return V3Snapshot{}, err
		}
	}
	completed = true
	return snapshot, nil
}

// CleanupCA42E2EJournal removes only the fixed tagged-fixture root after the
// same inherited namespace attestation succeeds.
func CleanupCA42E2EJournal() error {
	if err := requireCA42E2EJournalIsolation(); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(productionV3ParentPath, productionV3RootName))
}

func requireCA42E2EJournalIsolation() error {
	if os.Getenv("PANDORA_CA42_E2E_ISOLATED") != "1" || os.Geteuid() != 0 {
		return errors.New("release journal CA42 E2E isolation unavailable")
	}
	for fd, kind := range map[int]string{8: "pid", 9: "mnt"} {
		link, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
		if err != nil {
			return fmt.Errorf("release journal CA42 E2E host %s namespace unavailable: %w", kind, err)
		}
		if !strings.HasPrefix(link, kind+":[") {
			return fmt.Errorf("release journal CA42 E2E host %s namespace descriptor invalid", kind)
		}
		var host, current unix.Stat_t
		if err := unix.Fstat(fd, &host); err != nil {
			return err
		}
		if err := unix.Stat("/proc/thread-self/ns/"+kind, &current); err != nil {
			return err
		}
		if host.Dev == current.Dev && host.Ino == current.Ino {
			return fmt.Errorf("release journal CA42 E2E %s namespace did not diverge from host", kind)
		}
	}
	for _, root := range []string{"/var/lib", "/run", "/root"} {
		var filesystem unix.Statfs_t
		if err := unix.Statfs(root, &filesystem); err != nil {
			return err
		}
		if filesystem.Type != unix.TMPFS_MAGIC {
			return fmt.Errorf("release journal CA42 E2E isolation root is not tmpfs: %s", root)
		}
	}
	var fixtureFilesystem unix.Statfs_t
	if err := unix.Statfs(productionV3ParentPath, &fixtureFilesystem); err != nil {
		return err
	}
	if fixtureFilesystem.Type != unix.EXT4_SUPER_MAGIC {
		return errors.New("release journal CA42 E2E production parent is not ext4")
	}
	return nil
}

func ca42E2ELayoutSwitchedRecords(input CA42E2EJournalInput) (map[string][]byte, V3Snapshot, error) {
	prepared, _, err := v3PreparedRecordBytes(v3PreparedFields{
		JournalID: input.JournalID, AttemptID: input.AttemptID,
		ReleaseContractCoreSHA256: input.ReleaseContractCoreSHA256,
		ControllerSHA256:          input.ControllerSHA256, CreatedAtEpoch: input.CreatedAtEpoch,
	})
	if err != nil {
		return nil, V3Snapshot{}, err
	}
	records := map[string][]byte{v3Segments[V3Prepared]: prepared}
	snapshot, err := ParseV3(records)
	if err != nil {
		return nil, V3Snapshot{}, err
	}
	for snapshot.State() != V3LayoutSwitched {
		sequence := snapshot.Sequence() + 1
		next := v3NormalOrder[sequence]
		data, _, recordErr := v3GenericRecordBytes(v3GenericFields{
			JournalID: input.JournalID, AttemptID: input.AttemptID, Sequence: sequence,
			EventID:         SHA256Bytes([]byte("ca42-e2e-event-" + string(next))),
			OccurredAtEpoch: input.CreatedAtEpoch + int64(sequence),
			PreviousState:   snapshot.State(), State: next,
			PreviousRecordSHA256: snapshot.HeadSHA256(),
			EvidenceSHA256:       SHA256Bytes([]byte("ca42-e2e-evidence-" + string(next))), EvidenceSize: 1,
		})
		if recordErr != nil {
			return nil, V3Snapshot{}, recordErr
		}
		records[v3Segments[next]] = data
		snapshot, err = ParseV3(records)
		if err != nil {
			return nil, V3Snapshot{}, err
		}
	}
	return records, snapshot, nil
}
