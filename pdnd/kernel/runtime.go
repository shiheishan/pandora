// Package kernel is Pandora's protocol-independent data-plane runtime.
//
// It owns one route engine and one outbound generation per inbound. Sing-box,
// Xray, and standalone compatibility processes are deliberately not imported
// here: protocol adapters will call this package, not the other way around.
package kernel

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
)

const (
	DirectTag = "direct"
	BlockTag  = "block"
)

// Runtime is an immutable routing generation after Build succeeds. The
// outbound Set owns connection leases and performs safe replacement; Runtime
// only chooses a target and delegates the actual transport operation.
type Runtime struct {
	engine   *route.Engine
	outbound *outbound.Set
	once     sync.Once
}

// Build compiles one inbound's routing configuration with fail-closed rules.
// An absent configuration means direct-only operation. Unknown outbound types,
// unknown rule targets, malformed matchers, and missing GeoIP data are errors.
func Build(cfg *core.Routing) (*Runtime, error) {
	set := outbound.NewSet()
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = set.Close()
		}
	}()

	direct, err := outbound.New(outbound.Options{Tag: DirectTag, Type: "direct"})
	if err != nil {
		return nil, fmt.Errorf("创建 direct 出站: %w", err)
	}
	block, err := outbound.New(outbound.Options{Tag: BlockTag, Type: "block"})
	if err != nil {
		return nil, fmt.Errorf("创建 block 出站: %w", err)
	}
	objects := map[string]outbound.Outbound{DirectTag: direct, BlockTag: block}
	dialer, ok := direct.(outbound.Dialer)
	if !ok {
		return nil, fmt.Errorf("direct 出站未实现上游拨号器")
	}

	var rules []route.RawRule
	final := DirectTag
	if cfg != nil {
		if strings.TrimSpace(cfg.Final) != "" {
			final = strings.TrimSpace(cfg.Final)
		}
		for _, item := range cfg.Outbounds {
			if item.Tag == "" || item.Type == "" {
				return nil, fmt.Errorf("出站必须同时提供 tag 和 type")
			}
			if _, exists := objects[item.Tag]; exists {
				return nil, fmt.Errorf("出站标签 %q 重复或覆盖内建出站", item.Tag)
			}
			o, err := outbound.New(outbound.Options{
				Tag:      item.Tag,
				Type:     item.Type,
				Settings: item.Settings,
				Dialer:   dialer,
			})
			if err != nil {
				return nil, fmt.Errorf("创建出站 %q: %w", item.Tag, err)
			}
			objects[item.Tag] = o
		}
		for _, item := range cfg.Routes {
			rules = append(rules, route.RawRule{
				Matcher:     item.Matcher,
				OutboundTag: item.OutboundTag,
			})
		}
	}

	if err := set.Replace(objects); len(err) > 0 {
		return nil, fmt.Errorf("安装出站 generation: %v", err)
	}
	engine, err := route.CompileStrict(rules, set.Tags(), final, nil)
	if err != nil {
		_ = set.Close()
		return nil, fmt.Errorf("严格编译路由: %w", err)
	}
	closeOnError = false
	return &Runtime{engine: engine, outbound: set}, nil
}

// Select reports the selected tag for diagnostics. Data-plane callers should
// use DialTCP or ListenUDP so the outbound lease follows the returned socket.
func (r *Runtime) Select(meta route.Meta) string {
	return r.engine.Match(meta)
}

func (r *Runtime) DialTCP(ctx context.Context, meta route.Meta, destination M.Socksaddr) (net.Conn, error) {
	return r.outbound.DialTCP(ctx, r.Select(meta), destination)
}

func (r *Runtime) ListenUDP(ctx context.Context, meta route.Meta, destination M.Socksaddr) (net.PacketConn, error) {
	return r.outbound.ListenUDP(ctx, r.Select(meta), destination)
}

func (r *Runtime) Close() error {
	var err error
	r.once.Do(func() { err = r.outbound.Close() })
	return err
}
