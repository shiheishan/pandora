// pandora-h3-probe is a small, process-independent REALITY-over-HTTP/3
// interoperability client. It is intentionally separate from kernel tests so
// a future desktop/mobile client can reuse the same wire-level probe contract.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	realitytls "github.com/aegispanel/nodeagent/internal/reality"
	realityhttp3 "github.com/aegispanel/nodeagent/internal/realityquic/http3"
	"github.com/google/uuid"
)

const (
	vlessVersion byte = 0
	vlessTCP     byte = 1
	addrIPv4     byte = 1
	addrDomain   byte = 2
	addrIPv6     byte = 3
)

func main() {
	var (
		addr       string
		serverName string
		publicKey  string
		shortID    string
		path       string
		payload    string
		userUUID   string
		destHost   string
		destPort   int
		timeout    time.Duration
	)
	flag.StringVar(&addr, "addr", "127.0.0.1:443", "REALITY-over-H3 server address")
	flag.StringVar(&serverName, "server-name", "", "REALITY server name")
	flag.StringVar(&publicKey, "public-key", "", "REALITY public key, base64url without padding")
	flag.StringVar(&shortID, "short-id", "", "REALITY short ID, hex")
	flag.StringVar(&path, "path", "/xhttp/session-h3/1/", "XHTTP request path")
	flag.StringVar(&payload, "payload", "pandora-h3-probe", "payload to echo")
	flag.StringVar(&userUUID, "uuid", "", "optional VLESS UUID; omit for raw XHTTP")
	flag.StringVar(&destHost, "dest-host", "127.0.0.1", "VLESS destination host")
	flag.IntVar(&destPort, "dest-port", 443, "VLESS destination port")
	flag.DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	flag.Parse()

	key, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(publicKey))
	if err != nil || len(key) != 32 {
		fatal("public-key must be 32 bytes of base64url: %v", err)
	}
	sid, err := hex.DecodeString(strings.TrimSpace(shortID))
	if err != nil || len(sid) != 8 {
		fatal("short-id must be exactly 8 bytes of hex: %v", err)
	}

	transport := &realityhttp3.Transport{TLSClientConfig: &realitytls.Config{
		ServerName:         serverName,
		PublicKey:          key,
		ShortId:            sid,
		InsecureSkipVerify: true,
	}}
	defer transport.Close()

	body := []byte(payload)
	if strings.TrimSpace(userUUID) != "" {
		id, parseErr := uuid.Parse(strings.TrimSpace(userUUID))
		if parseErr != nil {
			fatal("uuid is invalid: %v", parseErr)
		}
		header, headerErr := vlessTCPHeader(id, destHost, destPort)
		if headerErr != nil {
			fatal("VLESS destination is invalid: %v", headerErr)
		}
		body = append(header, body...)
	}

	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+path, bytes.NewReader(body))
	if err != nil {
		fatal("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		fatal("HTTP/3 request: %v", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		fatal("read response: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fatal("HTTP/3 response status=%d body=%q", resp.StatusCode, responseBody)
	}
	fmt.Printf("proto=%s status=%d bytes=%d body=%q\n", resp.Proto, resp.StatusCode, len(responseBody), responseBody)
}

func vlessTCPHeader(id uuid.UUID, host string, port int) ([]byte, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("port %d out of range", port)
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	header := []byte{vlessVersion}
	header = append(header, id[:]...)
	header = append(header, 0, vlessTCP, byte(port>>8), byte(port))
	if ip4 := ip.To4(); ip4 != nil {
		header = append(header, addrIPv4)
		return append(header, ip4...), nil
	}
	if ip6 := ip.To16(); ip6 != nil {
		header = append(header, addrIPv6)
		return append(header, ip6...), nil
	}
	if host == "" || len(host) > 255 {
		return nil, fmt.Errorf("host must be an IPv4/IPv6 address or 1-255 byte domain")
	}
	header = append(header, addrDomain, byte(len(host)))
	return append(header, host...), nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
