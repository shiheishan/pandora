package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const downloadTimeout = 5 * time.Minute

// redactURL 只保留 scheme、host、path，去掉查询串等可能含令牌的部分。
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return u.Scheme + "://" + u.Host + path
}

func schemeAllowed(scheme string, allowHTTP bool) bool {
	if scheme == "https" {
		return true
	}
	return allowHTTP && scheme == "http"
}

// download 将 rawURL 指向的二进制下载到 dst，返回内容的 sha256 小写 hex。
func (a *Agent) download(ctx context.Context, rawURL, dst string, max int64) (string, error) {
	if max <= 0 {
		max = MaxBinaryBytes
	}

	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		// url.Parse 与 NewRequest 的错误里带完整地址（查询串可能有令牌），不往外带。
		return "", Errorf(ReasonDownloadFailed, "下载地址无效")
	}

	client := &http.Client{
		Transport: a.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("跳转次数超过上限")
			}
			if !schemeAllowed(req.URL.Scheme, a.AllowHTTP) {
				return errors.New("不跟随到非 https 的跳转")
			}
			return nil
		},
	}
	if client.Transport == nil {
		client.Transport = http.DefaultTransport
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", Errorf(ReasonDownloadFailed, "构造下载请求失败：%s", redactURL(parsed))
	}

	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			inner := ue.Err
			if inner == nil {
				inner = err
			}
			return "", Errorf(ReasonDownloadFailed, "下载失败：%s：%v", redactURL(parsed), inner)
		}
		return "", Errorf(ReasonDownloadFailed, "下载失败：%s：%v", redactURL(parsed), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", Errorf(ReasonDownloadFailed, "下载返回 HTTP %d", resp.StatusCode)
	}

	if resp.ContentLength > max {
		return "", Errorf(ReasonDownloadFailed, "超过上限 %d 字节", max)
	}

	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", Errorf(ReasonDownloadFailed, "打开暂存文件失败：%v", err)
	}

	h := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, max+1))
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		var ue *url.Error
		if errors.As(copyErr, &ue) {
			inner := ue.Err
			if inner == nil {
				inner = copyErr
			}
			return "", Errorf(ReasonDownloadFailed, "下载失败：%s：%v", redactURL(parsed), inner)
		}
		return "", Errorf(ReasonDownloadFailed, "下载失败：%s：%v", redactURL(parsed), copyErr)
	}
	if syncErr != nil {
		_ = os.Remove(dst)
		return "", Errorf(ReasonDownloadFailed, "同步暂存文件失败：%v", syncErr)
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return "", Errorf(ReasonDownloadFailed, "关闭暂存文件失败：%v", closeErr)
	}
	if written > max {
		_ = os.Remove(dst)
		return "", Errorf(ReasonDownloadFailed, "超过上限 %d 字节", max)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
