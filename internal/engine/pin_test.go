package engine

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/node"
	"github.com/ldm0206/vless-to-http/internal/subscription"
)

// selfSignedCert builds a certificate for the loopback address that no root
// store vouches for, the shape of node allowInsecure used to serve.
func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "v2h test node"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// tlsFront terminates TLS in front of a plaintext server and returns the
// address clients dial, which is what a TLS node looks like from outside.
func tlsFront(t *testing.T, backendPort int, cert tls.Certificate) (int, func()) {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				backend, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", backendPort))
				if err != nil {
					return
				}
				defer backend.Close()
				go io.Copy(backend, conn)
				io.Copy(conn, backend)
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port, func() { listener.Close() }
}

// TestEnginePinsSelfSignedNodeCertificate is the user-visible fix for Xray's
// removal of allowInsecure: an insecure node is probed, its certificate is
// pinned, and traffic reaches the origin through it.
func TestEnginePinsSelfSignedNodeCertificate(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "origin-ok")
	}))
	defer origin.Close()

	backendPort, stopBackend := startUpstream(t)
	defer stopBackend()
	cert := selfSignedCert(t)
	frontPort, stopFront := tlsFront(t, backendPort, cert)
	defer stopFront()

	target := node.Node{
		Name: "自签证书节点", Type: "vless", Server: "127.0.0.1", Port: frontPort,
		UUID: testUUID, Network: "tcp", TLS: true, SNI: "127.0.0.1",
		SkipCertVerify: true,
	}
	target.Normalize()

	dir := t.TempDir()
	httpPort := freePort(t)
	cfg := config.Default()
	cfg.Panel.Listen = "127.0.0.1:0"
	cfg.Proxy.HTTP = config.Listener{Enabled: true, Listen: fmt.Sprintf("127.0.0.1:%d", httpPort)}
	cfg.Proxy.SOCKS = config.SocksListener{Enabled: false}
	cfg.Health.Enabled = false
	cfg.Health.SwitchCooldown = config.Duration(50 * time.Millisecond)
	cfg.Subscriptions = []config.Subscription{{ID: "sub_1", Name: "订阅", Kind: "auto", Enabled: true}}
	cfg.Users = []config.User{{
		ID: "usr_1", Name: "alice", Password: "pw", Enabled: true,
		Mode: config.ModeFixed, Fallback: config.FallbackInherit,
		Targets: []config.Target{{Sub: "sub_1", Node: target.Name}},
	}}

	store := writeConfig(t, dir, cfg)
	cache := subscription.NewCache(dir)
	if err := cache.Set("sub_1", []node.Node{target}, "clash"); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	eng := New(store, cache, newTestLogger(t), dir)
	if err := eng.Apply("测试"); err != nil {
		// The core refusing the node is exactly the failure being fixed.
		t.Fatalf("apply: %v", err)
	}
	defer eng.Close()

	sum := sha256.Sum256(cert.Certificate[0])
	want := hex.EncodeToString(sum[:])
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(eng.PlanJSON(), `"pinnedPeerCertSha256": "`+want+`"`) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if plan := eng.PlanJSON(); !strings.Contains(plan, `"pinnedPeerCertSha256": "`+want+`"`) {
		t.Fatalf("the certificate was never pinned:\n%s", plan)
	} else if strings.Contains(plan, "allowInsecure") {
		t.Fatalf("the removed allowInsecure field is still emitted:\n%s", plan)
	}

	// The tunnel works through the pinned certificate.
	client := proxyClient(httpPort, "alice", "pw")
	if code, body := fetch(t, client, origin.URL); code != 200 || body != "origin-ok" {
		t.Fatalf("tunnelled request through a pinned node failed: %d %q", code, body)
	}
}
