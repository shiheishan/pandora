package certstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultFileRoot 是 file 模式证书唯一允许的目录（目录 0750、文件 0640、属主 root:pandora）。
	DefaultFileRoot = "/etc/pandora-native/certs"
	// DefaultPollInterval 是 file 模式按 mtime/size 检查变化的间隔。
	DefaultPollInterval = 60 * time.Second
)

// stamp 是一次 stat 的结果：解析后的真实路径、大小、修改时间。三者任一变化就重读。
type stamp struct {
	resolved string
	size     int64
	mtime    time.Time
}

func (a stamp) equal(b stamp) bool {
	return a.resolved == b.resolved && a.size == b.size && a.mtime.Equal(b.mtime)
}

type fileWatch struct {
	key      Key
	certPath string
	keyPath  string
	cert     stamp
	priv     stamp
}

// FileSource 管 file 模式证书：路径必须经符号链接解析后仍落在 root 下，
// 每 DefaultPollInterval 按 mtime/size 检查一次，变了就重读；读失败保留旧证书并记录错误。
type FileSource struct {
	root    string
	store   *Store
	now     func() time.Time
	mu      sync.Mutex
	watches map[Key]*fileWatch
}

// NewFileSource 建 file 来源；root 通常是 DefaultFileRoot，测试注入临时目录。
func NewFileSource(root string, store *Store) *FileSource {
	return &FileSource{root: filepath.Clean(root), store: store, now: time.Now, watches: make(map[Key]*fileWatch)}
}

// Register 校验路径并首次加载，返回给 Resolve 用的键。重复登记同一对路径是幂等的。
// 首次加载失败返回错误（调用方据此让入站在 Validate 阶段失败），状态里也记一笔；
// 只有路径不是绝对路径时返回零值键且不登记。
func (f *FileSource) Register(certPath, keyPath string) (Key, error) {
	if !filepath.IsAbs(certPath) || !filepath.IsAbs(keyPath) {
		return Key{}, newLoadError(ErrCodePathEscape, errors.New("certstore: cert_path and key_path must be absolute"))
	}
	// 键是 sha256(cert_path|key_path)：路径里不许有竖线或 NUL，保证不同路径对不会撞成同一个键。
	if strings.ContainsAny(certPath+keyPath, "|\x00") {
		return Key{}, newLoadError(ErrCodePathEscape, errors.New("certstore: cert_path and key_path must not contain '|' or NUL"))
	}
	certPath, keyPath = filepath.Clean(certPath), filepath.Clean(keyPath)
	k := FileKey(certPath, keyPath)
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.watches[k]; ok && f.store.Current(k) != nil {
		return k, nil
	}
	w := &fileWatch{key: k, certPath: certPath, keyPath: keyPath}
	// 首次加载失败也纳入监视：文件修好后下一轮 Poll 自动恢复、错误随之清掉。
	// 不再引用时调用方要 Unregister。
	f.watches[k] = w
	if err := f.reload(w); err != nil {
		return k, err
	}
	return k, nil
}

// Unregister 停止监视并清空内存指针（入站不再引用这对文件时调用）。
func (f *FileSource) Unregister(k Key) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.watches[k]; !ok {
		return
	}
	delete(f.watches, k)
	f.store.remove(k)
}

// Poll 检查一轮全部登记的文件，有变化的重读。
func (f *FileSource) Poll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]Key, 0, len(f.watches))
	for k := range f.watches {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ID < keys[j].ID })
	for _, k := range keys {
		w := f.watches[k]
		cs, cerr := f.stat(w.certPath)
		ks, kerr := f.stat(w.keyPath)
		if err := errors.Join(cerr, kerr); err != nil {
			// 清空印记：文件恢复后哪怕 mtime/size 与之前相同也会重读一次，错误状态随之清掉。
			w.cert, w.priv = stamp{}, stamp{}
			f.store.fail(k, f.baseStatus(w), errorCode(err), err)
			continue
		}
		if cs.equal(w.cert) && ks.equal(w.priv) {
			continue
		}
		_ = f.reload(w)
	}
}

// Run 每 interval 调一次 Poll，直到 ctx 结束。interval ≤ 0 时用 DefaultPollInterval。
func (f *FileSource) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.Poll()
		}
	}
}

func (f *FileSource) baseStatus(w *fileWatch) Status {
	return Status{Source: KindFile, CertPath: w.certPath, KeyPath: w.keyPath}
}

// reload 读两份文件并自检，通过才替换；失败保留旧证书并记错误。调用方持有 f.mu。
func (f *FileSource) reload(w *fileWatch) error {
	l, cs, ks, err := f.readPair(w)
	if err == nil {
		w.cert, w.priv = cs, ks
		f.store.put(w.key, l, f.baseStatus(w))
		return nil
	}
	// 读到了但内容不合格（坏 PEM、不配对、过期）：记下印记，同一份坏文件不反复重读，文件再变时自然重试。
	// 读不到（不存在、权限、逃逸）：清空印记，恢复后无论 mtime/size 是否同前都会重读。
	w.cert, w.priv = cs, ks
	f.store.fail(w.key, f.baseStatus(w), errorCode(err), err)
	return fmt.Errorf("certstore: load %s: %w", w.certPath, err)
}

func (f *FileSource) readPair(w *fileWatch) (*loaded, stamp, stamp, error) {
	chain, cs, err := f.read(w.certPath)
	if err != nil {
		return nil, stamp{}, stamp{}, err
	}
	keyPEM, ks, err := f.read(w.keyPath)
	if err != nil {
		return nil, stamp{}, stamp{}, err
	}
	l, err := parseKeyPair(chain, keyPEM, f.now())
	return l, cs, ks, err
}

// confine 先按字面检查 path 在 root 下，再 EvalSymlinks 后检查真实路径仍在（解析后的）root 下。
// 两道都过才放行：字面挡 ../，解析挡符号链接逃逸。
func (f *FileSource) confine(path string) (string, error) {
	if !within(f.root, filepath.Clean(path)) {
		return "", newLoadError(ErrCodePathEscape, fmt.Errorf("%s is outside %s", path, f.root))
	}
	root, err := filepath.EvalSymlinks(f.root)
	if err != nil {
		return "", newLoadError(ErrCodeReadFailed, err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", newLoadError(ErrCodeReadFailed, err)
	}
	if !within(root, resolved) {
		return "", newLoadError(ErrCodePathEscape, fmt.Errorf("%s resolves outside %s", path, f.root))
	}
	return resolved, nil
}

// within 判断 path 是否严格位于 root 之下（不含 root 本身）。
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (f *FileSource) stat(path string) (stamp, error) {
	resolved, err := f.confine(path)
	if err != nil {
		return stamp{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return stamp{}, newLoadError(ErrCodeReadFailed, err)
	}
	if !info.Mode().IsRegular() {
		return stamp{}, newLoadError(ErrCodeReadFailed, fmt.Errorf("%s is not a regular file", path))
	}
	return stamp{resolved: resolved, size: info.Size(), mtime: info.ModTime()}, nil
}

// read 在校验过的真实路径上打开文件，读完后再解析一次路径并比对是同一个文件，
// 堵住「检查之后、打开之前把符号链接换到别处」的竞态。
func (f *FileSource) read(path string) ([]byte, stamp, error) {
	resolved, err := f.confine(path)
	if err != nil {
		return nil, stamp{}, err
	}
	fh, err := os.Open(resolved)
	if err != nil {
		return nil, stamp{}, newLoadError(ErrCodeReadFailed, err)
	}
	defer fh.Close()
	info, err := fh.Stat()
	if err != nil {
		return nil, stamp{}, newLoadError(ErrCodeReadFailed, err)
	}
	if !info.Mode().IsRegular() {
		return nil, stamp{}, newLoadError(ErrCodeReadFailed, fmt.Errorf("%s is not a regular file", path))
	}
	body, err := io.ReadAll(io.LimitReader(fh, maxPEMSize+1))
	if err != nil {
		return nil, stamp{}, newLoadError(ErrCodeReadFailed, err)
	}
	if len(body) > maxPEMSize {
		return nil, stamp{}, newLoadError(ErrCodeBadPEM, fmt.Errorf("%s is larger than %d bytes", path, maxPEMSize))
	}
	again, err := f.confine(path)
	if err != nil {
		return nil, stamp{}, err
	}
	now, err := os.Stat(again)
	if err != nil || !os.SameFile(info, now) {
		return nil, stamp{}, newLoadError(ErrCodeReadFailed, fmt.Errorf("%s changed while reading", path))
	}
	return body, stamp{resolved: resolved, size: info.Size(), mtime: info.ModTime()}, nil
}
