package admin

import (
	"reflect"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/config"
)

// 保留端口表的缺省值写在两处：platform/config（读环境变量时的缺省）与 nodefabric（没注入时的
// 缺省）。两处必须相同，否则「没经 config.Load 装配」的进程与生产的保留表不一样。
func TestNodePortPolicyDefaultsMatchConfig(t *testing.T) {
	reserved, panel := config.DefaultNodePortRanges()
	cfg := &config.Config{NodePorts: config.NodePorts{Reserved: reserved, PanelReserved: panel}}
	svc := nodefabric.NewService(nil, nil)
	configureNodePortPolicy(Deps{Cfg: cfg, Node: svc})
	want := nodefabric.DefaultPortPolicy()
	got := nodefabric.PortPolicyOf(svc)
	if !reflect.DeepEqual(got.Reserved, want.Reserved) || !reflect.DeepEqual(got.PanelReserved, want.PanelReserved) {
		t.Fatalf("config defaults %+v / %+v differ from nodefabric defaults %+v / %+v",
			got.Reserved, got.PanelReserved, want.Reserved, want.PanelReserved)
	}
}
