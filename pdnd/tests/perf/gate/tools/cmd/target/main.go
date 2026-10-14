package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"flag"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

const tlsDestDNSName = "www.example.test"

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags)

	var tcpEcho, udpEcho, sinkAddr, tlsDest string
	flag.StringVar(&tcpEcho, "tcp-echo", "", "TCP 回显监听地址 host:port")
	flag.StringVar(&udpEcho, "udp-echo", "", "UDP 回显监听地址 host:port")
	flag.StringVar(&sinkAddr, "sink", "", "吞吐 sink 监听地址 host:port")
	flag.StringVar(&tlsDest, "tls-dest", "", "REALITY 诱饵 TLS 监听地址 host:port")
	flag.Parse()

	if tcpEcho == "" && udpEcho == "" && sinkAddr == "" && tlsDest == "" {
		log.Print("至少指定一个监听地址")
		os.Exit(2)
	}

	var closers []func() error

	if tcpEcho != "" {
		ln, err := net.Listen("tcp", tcpEcho)
		if err != nil {
			log.Fatalf("tcp-echo 监听失败: %v", err)
		}
		log.Printf("tcp-echo 监听 %s", ln.Addr())
		go acceptLoop(ln, serveTCPEcho)
		closers = append(closers, ln.Close)
	}

	if udpEcho != "" {
		addr, err := net.ResolveUDPAddr("udp", udpEcho)
		if err != nil {
			log.Fatalf("udp-echo 地址无效: %v", err)
		}
		pc, err := net.ListenUDP("udp", addr)
		if err != nil {
			log.Fatalf("udp-echo 监听失败: %v", err)
		}
		if err := pc.SetReadBuffer(8 << 20); err != nil {
			log.Printf("udp-echo SetReadBuffer: %v", err)
		}
		if err := pc.SetWriteBuffer(8 << 20); err != nil {
			log.Printf("udp-echo SetWriteBuffer: %v", err)
		}
		log.Printf("udp-echo 监听 %s", pc.LocalAddr())
		go serveUDPEcho(pc)
		closers = append(closers, pc.Close)
	}

	if sinkAddr != "" {
		ln, err := net.Listen("tcp", sinkAddr)
		if err != nil {
			log.Fatalf("sink 监听失败: %v", err)
		}
		log.Printf("sink 监听 %s", ln.Addr())
		go acceptLoop(ln, serveSink)
		closers = append(closers, ln.Close)
	}

	if tlsDest != "" {
		cert, err := generateTLSDestCert()
		if err != nil {
			log.Fatalf("tls-dest 证书生成失败: %v", err)
		}
		cfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
			NextProtos:   []string{"h2", "http/1.1"},
		}
		ln, err := tls.Listen("tcp", tlsDest, cfg)
		if err != nil {
			log.Fatalf("tls-dest 监听失败: %v", err)
		}
		log.Printf("tls-dest 监听 %s", ln.Addr())
		go acceptLoop(ln, serveTLSDest)
		closers = append(closers, ln.Close)
	}

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch

	for _, closeFn := range closers {
		_ = closeFn()
	}
	os.Exit(0)
}

func acceptLoop(ln net.Listener, serve func(net.Conn)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			serve(c)
		}(conn)
	}
}

func serveTCPEcho(conn net.Conn) {
	_, _ = io.Copy(conn, conn)
	conn.Close()
}

func serveUDPEcho(pc *net.UDPConn) {
	workers := runtime.GOMAXPROCS(0)
	for w := 0; w < workers; w++ {
		go func() {
			// 每个 worker 自己的缓冲：共用一块会让并发读写互相覆盖
			buf := make([]byte, 64<<10)
			for {
				n, addr, err := pc.ReadFromUDPAddrPort(buf)
				if err != nil {
					return
				}
				_, _ = pc.WriteToUDPAddrPort(buf[:n], addr)
			}
		}()
	}
	select {}
}

func serveTLSDest(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	if tc, ok := conn.(*tls.Conn); ok {
		if err := tc.Handshake(); err != nil {
			conn.Close()
			return
		}
	}
	_, _ = io.Copy(io.Discard, conn)
	conn.Close()
}

// generateTLSDestCert 生成 REALITY 诱饵站用的自签 ECDSA P-256 证书。
func generateTLSDestCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: tlsDestDNSName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(30 * 24 * time.Hour),
		DNSNames:     []string{tlsDestDNSName},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

func randomSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		panic(err)
	}
	return n.Add(n, big.NewInt(1))
}
