package http3

import (
	stdtls "crypto/tls"
	"crypto/x509"

	realitytls "github.com/aegispanel/nodeagent/internal/reality"
)

// standardTLSState converts only the public connection-state fields required
// by net/http. The QUIC handshake itself remains owned by Pandora REALITY;
// this bridge is for HTTP tracing and Response.TLS/Request.TLS compatibility.
func standardTLSState(state realitytls.ConnectionState) stdtls.ConnectionState {
	return stdtls.ConnectionState{
		Version:                     state.Version,
		HandshakeComplete:           state.HandshakeComplete,
		DidResume:                   state.DidResume,
		CipherSuite:                 state.CipherSuite,
		CurveID:                     stdtls.CurveID(state.CurveID),
		NegotiatedProtocol:          state.NegotiatedProtocol,
		NegotiatedProtocolIsMutual:  state.NegotiatedProtocolIsMutual,
		ServerName:                  state.ServerName,
		PeerCertificates:            cloneCertificates(state.PeerCertificates),
		VerifiedChains:              cloneVerifiedChains(state.VerifiedChains),
		SignedCertificateTimestamps: cloneBytes2(state.SignedCertificateTimestamps),
		OCSPResponse:                append([]byte(nil), state.OCSPResponse...),
		TLSUnique:                   append([]byte(nil), state.TLSUnique...),
		ECHAccepted:                 state.ECHAccepted,
	}
}

func cloneCertificates(src []*x509.Certificate) []*x509.Certificate {
	return append([]*x509.Certificate(nil), src...)
}

func cloneVerifiedChains(src [][]*x509.Certificate) [][]*x509.Certificate {
	if src == nil {
		return nil
	}
	dst := make([][]*x509.Certificate, len(src))
	for i := range src {
		dst[i] = cloneCertificates(src[i])
	}
	return dst
}

func cloneBytes2(src [][]byte) [][]byte {
	if src == nil {
		return nil
	}
	dst := make([][]byte, len(src))
	for i := range src {
		dst[i] = append([]byte(nil), src[i]...)
	}
	return dst
}
