package kernel

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/core/mieru"
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
)

// mieruAdapter keeps Mieru's wire listener behind the same NativeCore adapter
// contract. Both TCP and Mieru's packet-over-stream UDP egress are routed
// through Pandora's DataPlane; the wire framing remains Mieru-owned.
type mieruAdapter struct {
	spec   InboundSpec
	inner  *mieru.Inbound
	cancel context.CancelFunc
}

func newMieruAdapter(spec InboundSpec) (Adapter, error) {
	transport := strings.TrimSpace(rawString(spec.Config.Raw, "transport"))
	if transport == "" {
		transport = "TCP"
	}
	return &mieruAdapter{spec: spec, inner: mieru.New(spec.Config.Tag, spec.Config.Port, transport, slog.Default())}, nil
}

func (a *mieruAdapter) Protocol() string { return "mieru" }

func (a *mieruAdapter) Validate(spec InboundSpec) error {
	if !strings.EqualFold(spec.Config.Protocol, "mieru") {
		return fmt.Errorf("native mieru received protocol %q", spec.Config.Protocol)
	}
	if spec.Config.Port < 1 || spec.Config.Port > 65535 {
		return fmt.Errorf("mieru port is invalid")
	}
	transport := strings.ToLower(strings.TrimSpace(rawString(spec.Config.Raw, "transport")))
	if transport != "" && transport != "tcp" && transport != "udp" {
		return fmt.Errorf("native mieru transport must be tcp or udp")
	}
	return nil
}

func (a *mieruAdapter) Start(ctx context.Context, spec InboundSpec, hooks AdapterHooks) error {
	if ctx == nil || hooks.DataPlane == nil {
		return fmt.Errorf("native mieru start requires context and data plane")
	}
	a.inner.SetTransport(mieruTransport{plane: hooks.DataPlane})
	localCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	go func() {
		<-localCtx.Done()
		_ = a.inner.Close()
	}()
	return a.inner.Start()
}

func (a *mieruAdapter) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	return a.inner.Close()
}
func (a *mieruAdapter) AddUsers(users []core.User) error             { return a.inner.AddUsers(users) }
func (a *mieruAdapter) UpsertUsers(users []core.User) error          { return a.inner.UpsertUsers(users) }
func (a *mieruAdapter) DelUsers(ids []string) error                  { return a.inner.DelUsers(ids) }
func (a *mieruAdapter) SnapshotTraffic() ([]core.UserTraffic, error) { return a.inner.Traffic(), nil }
func (a *mieruAdapter) OnlineIPs() map[int64][]string                { return a.inner.Online() }

type mieruTransport struct{ plane DataPlane }

func (t mieruTransport) DialTCP(ctx context.Context, meta route.Meta, destination M.Socksaddr) (net.Conn, error) {
	return t.plane.DialTCP(ctx, meta, destination)
}

func (t mieruTransport) ListenUDP(ctx context.Context, meta route.Meta, destination M.Socksaddr) (net.PacketConn, error) {
	return t.plane.ListenUDP(ctx, meta, destination)
}
