package reality

import (
	"context"
	"fmt"
)

// realityQUICServerHandshake is the QUIC counterpart of ServerHandoff. It
// keeps the REALITY authentication and target-flight synthesis in this
// package, while QUIC packet encryption remains owned by the QUIC transport.
// The target connection is used only to obtain the browser-compatible server
// TLS flight; application traffic never traverses it.
func (c *Conn) realityQUICServerHandshake(ctx context.Context) (err error) {
	clientHello, ech, err := c.readClientHello(ctx)
	if err != nil {
		return err
	}
	if ech != nil || c.vers != VersionTLS13 || clientHello == nil || !c.config.ServerNames[clientHello.serverName] {
		return fmt.Errorf("REALITY: QUIC client hello rejected")
	}

	hs := serverHandshakeStateTLS13{
		c:           c,
		ctx:         ctx,
		clientHello: clientHello,
	}
	if err := authenticateHandoff(&hs, c.config); err != nil {
		return err
	}
	// ServerHandoff intentionally skips the ordinary processClientHello path.
	// Select ALPN here so the synthesized EncryptedExtensions still satisfies
	// RFC 9001 (HTTP/3 clients require an explicit h3 selection).
	for _, configured := range c.config.NextProtos {
		for _, offered := range clientHello.alpnProtocols {
			if configured == offered {
				c.clientProtocol = configured
				break
			}
		}
		if c.clientProtocol != "" {
			break
		}
	}
	if c.clientProtocol == "" {
		return fmt.Errorf("REALITY: QUIC client did not offer a configured ALPN")
	}
	if clientHello.quicTransportParameters == nil {
		return fmt.Errorf("REALITY: QUIC client transport parameters are missing")
	}
	// The normal TLS server path records the peer transport parameters while
	// processing ClientHello. Handoff intentionally bypasses that path, so
	// publish the same event explicitly before HTTP/3 can open streams.
	c.quicSetTransportParameters(clientHello.quicTransportParameters)
	if c.config.DialContext == nil {
		return fmt.Errorf("REALITY: QUIC target dialer is not configured")
	}
	target, err := c.config.DialContext(ctx, c.config.Type, c.config.Dest)
	if err != nil {
		return fmt.Errorf("REALITY: QUIC failed to dial dest: %w", err)
	}
	defer target.Close()

	// QUIC carries a bare TLS handshake message. A TCP TLS target expects the
	// same message inside a TLS record, but the QUIC transport-parameters
	// extension is not valid on a TCP TLS endpoint. Re-marshal a target-only
	// copy without that extension; the original QUIC hello remains the
	// transcript used by REALITY authentication and QUIC key schedule.
	targetHello := clientHello.clone()
	targetHello.quicTransportParameters = nil
	targetHello.original = nil
	targetHelloBytes, err := targetHello.marshal()
	if err != nil {
		return fmt.Errorf("REALITY: marshal target client hello: %w", err)
	}
	if _, err := target.Write(tlsRecord(targetHelloBytes)); err != nil {
		return fmt.Errorf("REALITY: QUIC target client hello: %w", err)
	}
	if err := readTargetFlight(&hs, target, c.config); err != nil {
		return err
	}
	if err := hs.readClientFinished(); err != nil {
		return fmt.Errorf("REALITY: QUIC client Finished rejected: %w", err)
	}
	c.isHandshakeComplete.Store(true)
	return nil
}

func tlsRecord(handshake []byte) []byte {
	if len(handshake) > 0xffff {
		return nil
	}
	record := make([]byte, 5+len(handshake))
	record[0] = byte(recordTypeHandshake)
	record[1] = 3
	record[2] = 3
	record[3] = byte(len(handshake) >> 8)
	record[4] = byte(len(handshake))
	copy(record[5:], handshake)
	return record
}

// quicServerHandshake selects the REALITY path only when the configuration
// carries the complete native REALITY server tuple. Ordinary QUIC TLS keeps
// the upstream server handshake and remains compatible with HTTP/3 clients.
func (c *Conn) quicServerHandshake(ctx context.Context) error {
	if c.config != nil && len(c.config.PrivateKey) == 32 && len(c.config.ServerNames) > 0 && c.config.Dest != "" {
		return c.realityQUICServerHandshake(ctx)
	}
	return c.serverHandshake(ctx)
}

// quicRealityConfigValid is used by callers constructing a QUIC server to
// reject an accidental stream-style REALITY configuration early.
func quicRealityConfigValid(config *Config) error {
	if config == nil {
		return fmt.Errorf("REALITY: QUIC config is nil")
	}
	if len(config.PrivateKey) != 32 || len(config.ServerNames) == 0 || config.Dest == "" {
		return fmt.Errorf("REALITY: QUIC config requires private key, server names and dest")
	}
	if config.MaxTimeDiff < 0 {
		return fmt.Errorf("REALITY: QUIC max time diff cannot be negative")
	}
	return nil
}
