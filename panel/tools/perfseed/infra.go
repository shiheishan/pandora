package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// runInfra 经后台 API 建用户组、池、服务器与节点并签接入令牌；按 infra.json 续做。
func runInfra(ctx context.Context, o infraOpts) error {
	if err := os.MkdirAll(o.OutDir, 0o700); err != nil {
		return err
	}
	infraPath := filepath.Join(o.OutDir, fileInfra)
	tokensPath := filepath.Join(o.OutDir, fileTokens)
	nodesimPath := filepath.Join(o.OutDir, fileNodesimEnv)

	var state InfraState
	exists, err := loadJSON(infraPath, &state)
	if err != nil {
		return err
	}
	if state.TokensDone && fileExists(tokensPath) && fileExists(nodesimPath) {
		fmt.Printf("infra 已经做过（%s），跳过\n", infraPath)
		return nil
	}
	if exists && len(state.Servers) > o.Servers {
		return fmt.Errorf("infra.json 里已有 %d 台服务器，与本次 --servers=%d 不一致", len(state.Servers), o.Servers)
	}

	pass, err := readPasswordFile(o.AdminPasswordFile)
	if err != nil {
		return err
	}
	cli := newAdminClient(o.AdminURL, o.AdminEmail, pass)
	if err := cli.auth(ctx); err != nil {
		return err
	}

	if state.Cipher == "" {
		cipher, err := fetchShadowsocksCipher(ctx, cli)
		if err != nil {
			return err
		}
		state.Cipher = cipher
		if err := saveJSON(infraPath, state); err != nil {
			return err
		}
	}

	if state.GroupID == "" {
		id, err := createUserGroup(ctx, cli)
		if err != nil {
			return err
		}
		state.GroupID = id
		if err := saveJSON(infraPath, state); err != nil {
			return err
		}
	}

	if state.PoolID == "" {
		id, err := createNodePool(ctx, cli)
		if err != nil {
			return err
		}
		state.PoolID = id
		if err := saveJSON(infraPath, state); err != nil {
			return err
		}
	}

	for len(state.Servers) < o.Servers {
		i := len(state.Servers) + 1
		state.Servers = append(state.Servers, ServerState{Name: serverName(i)})
	}

	for i := 0; i < o.Servers; i++ {
		idx := i + 1
		sv := &state.Servers[i]
		if sv.ServerID == "" {
			id, err := createServer(ctx, cli, idx)
			if err != nil {
				return err
			}
			sv.ServerID = id
			if err := saveJSON(infraPath, state); err != nil {
				return err
			}
		}
		if sv.NodeID == "" {
			nid, rv, err := createNode(ctx, cli, idx, sv.ServerID, state.PoolID, state.Cipher)
			if err != nil {
				return err
			}
			sv.NodeID = nid
			sv.NodeRowVersion = rv
			if err := saveJSON(infraPath, state); err != nil {
				return err
			}
		}
		if idx%20 == 0 || idx == o.Servers {
			fmt.Printf("服务器与节点 %d/%d\n", idx, o.Servers)
		}
	}

	// 走到这里说明令牌文件不全（或从没签过）：整批重签，每次运行的幂等键不同
	if err := cli.auth(ctx); err != nil {
		return err
	}
	runUUID := uuid.NewString()
	var entries []TokenEntry
	var panel, ca string
	for i := 0; i < o.Servers; i++ {
		sv := state.Servers[i]
		idem := fmt.Sprintf("perfseed-boot-%s-%s", sv.ServerID, runUUID)
		var out struct {
			Token          string `json:"token"`
			InstallCommand string `json:"install_command"`
		}
		path := fmt.Sprintf("/v1/servers/%s/bootstrap-token", sv.ServerID)
		if err := cli.do(ctx, "POST", path, map[string]struct{}{}, idem, 201, &out); err != nil {
			return err
		}
		p, c, err := parseInstallCommand(out.InstallCommand)
		if err != nil {
			return fmt.Errorf("服务器 %s：%w", sv.Name, err)
		}
		if panel == "" {
			panel, ca = p, c
		} else if panel != p || ca != c {
			return fmt.Errorf("各台 panel/ca 不一致（%s 与其它台不同）", sv.Name)
		}
		entries = append(entries, TokenEntry{Name: sv.Name, Token: out.Token})
	}

	tokData, err := formatTokens(entries)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(tokensPath, tokData); err != nil {
		return err
	}
	if err := writeFileAtomic(nodesimPath, formatNodesimEnv(panel, ca)); err != nil {
		return err
	}
	adminEnvPath := filepath.Join(o.OutDir, fileAdminEnv)
	if err := writeFileAtomic(adminEnvPath, formatAdminEnv(o.AdminEmail)); err != nil {
		return err
	}
	state.TokensDone = true
	if err := saveJSON(infraPath, state); err != nil {
		return err
	}

	printInfraDone(o.OutDir, o.Servers, panel, ca)
	return nil
}

func printInfraDone(outDir string, servers int, panel, ca string) {
	tokensPath := filepath.Join(outDir, fileTokens)
	fmt.Printf("infra 完成：%d 台服务器与节点\n", servers)
	fmt.Printf("nodesim --panel '%s' --ca-sha256 '%s' --tokens-file %s …\n", panel, ca, tokensPath)
	fmt.Println("等全部 connected 后跑 perfseed activate")
}

func fetchShadowsocksCipher(ctx context.Context, cli *adminClient) (string, error) {
	var out struct {
		Schemas []struct {
			NodeType string   `json:"node_type"`
			Methods  []string `json:"methods"`
		} `json:"schemas"`
	}
	if err := cli.do(ctx, "GET", "/v1/node-protocol-schemas", nil, "", 200, &out); err != nil {
		return "", err
	}
	for _, s := range out.Schemas {
		if s.NodeType == "shadowsocks" {
			if len(s.Methods) == 0 {
				return "", fmt.Errorf("shadowsocks schema 没有 methods")
			}
			return s.Methods[0], nil
		}
	}
	return "", fmt.Errorf("未找到 node_type=shadowsocks 的协议 schema")
}

func createUserGroup(ctx context.Context, cli *adminClient) (string, error) {
	body := map[string]string{
		"code":        "perf",
		"name":        "Perf 压测",
		"description": "P8 性能测试造数",
	}
	var out struct {
		ID string `json:"id"`
	}
	err := cli.do(ctx, "POST", "/v1/user-groups", body, "", 200, &out)
	if err != nil {
		var ae *apiError
		if errorsAsAPI(err, &ae) && ae.Status == 409 {
			return "", fmt.Errorf("面板上已有 code=perf 的用户组，但 infra.json 里没有：上次中途失败且 infra.json 丢了，换空面板重来")
		}
		return "", err
	}
	return out.ID, nil
}

func createNodePool(ctx context.Context, cli *adminClient) (string, error) {
	body := map[string]string{
		"code": "perf",
		"name": "Perf 压测池",
	}
	var out struct {
		ID string `json:"id"`
	}
	err := cli.do(ctx, "POST", "/v1/node-pools", body, "", 200, &out)
	if err != nil {
		var ae *apiError
		if errorsAsAPI(err, &ae) && ae.Status == 409 {
			return "", fmt.Errorf("面板上已有 code=perf 的池，但 infra.json 里没有：上次中途失败且 infra.json 丢了，换空面板重来")
		}
		return "", err
	}
	return out.ID, nil
}

func createServer(ctx context.Context, cli *adminClient, i int) (string, error) {
	sn := serverName(i)
	body := map[string]string{
		"name":     "perf-" + sn,
		"hostname": sn + ".perf.example.test",
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := cli.do(ctx, "POST", "/v1/servers", body, "", 201, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func createNode(ctx context.Context, cli *adminClient, i int, serverID, poolID, cipher string) (string, int64, error) {
	sn := serverName(i)
	body := map[string]interface{}{
		"name":            fmt.Sprintf("perf-n%04d", i),
		"server_id":       serverID,
		"pool_id":         poolID,
		"node_type":       "shadowsocks",
		"server_host":     sn + ".perf.example.test",
		"server_port":     20000 + i,
		"traffic_rate":    1,
		"display_name":    fmt.Sprintf("Perf 线路 %04d", i),
		"protocol_config": map[string]string{"cipher": cipher},
	}
	var out struct {
		ID         string `json:"id"`
		RowVersion int64  `json:"row_version"`
	}
	idem := fmt.Sprintf("perfseed-node-%s", serverID)
	if err := cli.do(ctx, "POST", "/v1/nodes", body, idem, 201, &out); err != nil {
		return "", 0, err
	}
	return out.ID, out.RowVersion, nil
}
