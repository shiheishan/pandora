// Command loadtest 给面板做资源占用实测：造数、模拟节点、模拟用户流量、触发全量重拉。
//
// 在 Go module 根目录（panel/）下运行：go run ./tools/loadtest help
package main

import (
	"fmt"
	"os"

	"github.com/aegispanel/aegis/tools/loadtest/nodesim"
	"github.com/aegispanel/aegis/tools/loadtest/seed"
	"github.com/aegispanel/aegis/tools/loadtest/userload"
)

const usage = `usage: go run ./tools/loadtest <command> [flags]

seed    create N fictitious users, plans, subscriptions and nodes with identities; writes a manifest
nodes   run simulated pdnd nodes against the node gateway (signed channel + UniProxy + stream)
users   run mixed user traffic: subscription pulls, portal reads, admin list reads
burst   change one user through the admin API so every node re-pulls its user list

Run "go run ./tools/loadtest <command> -h" for the flags of each command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "seed":
		err = seed.Main(os.Args[2:])
	case "nodes":
		err = nodesim.Main(os.Args[2:])
	case "users":
		err = userload.Main(os.Args[2:])
	case "burst":
		err = userload.BurstMain(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(1)
	}
}
