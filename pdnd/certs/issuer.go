package certs

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-acme/lego/v5/acme"
	"github.com/go-acme/lego/v5/acme/api"
	"github.com/go-acme/lego/v5/certcrypto"
	"github.com/go-acme/lego/v5/certificate"
	"github.com/go-acme/lego/v5/challenge/http01"
	"github.com/go-acme/lego/v5/lego"
	"github.com/go-acme/lego/v5/registration"
)

// acmeUser 实现 registration.User：不填邮箱（LE 已不发到期邮件，提醒由面板做）。
type acmeUser struct {
	key crypto.Signer
	reg *acme.ExtendedAccount
}

func (u *acmeUser) GetEmail() string                       { return "" }
func (u *acmeUser) GetRegistration() *acme.ExtendedAccount { return u.reg }
func (u *acmeUser) GetPrivateKey() crypto.Signer           { return u.key }

type issuer struct {
	client   *lego.Client
	httpPort int
}

func newIssuer(ctx context.Context, st *store, opts Options, force bool) (*issuer, error) {
	dirURL := opts.DirectoryURL
	if dirURL == "" {
		dirURL = lego.DirectoryURLLetsEncrypt
	}
	httpPort := opts.HTTPPort
	if httpPort == 0 {
		httpPort = 80
	}

	var user *acmeUser

	if !force {
		acc, err := st.loadAccount()
		if err != nil {
			return nil, &issueError{code: ReasonStoreFailed, err: err}
		}
		if acc != nil && acc.DirectoryURL == dirURL && acc.KeyPEM != "" && acc.URI != "" {
			key, err := parseAccountPrivateKey(acc.KeyPEM)
			if err != nil {
				return nil, &issueError{code: ReasonStoreFailed, err: err}
			}
			user = &acmeUser{
				key: key,
				reg: &acme.ExtendedAccount{Location: acc.URI},
			}
		}
	}

	if user == nil {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, &issueError{code: ReasonAccountFailed, err: err}
		}
		user = &acmeUser{key: key}

		client, err := buildLegoClient(dirURL, user, opts)
		if err != nil {
			return nil, wrapAccountErr(err)
		}
		if err := client.Challenge.SetHTTP01Provider(http01.NewProviderServer("", strconv.Itoa(httpPort))); err != nil {
			return nil, wrapAccountErr(err)
		}

		reg, err := client.Registration.Register(ctx, registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return nil, wrapAccountErr(err)
		}
		user.reg = reg

		keyPEM, err := marshalECPrivateKeyPEM(key)
		if err != nil {
			return nil, &issueError{code: ReasonStoreFailed, err: err}
		}
		if err := st.saveAccount(&accountFile{
			DirectoryURL: dirURL,
			URI:          reg.Location,
			KeyPEM:       keyPEM,
		}); err != nil {
			return nil, &issueError{code: ReasonStoreFailed, err: err}
		}

		return &issuer{client: client, httpPort: httpPort}, nil
	}

	client, err := buildLegoClient(dirURL, user, opts)
	if err != nil {
		return nil, wrapAccountErr(err)
	}
	if err := client.Challenge.SetHTTP01Provider(http01.NewProviderServer("", strconv.Itoa(httpPort))); err != nil {
		return nil, wrapAccountErr(err)
	}
	return &issuer{client: client, httpPort: httpPort}, nil
}

func buildLegoClient(dirURL string, user *acmeUser, opts Options) (*lego.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{
		RootCAs:    opts.RootCAs,
		MinVersion: tls.VersionTLS12,
	}
	httpClient := &http.Client{
		Timeout:   30 * time.Second,
		Transport: tr,
	}
	// 从 NewConfig 起步：它带着证书下载的超时与请求限速缺省值，结构体字面量会把它们留成 0
	cfg := lego.NewConfig(user)
	cfg.CADirURL = dirURL
	cfg.UserAgent = "pandora-native"
	cfg.HTTPClient = httpClient
	return lego.NewClient(cfg)
}

func wrapAccountErr(err error) error {
	if err == nil {
		return nil
	}
	code, _, _ := classify(err)
	if code == ReasonACMEUnreachable {
		return err
	}
	return &issueError{code: ReasonAccountFailed, err: err}
}

func parseAccountPrivateKey(pemStr string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("账户私钥 PEM 无效")
	}
	if block.Type != "EC PRIVATE KEY" {
		return nil, fmt.Errorf("账户私钥类型应为 EC PRIVATE KEY，实际为 %q", block.Type)
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	return key, nil
}

func marshalECPrivateKeyPEM(key *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), nil
}

func checkChallengePort(port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil {
		return &issueError{code: ReasonChallengePortBusy, err: err}
	}
	_ = ln.Close()
	return nil
}

func (is *issuer) obtain(ctx context.Context, id string, previous *x509.Certificate) (certPEM, keyPEM []byte, err error) {
	if err := checkChallengePort(is.httpPort); err != nil {
		return nil, nil, err
	}

	req := certificate.ObtainRequest{
		Domains: []string{id},
		KeyType: certcrypto.EC256,
		Bundle:  true,
		Profile: profileFor(id),
	}
	if previous != nil {
		replaces, err := api.MakeARICertID(previous)
		if err != nil {
			return nil, nil, err
		}
		req.ReplacesCertID = replaces
	}

	res, err := is.client.Certificate.Obtain(ctx, req)
	if err != nil {
		var replaced *acme.AlreadyReplacedError
		if errors.As(err, &replaced) && req.ReplacesCertID != "" {
			req.ReplacesCertID = ""
			res, err = is.client.Certificate.Obtain(ctx, req)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	return res.Certificate, res.PrivateKey, nil
}

func (is *issuer) renewalInfo(ctx context.Context, leaf *x509.Certificate, now time.Time) (renewAt time.Time, pollAfter time.Duration, err error) {
	info, err := is.client.Certificate.GetRenewalInfo(ctx, leaf)
	if err != nil {
		return time.Time{}, 0, err
	}

	if t := info.ShouldRenewAt(now, ariPollInterval); t != nil {
		renewAt = *t
	} else {
		renewAt = info.SuggestedWindow.Start
	}

	if info.RetryAfter > 0 {
		pollAfter = info.RetryAfter
	} else {
		pollAfter = ariPollInterval
	}
	return renewAt, pollAfter, nil
}
