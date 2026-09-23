package ca42runner

import (
	"context"
	"errors"
	"hash"
	"io"
)

const artifactHashBufferSize = 64 << 10

// hashReaderAt hashes exactly size bytes without changing the source offset or
// allocating in proportion to the artifact size.
func hashReaderAt(ctx context.Context, hasher hash.Hash, reader io.ReaderAt, size int64) error {
	if ctx == nil || hasher == nil || reader == nil || size <= 0 {
		return errors.New("CA42 retained artifact stream request invalid")
	}
	buffer := make([]byte, artifactHashBufferSize)
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := int64(len(buffer))
		if remaining := size - offset; remaining < want {
			want = remaining
		}
		n, err := reader.ReadAt(buffer[:want], offset)
		if n > 0 {
			if _, writeErr := hasher.Write(buffer[:n]); writeErr != nil {
				return writeErr
			}
			offset += int64(n)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if n == 0 || (errors.Is(err, io.EOF) && offset != size) {
			return errors.New("CA42 retained artifact short read")
		}
	}
	return nil
}
