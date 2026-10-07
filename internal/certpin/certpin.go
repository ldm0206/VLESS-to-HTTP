// Package certpin decides how a node's TLS certificate should be trusted when
// its subscription asks for certificate verification to be skipped.
//
// Xray removed "allowInsecure" in favour of "pinnedPeerCertSha256", so a node
// that cannot be verified against the system roots is pinned instead: the
// SHA-256 of the certificate it presented is remembered and handed to the core.
package certpin

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"time"
)

// Target is the TLS endpoint of one node.
type Target struct {
	Host string
	Port int
	SNI  string
}

// Options tunes an inspection.
type Options struct {
	// Roots replaces the system roots, which lets tests trust a local CA.
	Roots *x509.CertPool
	// Timeout bounds both handshakes. Defaults to 8 seconds.
	Timeout time.Duration
}

// Result is what one inspection learned about the peer certificate.
type Result struct {
	// Verified is true when the certificate passed normal verification, in
	// which case nothing has to be pinned.
	Verified bool
	// Sha256 is the lower-case hex SHA-256 of the leaf certificate's DER, the
	// form Xray's "pinnedPeerCertSha256" expects. Empty when Verified.
	Sha256 string
}

// Inspect connects to the target and reports how its certificate should be
// trusted. A certificate that is genuinely valid is left alone: pinning it
// would only break the node the first time it is renewed.
func Inspect(ctx context.Context, target Target, opts Options) (Result, error) {
	if _, err := handshake(ctx, target, opts, false); err == nil {
		return Result{Verified: true}, nil
	} else if !untrusted(err) {
		return Result{}, err
	}

	// Skipping the check is what the subscription asked for, so pin the
	// certificate that was served rather than failing the node.
	state, err := handshake(ctx, target, opts, true)
	if err != nil {
		return Result{}, err
	}
	if len(state.PeerCertificates) == 0 {
		return Result{}, errors.New("对端没有提供证书")
	}
	sum := sha256.Sum256(state.PeerCertificates[0].Raw)
	return Result{Sha256: hex.EncodeToString(sum[:])}, nil
}

// handshake performs one full TLS handshake against the target.
func handshake(ctx context.Context, target Target, opts Options, insecure bool) (tls.ConnectionState, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	addr := net.JoinHostPort(target.Host, strconv.Itoa(target.Port))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return tls.ConnectionState{}, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}

	// An empty SNI is left empty on purpose: the core lets the dialer derive it
	// from the address, and the probe has to see the same certificate it will.
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         target.SNI,
		RootCAs:            opts.Roots,
		InsecureSkipVerify: insecure,
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return tls.ConnectionState{}, err
	}
	return tlsConn.ConnectionState(), nil
}

// untrusted reports whether err is the peer certificate being rejected, as
// opposed to the connection itself failing.
func untrusted(err error) bool {
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		return true
	}
	// Older Go releases surface the x509 errors without that wrapper.
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid)
}
