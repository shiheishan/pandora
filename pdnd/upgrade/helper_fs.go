package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
)

// openStateDir：parent 是 StateParent（缺省 /var/lib/pandora-native）。
// os.OpenRoot(parent) → p.OpenRoot(StateSubdir) → 关掉 p，返回子 Root（调用方 Close）。子目录不存在时返回的错误能被 errors.Is(err, fs.ErrNotExist) 认出。
func openStateDir(parent string) (*os.Root, error) {
	p, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	state, err := p.OpenRoot(StateSubdir)
	_ = p.Close()
	if err != nil {
		return nil, err
	}
	return state, nil
}

// openStateFile：暂存目录里的 name 以只读打开并核对：
//
//	fi := state.Lstat(name)，必须 fi.Mode().IsRegular()（符号链接、目录、FIFO、设备一律拒），fi.Size() <= max；
//	f := state.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
//	（O_NOFOLLOW：Lstat 与打开之间被换成符号链接也打不开；O_NONBLOCK：被换成 FIFO 时不会卡住）；
//	st := f.Stat()，必须 os.SameFile(fi, st) 且 st.Mode().IsRegular() 且 st.Size() <= max；不满足就关掉 f 返回错误。
func openStateFile(state *os.Root, name string, max int64) (*os.File, error) {
	fi, err := state.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: 不是普通文件", name)
	}
	if fi.Size() > max {
		return nil, fmt.Errorf("%s: 超过大小上限", name)
	}
	f, err := state.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !os.SameFile(fi, st) || !st.Mode().IsRegular() || st.Size() > max {
		_ = f.Close()
		return nil, fmt.Errorf("%s: 打开后与 Lstat 不一致或非普通文件", name)
	}
	return f, nil
}

// readState：openStateFile 后 io.ReadAll(io.LimitReader(f, max+1))，读到超过 max 报错。
func readState(state *os.Root, name string, max int64) ([]byte, error) {
	f, err := openStateFile(state, name, max)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s: 内容超过大小上限", name)
	}
	return data, nil
}

// copyFromState：openStateFile 后把内容拷到 dst（dst 在 root 自己的工作区里）：
//
//	写 dst+".tmp"（O_WRONLY|O_CREATE|O_TRUNC，0600），io.Copy 至多 max+1 字节、超过 max 报错并删掉 tmp，Sync，Close，os.Rename 到 dst。
func copyFromState(state *os.Root, name, dst string, max int64) error {
	f, err := openStateFile(state, name, max)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		_ = f.Close()
		return err
	}
	n, err := io.Copy(out, io.LimitReader(f, max+1))
	_ = f.Close()
	if err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if n > max {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("%s: 拷贝超过大小上限", name)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// writeState：root 往 pandora 属主目录里原子写 name（result、applying 用）：
//
//	先 state.Remove("."+name+".tmp")（忽略不存在；删的是链接本身，不跟随）；
//	f := state.OpenFile("."+name+".tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)（O_EXCL：被抢先放了符号链接也失败）；
//	写入、f.Chmod(0o644)（显式设，不受单元的 UMask=0077 影响，pandora 才读得到）、Sync、Close；state.Rename(tmp, name)（rename 换掉的是目录项本身，name 预先是符号链接也不会写穿）。
//	任一步失败删掉 tmp。
func writeState(state *os.Root, name string, data []byte) error {
	tmp := "." + name + ".tmp"
	_ = state.Remove(tmp)
	f, err := state.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = state.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		_ = state.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = state.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = state.Remove(tmp)
		return err
	}
	if err := state.Rename(tmp, name); err != nil {
		_ = state.Remove(tmp)
		return err
	}
	return nil
}

// removeState：state.Remove(name)，不存在不算错。
func removeState(state *os.Root, name string) error {
	err := state.Remove(name)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ensureWorkDir：root 自己的工作区（缺省 /var/lib/pandora-native-upgrade，上级 /var/lib 只有 root 可写）：
//
//	os.MkdirAll(dir, 0o700)；os.Lstat 必须是目录且不是符号链接；os.Chmod(dir, 0o700)。
func ensureWorkDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s: 是符号链接", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s: 不是目录", dir)
	}
	return os.Chmod(dir, 0o700)
}

// writeFileSync：在 root 自己的目录里原子写：path+".tmp"（O_WRONLY|O_CREATE|O_TRUNC）→ 写 → Chmod(mode) → Sync → Close → Rename。
func writeFileSync(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// copyFileSync：src 必须是普通文件（os.Lstat）；拷到 dst+".tmp" → Chmod(mode) → Sync → Close → Rename 成 dst。
// 用于 /usr/local/bin 下的 .prev / .new（root 属主目录，路径可信）。
func copyFileSync(src, dst string, mode os.FileMode) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: 不是普通文件", src)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		_ = in.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = in.Close()
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	_ = in.Close()
	if err := out.Chmod(mode); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// sha256File：os.Lstat 必须是普通文件；返回小写 hex。
func sha256File(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s: 不是普通文件", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
