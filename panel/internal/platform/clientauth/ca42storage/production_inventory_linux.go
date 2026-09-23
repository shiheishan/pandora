//go:build linux && (amd64 || arm64)

package ca42storage

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type productionInventoryBinder struct {
	root          *os.File
	stat          unix.Stat_t
	mountID       uint64
	ancestors     map[string]*productionInventoryAncestor
	ancestorOrder []string
}

type productionInventoryAncestor struct {
	file    *os.File
	stat    unix.Stat_t
	mountID uint64
}

// RetainProductionInventory is the only production inventory opener. It has
// no path, root FD, source FD, clock, or syscall parameters: every pathname is
// taken from the opaque plan-bound descriptor and resolved beneath a retained
// descriptor for the fixed filesystem root.
func RetainProductionInventory(ctx context.Context, descriptor BoundDescriptor) (_ *InventoryLease, resultErr error) {
	if ctx == nil {
		return nil, errors.New("production fs-verity inventory context invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	binder, err := openProductionInventoryBinder()
	if err != nil {
		return nil, err
	}
	binderOwned := true
	defer func() {
		if binderOwned {
			resultErr = errors.Join(resultErr, binder.close())
		}
	}()

	now := time.Now().UTC()
	trusted, entries, err := descriptor.cachedEntriesAt(now)
	if err != nil || len(entries) == 0 {
		return nil, errors.Join(errors.New("production fs-verity inventory descriptor invalid"), err)
	}
	sources := make([]*os.File, 0, len(entries))
	sourcesOwned := true
	closeSources := func() error {
		var closeErr error
		for index := len(sources) - 1; index >= 0; index-- {
			if sources[index] != nil {
				closeErr = errors.Join(closeErr, sources[index].Close())
				sources[index] = nil
			}
		}
		return closeErr
	}
	defer func() {
		if sourcesOwned {
			resultErr = errors.Join(resultErr, closeSources())
		}
	}()
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source, openErr := binder.openEntryContext(ctx, entry, time.Now().UTC())
		if openErr != nil {
			return nil, openErr
		}
		sources = append(sources, source)
	}

	lease, err := retainInventoryWithOps(ctx, trusted, sources, time.Now, platformInventoryOps())
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			resultErr = errors.Join(resultErr, lease.Close())
		}
	}()
	if err := closeSources(); err != nil {
		return nil, errors.Join(errors.New("production fs-verity inventory source close failed"), err)
	}
	sourcesOwned = false
	if err := binder.validate(ctx); err != nil {
		return nil, err
	}
	finalNow := time.Now().UTC()
	_, finalEntries, err := trusted.cachedEntriesAt(finalNow)
	if err != nil || len(finalEntries) != len(entries) {
		return nil, errors.Join(errors.New("production fs-verity inventory final descriptor invalid"), err)
	}
	for _, entry := range finalEntries {
		if err := binder.rebind(ctx, entry, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	publishNow := time.Now().UTC()
	_, publishEntries, err := trusted.cachedEntriesAt(publishNow)
	if err != nil || len(publishEntries) != len(finalEntries) {
		return nil, errors.Join(errors.New("production fs-verity inventory publication descriptor invalid"), err)
	}
	for index := range publishEntries {
		if publishEntries[index].seal != finalEntries[index].seal {
			return nil, errors.New("production fs-verity inventory publication identity changed")
		}
	}
	if err := binder.validate(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lease.state.mu.Lock()
	if lease.state.closed || lease.state.poisoned || lease.state.binder != nil {
		lease.state.mu.Unlock()
		return nil, errors.New("production fs-verity inventory lease state invalid")
	}
	lease.state.binder = binder
	lease.state.mu.Unlock()
	binderOwned = false
	success = true
	return lease, nil
}

func openProductionInventoryBinder() (*productionInventoryBinder, error) {
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil || fd < 3 {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return nil, errors.New("production fs-verity inventory root unavailable")
	}
	root := os.NewFile(uintptr(fd), "pandora-ca42-production-root")
	if root == nil {
		_ = unix.Close(fd)
		return nil, errors.New("production fs-verity inventory root adoption failed")
	}
	stat, mountID, err := fdIdentity(root)
	if err != nil || !secureProductionDirectory(stat) {
		_ = root.Close()
		return nil, errors.New("production fs-verity inventory root identity invalid")
	}
	return &productionInventoryBinder{root: root, stat: stat, mountID: mountID,
		ancestors: make(map[string]*productionInventoryAncestor)}, nil
}

func (binder *productionInventoryBinder) validate(ctx context.Context) error {
	if binder == nil || binder.root == nil || int(binder.root.Fd()) < 3 || binder.mountID == 0 || ctx == nil {
		return errors.New("production fs-verity inventory binder invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, mountID, err := fdIdentity(binder.root)
	if err != nil || mountID != binder.mountID || !secureProductionDirectory(current) || !sameDirectoryIdentity(binder.stat, current) {
		return errors.New("production fs-verity inventory root binding changed")
	}
	for _, path := range binder.ancestorOrder {
		ancestor := binder.ancestors[path]
		if ancestor == nil || ancestor.file == nil || int(ancestor.file.Fd()) < 3 || ancestor.mountID == 0 {
			return errors.New("production fs-verity inventory ancestor binding invalid")
		}
		current, mountID, err := fdIdentity(ancestor.file)
		if err != nil || mountID != ancestor.mountID || !secureProductionDirectory(current) || !sameDirectoryIdentity(ancestor.stat, current) {
			return errors.New("production fs-verity inventory ancestor binding changed")
		}
	}
	return nil
}

func secureProductionDirectory(stat unix.Stat_t) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Uid == 0 && stat.Gid == 0 && stat.Mode&0o022 == 0
}

func sameDirectoryIdentity(before, after unix.Stat_t) bool {
	return before.Dev == after.Dev && before.Ino == after.Ino && before.Mode == after.Mode &&
		before.Uid == after.Uid && before.Gid == after.Gid
}

func (binder *productionInventoryBinder) openEntry(entry BoundEntry, now time.Time) (*os.File, error) {
	return binder.openEntryContext(context.Background(), entry, now)
}

func (binder *productionInventoryBinder) openEntryContext(ctx context.Context, entry BoundEntry, now time.Time) (*os.File, error) {
	if err := binder.validate(ctx); err != nil {
		return nil, err
	}
	snapshot, err := entry.verifiedEntryAt(now)
	if err != nil {
		return nil, errors.New("production fs-verity inventory entry invalid")
	}
	relative, err := canonicalInventoryRelative(snapshot.Path)
	if err != nil {
		return nil, err
	}
	components := strings.Split(relative, "/")
	parent := binder.root
	for index := 0; index < len(components)-1; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		prefix := strings.Join(components[:index+1], "/")
		how := &unix.OpenHow{
			Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		}
		fd, openErr := unix.Openat2(int(parent.Fd()), components[index], how)
		if openErr != nil || fd < 3 {
			if fd >= 0 {
				_ = unix.Close(fd)
			}
			return nil, errors.New("production fs-verity inventory ancestor open denied")
		}
		opened := os.NewFile(uintptr(fd), "pandora-ca42-production-ancestor")
		if opened == nil {
			_ = unix.Close(fd)
			return nil, errors.New("production fs-verity inventory ancestor adoption failed")
		}
		stat, mountID, identityErr := fdIdentity(opened)
		if identityErr != nil || mountID == 0 || !secureProductionDirectory(stat) {
			return nil, errors.Join(errors.New("production fs-verity inventory ancestor identity invalid"), opened.Close())
		}
		if retained := binder.ancestors[prefix]; retained != nil {
			if retained.mountID != mountID || !sameDirectoryIdentity(retained.stat, stat) {
				return nil, errors.Join(errors.New("production fs-verity inventory ancestor path changed"), opened.Close())
			}
			if err := opened.Close(); err != nil {
				return nil, errors.Join(errors.New("production fs-verity inventory ancestor close failed"), err)
			}
			parent = retained.file
			continue
		}
		binder.ancestors[prefix] = &productionInventoryAncestor{file: opened, stat: stat, mountID: mountID}
		binder.ancestorOrder = append(binder.ancestorOrder, prefix)
		parent = opened
	}
	how := &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	}
	fd, err := unix.Openat2(int(parent.Fd()), components[len(components)-1], how)
	if err != nil || fd < 3 {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return nil, errors.New("production fs-verity inventory entry open denied")
	}
	file := os.NewFile(uintptr(fd), "pandora-ca42-production-inventory")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("production fs-verity inventory entry adoption failed")
	}
	if err := validateProductionEntryFD(file, snapshot); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	runtime.KeepAlive(binder.root)
	return file, nil
}

func validateProductionEntryFD(file *os.File, entry Entry) error {
	if file == nil || int(file.Fd()) < 3 {
		return errors.New("production fs-verity inventory entry descriptor invalid")
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return errors.New("production fs-verity inventory entry not read-only")
	}
	fdFlags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || fdFlags&unix.FD_CLOEXEC == 0 {
		return errors.New("production fs-verity inventory entry not close-on-exec")
	}
	stat, mountID, err := fdIdentity(file)
	if err != nil || !entryMatchesStat(entry, stat, mountID) {
		return errors.New("production fs-verity inventory entry identity mismatch")
	}
	return nil
}

func (binder *productionInventoryBinder) rebind(ctx context.Context, entry BoundEntry, now time.Time) error {
	if err := binder.validate(ctx); err != nil {
		return err
	}
	file, err := binder.openEntryContext(ctx, entry, now)
	if err != nil {
		return errors.Join(errors.New("production fs-verity inventory path rebind failed"), err)
	}
	return file.Close()
}

func (binder *productionInventoryBinder) close() error {
	if binder == nil {
		return nil
	}
	var result error
	for index := len(binder.ancestorOrder) - 1; index >= 0; index-- {
		path := binder.ancestorOrder[index]
		if ancestor := binder.ancestors[path]; ancestor != nil && ancestor.file != nil {
			result = errors.Join(result, ancestor.file.Close())
			ancestor.file = nil
		}
		delete(binder.ancestors, path)
	}
	binder.ancestorOrder = nil
	binder.ancestors = nil
	if binder.root != nil {
		result = errors.Join(result, binder.root.Close())
	}
	binder.root = nil
	binder.stat = unix.Stat_t{}
	binder.mountID = 0
	return result
}

func canonicalInventoryRelative(path string) (string, error) {
	if path == "" || path[0] != '/' || path == "/" || strings.ContainsAny(path, "\x00\\") || strings.Contains(path, "//") {
		return "", errors.New("production fs-verity inventory path invalid")
	}
	relative := strings.TrimPrefix(path, "/")
	if relative == "" {
		return "", errors.New("production fs-verity inventory path invalid")
	}
	for _, component := range strings.Split(relative, "/") {
		if component == "" || component == "." || component == ".." {
			return "", errors.New("production fs-verity inventory path invalid")
		}
	}
	return relative, nil
}
