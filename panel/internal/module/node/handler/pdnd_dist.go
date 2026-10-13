// 节点端一键安装由面板自己分发二进制，落地机只需面板地址，不必持有 GitHub 凭据。
// 产物名白名单防止公开端点被拼路径读任意文件；校验和在 HTTPS 之外再证明拿到的就是面板发的那个；
// 安装脚本不含秘密，可公开下发。
package handler

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

// registerPdndDist 注册节点端一键安装的公开路由（public 引擎根路径，不登录）。
func (h *Handler) registerPdndDist(r gin.IRoutes) {
	r.GET("/pdnd/install.sh", h.pdndInstallScript)
	r.GET("/pdnd/bin/:name", h.pdndBinary)
	r.GET("/pdnd/sha256/:name", h.pdndChecksum)
}

func (h *Handler) pdndInstallScript(c *gin.Context) {
	script, err := h.svc.PdndInstallScript()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Content-Type", "text/x-shellscript; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/x-shellscript; charset=utf-8", []byte(script))
}

func (h *Handler) pdndBinary(c *gin.Context) {
	name := c.Param("name")
	a, err := h.svc.OpenPdndArtifact(name)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	defer a.File.Close()
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", name))
	http.ServeContent(c.Writer, c.Request, name, a.ModTime, a.File)
}

func (h *Handler) pdndChecksum(c *gin.Context) {
	name := c.Param("name")
	body, err := h.svc.PdndChecksum(name)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(body))
}
