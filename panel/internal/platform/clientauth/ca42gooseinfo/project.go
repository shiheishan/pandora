package ca42gooseinfo

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
)

// ProjectFromRetainedReaderAt derives the canonical manifest from the same
// retained Goose bytes. It never executes Goose and never reopens a pathname.
func ProjectFromRetainedReaderAt(ctx context.Context, reader io.ReaderAt, size uint64, expectedBinarySHA256, architecture string) ([]byte, error) {
	if ctx == nil || reader == nil || size == 0 || size > MaxBinaryBytes || !nonZeroHex64(expectedBinarySHA256) || !h1sum.MatchString(RequiredMainModuleSum) {
		return nil, errors.New("Goose retained build projection input invalid")
	}
	before, err := hashReaderAt(ctx, reader, size)
	if err != nil || hex.EncodeToString(before[:]) != expectedBinarySHA256 {
		return nil, errors.New("Goose retained binary identity mismatch")
	}
	bounded := io.NewSectionReader(reader, 0, int64(size))
	info, err := buildinfo.Read(bounded)
	if err != nil || info.Path != RequiredCommandPath || info.Main.Path != RequiredMainModulePath ||
		info.Main.Version != RequiredGooseVersion || info.Main.Sum != RequiredMainModuleSum || info.Main.Replace != nil ||
		info.GoVersion != RequiredGoVersion {
		return nil, errors.New("Goose retained Go build info invalid")
	}
	dependencySHA, dependencyCount, err := DependencySetSHA256(info.Deps)
	if err != nil {
		return nil, err
	}
	settingSHA, settingCount, err := BuildSettingSetSHA256(info.Settings, architecture)
	if err != nil {
		return nil, err
	}
	elfFile, err := elf.NewFile(io.NewSectionReader(reader, 0, int64(size)))
	if err != nil {
		return nil, errors.New("Goose retained ELF invalid")
	}
	machine := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[architecture]
	if machine == 0 || elfFile.Class != elf.ELFCLASS64 || elfFile.Data != elf.ELFDATA2LSB ||
		elfFile.OSABI != elf.ELFOSABI_NONE || elfFile.Type != elf.ET_EXEC || elfFile.Machine != machine {
		return nil, errors.New("Goose retained ELF header invalid")
	}
	for _, program := range elfFile.Progs {
		if program.Type == elf.PT_INTERP || program.Type == elf.PT_DYNAMIC {
			return nil, errors.New("Goose retained ELF is dynamically linked")
		}
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	after, err := hashReaderAt(ctx, reader, size)
	if err != nil || before != after {
		return nil, errors.New("Goose retained binary changed during projection")
	}
	values := []string{
		Format, "goose", expectedBinarySHA256, strconv.FormatUint(size, 10), architecture, RequiredGoVersion,
		RequiredCommandPath, RequiredMainModulePath, RequiredGooseVersion, RequiredMainModuleSum, "none",
		strconv.FormatUint(dependencyCount, 10), dependencySHA, strconv.FormatUint(settingCount, 10), settingSHA,
		"false", "exe", "true", "git", RequiredVCSRevision, RequiredVCSTime, "false",
		"ELFCLASS64", "ELFDATA2LSB", "ELFOSABI_NONE", "ET_EXEC", map[string]string{"amd64": "EM_X86_64", "arm64": "EM_AARCH64"}[architecture],
		"absent", "0", "absent", "absent",
	}
	return CanonicalBytes(values)
}

func hashReaderAt(ctx context.Context, reader io.ReaderAt, size uint64) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	hasher := sha256.New()
	buffer := make([]byte, 1<<20)
	var offset int64
	for uint64(offset) < size {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		want := uint64(len(buffer))
		if remaining := size - uint64(offset); remaining < want {
			want = remaining
		}
		count, err := reader.ReadAt(buffer[:want], offset)
		if count > 0 {
			_, _ = hasher.Write(buffer[:count])
			offset += int64(count)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return empty, errors.New("Goose retained binary read failed")
		}
		if count == 0 {
			return empty, errors.New("Goose retained binary truncated")
		}
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}
