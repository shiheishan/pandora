package kernel

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// TCP 流量按周期计入：一条持续 3 个上报周期的连接，每个周期的 GetTraffic 都拿到
// 这个周期里的增量，而不是等连接结束一次性入账（原先长连接跨多少周期都是 0，
// 进程被强杀就全丢）。
// trafficPeriodWait 是每个周期等计数到齐的上限。检查机上 -race 与其他 job 并发，
// 1 秒偶发不够（10-09 检查机红过一次、GitHub 同提交绿）；计数只会涨到正好等于、
// 不会超，放宽上限不放过错误。
const trafficPeriodWait = 5 * time.Second

func TestLongConnectionTrafficIsReportedEachPeriod(t *testing.T) {
	for _, p := range lifecycleProtos() {
		t.Run(p.name, func(t *testing.T) {
			echo := startLifecycleEcho(t, false)
			user := p.user(0)
			c, port, tag := startLifecycleCore(t, p, []core.User{user})
			conn, err := p.dial(port, user, echo.addr())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// 握手本身的字节先清掉，后面每个周期只看这一周期发的那段。
			if err := echoOnce(conn, "warmup"); err != nil {
				t.Fatal(err)
			}
			var warm int64
			if ok, why := waitFor(trafficPeriodWait, func() (bool, string) {
				traffic, _ := c.GetTraffic(tag)
				for _, x := range traffic {
					warm += x.Download
				}
				return warm == int64(len("warmup")), fmt.Sprintf("预热下行 %d", warm)
			}); !ok {
				t.Fatal(why)
			}
			for period := 1; period <= 3; period++ {
				payload := strings.Repeat(fmt.Sprint(period), 1000*period)
				if err := echoOnce(conn, payload); err != nil {
					t.Fatal(err)
				}
				// 下行计数在写给客户端之后才累加，客户端读到回显时计数可能还差最后
				// 一笔：在本周期内短暂轮询累加，总数必须正好等于这一周期的字节数。
				var got core.UserTraffic
				ok, why := waitFor(trafficPeriodWait, func() (bool, string) {
					traffic, err := c.GetTraffic(tag)
					if err != nil {
						return false, err.Error()
					}
					for _, x := range traffic {
						if x.ID == user.ID {
							got.Upload += x.Upload
							got.Download += x.Download
						}
					}
					return got.Upload == int64(len(payload)) && got.Download == int64(len(payload)),
						fmt.Sprintf("上行 %d 下行 %d", got.Upload, got.Download)
				})
				// 计的是协议解出来的应用层字节，与 payload 等长。
				if !ok {
					t.Fatalf("第 %d 个周期（连接仍开着）：%s，期望各 %d", period, why, len(payload))
				}
			}
		})
	}
}
