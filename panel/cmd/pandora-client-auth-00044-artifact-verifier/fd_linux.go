//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

type fdSpec struct {
	name      string
	fd        int
	maxBytes  int64
	exact     int64
	sensitive bool
}

type fdIdentity struct {
	dev   uint64
	ino   uint64
	mode  uint32
	uid   uint32
	nlink uint64
	size  int64
}

func executePlatform(config cliConfig) ([]byte, error) {
	if os.Geteuid() != 0 {
		return nil, verifyFailure(errors.New("root required"))
	}
	specs := []fdSpec{
		{name: "source", fd: config.SourceFD, maxBytes: maxSourceBytes},
		{name: "artifact", fd: config.ArtifactFD, maxBytes: maxArtifactBytes},
		{name: "detached", fd: config.DetachedFD, maxBytes: maxEnvelopeBytes},
		{name: "expectations", fd: config.ExpectationsFD, maxBytes: maxEnvelopeBytes},
		{name: "artifact key", fd: config.ArtifactKeyFD, maxBytes: 32, exact: 32, sensitive: true},
		{name: "evidence key", fd: config.EvidenceKeyFD, maxBytes: 32, exact: 32, sensitive: true},
	}

	identities := make([]fdIdentity, len(specs))
	seen := make(map[[2]uint64]string, len(specs))
	for i, spec := range specs {
		identity, err := inspectFD(spec)
		if err != nil {
			return nil, err
		}
		object := [2]uint64{identity.dev, identity.ino}
		if prior, ok := seen[object]; ok {
			return nil, verifyFailure(fmt.Errorf("fd object alias: %s and %s", prior, spec.name))
		}
		seen[object] = spec.name
		identities[i] = identity
	}

	contents := make([][]byte, len(specs))
	for i, spec := range specs {
		content, err := readFD(spec)
		if err != nil {
			return nil, err
		}
		if spec.sensitive {
			defer zeroBytes(content)
		}
		if int64(len(content)) != identities[i].size {
			return nil, verifyFailure(fmt.Errorf("read length differs from identity: %s", spec.name))
		}
		contents[i] = content
	}

	for i, spec := range specs {
		identity, err := statIdentity(spec.fd)
		if err != nil {
			return nil, ioFailure(fmt.Errorf("post-read fstat %s: %w", spec.name, err))
		}
		if identity != identities[i] {
			return nil, verifyFailure(fmt.Errorf("fd identity changed: %s", spec.name))
		}
	}

	receipt, err := verifyAll(verificationInputs{
		Argv:         config.Argv,
		Environment:  os.Environ(),
		SourceFormat: config.SourceFormat,
		Source:       contents[0], Artifact: contents[1], Detached: contents[2], Expectations: contents[3],
		ArtifactKeyID: config.ArtifactKeyID, ArtifactKey: contents[4],
		EvidenceKeyID: config.EvidenceKeyID, EvidenceKey: contents[5],
	})
	if err != nil {
		return nil, verifyFailure(err)
	}
	return receipt, nil
}

func inspectFD(spec fdSpec) (fdIdentity, error) {
	identity, err := statIdentity(spec.fd)
	if err != nil {
		return fdIdentity{}, ioFailure(fmt.Errorf("fstat %s: %w", spec.name, err))
	}
	if identity.uid != 0 || identity.mode&syscall.S_IFMT != syscall.S_IFREG || identity.nlink != 1 || identity.mode&0o077 != 0 {
		return fdIdentity{}, verifyFailure(fmt.Errorf("fd policy: %s", spec.name))
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(spec.fd), uintptr(syscall.F_GETFL), 0)
	if errno != 0 {
		return fdIdentity{}, ioFailure(fmt.Errorf("fcntl %s: %w", spec.name, errno))
	}
	if int(flags)&syscall.O_ACCMODE != syscall.O_RDONLY {
		return fdIdentity{}, verifyFailure(fmt.Errorf("fd not read-only: %s", spec.name))
	}
	offset, err := syscall.Seek(spec.fd, 0, io.SeekCurrent)
	if err != nil {
		return fdIdentity{}, ioFailure(fmt.Errorf("seek %s: %w", spec.name, err))
	}
	if offset != 0 || identity.size < 0 || identity.size > spec.maxBytes || spec.exact != 0 && identity.size != spec.exact {
		return fdIdentity{}, verifyFailure(fmt.Errorf("fd size or offset: %s", spec.name))
	}
	return identity, nil
}

func statIdentity(fd int) (fdIdentity, error) {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return fdIdentity{}, err
	}
	return fdIdentity{dev: uint64(stat.Dev), ino: stat.Ino, mode: stat.Mode, uid: stat.Uid, nlink: uint64(stat.Nlink), size: stat.Size}, nil
}

func readFD(spec fdSpec) ([]byte, error) {
	content := make([]byte, 0, min(spec.maxBytes, 64<<10))
	success := false
	if spec.sensitive {
		defer func() {
			if !success {
				zeroBytes(content)
			}
		}()
	}
	buffer := make([]byte, 32<<10)
	if spec.sensitive {
		defer zeroBytes(buffer)
	}
	for {
		n, err := syscall.Read(spec.fd, buffer)
		if n > 0 {
			if int64(len(content))+int64(n) > spec.maxBytes {
				return nil, verifyFailure(fmt.Errorf("read limit: %s", spec.name))
			}
			content = append(content, buffer[:n]...)
		}
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return nil, ioFailure(fmt.Errorf("read %s: %w", spec.name, err))
		}
		if n == 0 {
			break
		}
	}
	if spec.exact != 0 && int64(len(content)) != spec.exact {
		return nil, verifyFailure(fmt.Errorf("read length: %s", spec.name))
	}
	success = true
	return content, nil
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
