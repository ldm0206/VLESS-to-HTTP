package certpin

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net"
	"testing"
	"time"
)

// tlsServer serves one certificate on a loopback port and returns its address.
func tlsServer(t *testing.T, cert tls.Certificate) (string, int) {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				// The handshake is all the probe needs; errors after it are the
				// probe closing the connection.
				conn.(*tls.Conn).Handshake()
			}()
		}
	}()

	addr := listener.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func template(names ...string) *x509.Certificate {
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, name := range names {
		if ip := net.ParseIP(name); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			continue
		}
		tmpl.DNSNames = append(tmpl.DNSNames, name)
	}
	tmpl.Subject = pkix.Name{CommonName: names[0]}
	return tmpl
}

// selfSigned builds a certificate that no root store can vouch for.
func selfSigned(t *testing.T, names ...string) tls.Certificate {
	t.Helper()
	key := newKey(t)
	tmpl := template(names...)
	tmpl.IsCA = true
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// signedBy builds a certificate issued by a local CA, so a probe that is told
// to trust that CA verifies normally.
func signedBy(t *testing.T, names ...string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	caKey := newKey(t)
	caTmpl := template("v2h test ca")
	caTmpl.IsCA = true
	caDer, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create ca: %v", err)
	}
	ca, err := x509.ParseCertificate(caDer)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}

	key := newKey(t)
	der, err := x509.CreateCertificate(rand.Reader, template(names...), ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

func leafHash(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	sum := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(sum[:])
}

// TestSelfSignedCertificateIsPinned is the case allowInsecure used to cover: a
// server whose certificate no root store can vouch for still has to be usable.
func TestSelfSignedCertificateIsPinned(t *testing.T) {
	cert := selfSigned(t, "127.0.0.1")
	host, port := tlsServer(t, cert)

	res, err := Inspect(context.Background(), Target{Host: host, Port: port, SNI: "127.0.0.1"}, Options{})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if res.Verified {
		t.Fatal("a self-signed certificate must not be reported as verified")
	}
	if res.Sha256 != leafHash(t, cert) {
		t.Fatalf("pin = %s, want %s", res.Sha256, leafHash(t, cert))
	}
}

// TestTrustedCertificateNeedsNoPin keeps a working node working: pinning a
// certificate that verifies normally would break it at the next renewal.
func TestTrustedCertificateNeedsNoPin(t *testing.T) {
	cert, roots := signedBy(t, "127.0.0.1")
	host, port := tlsServer(t, cert)

	res, err := Inspect(context.Background(), Target{Host: host, Port: port, SNI: "127.0.0.1"}, Options{Roots: roots})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !res.Verified || res.Sha256 != "" {
		t.Fatalf("a trusted certificate should need no pin, got %+v", res)
	}
}

// TestWrongNameIsPinned covers a CDN fronting the node with a certificate for
// another host, which is the other half of what allowInsecure hid.
func TestWrongNameIsPinned(t *testing.T) {
	cert, roots := signedBy(t, "example.com")
	host, port := tlsServer(t, cert)

	res, err := Inspect(context.Background(), Target{Host: host, Port: port, SNI: "localhost"}, Options{Roots: roots})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if res.Verified || res.Sha256 != leafHash(t, cert) {
		t.Fatalf("a mismatched certificate should be pinned, got %+v", res)
	}
}

// TestUnreachableNodeIsNotPinned guards against pinning a certificate that was
// never seen: a node that is merely down has to keep failing.
func TestUnreachableNodeIsNotPinned(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().(*net.TCPAddr)
	listener.Close()

	res, err := Inspect(context.Background(), Target{Host: addr.IP.String(), Port: addr.Port}, Options{Timeout: 5 * time.Second})
	if err == nil {
		t.Fatalf("an unreachable node must fail the inspection, got %+v", res)
	}
	if res.Sha256 != "" || res.Verified {
		t.Fatalf("nothing should be pinned without a certificate, got %+v", res)
	}
}
