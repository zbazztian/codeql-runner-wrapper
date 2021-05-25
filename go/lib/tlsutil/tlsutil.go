// Copyright (C) 2014 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"time"

	"github.com/pkg/errors"

	"github.com/syncthing/syncthing/lib/rand"
)

var (
	ErrIdentificationFailed = errors.New("failed to identify socket type")
)

var (
	// The list of cipher suites we will use / suggest for TLS 1.2 connections.
	cipherSuites = []uint16{
		// Suites that are good and fast on hardware *without* AES-NI.
		tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,

		// Suites that are good and fast on hardware with AES-NI. These are
		// reordered from the Go default to put the 256 bit ciphers above the
		// 128 bit ones - because that looks cooler, even though there is
		// probably no relevant difference in strength yet.
		tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,

		// The rest of the suites, minus DES stuff.
		tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256,
		tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
		tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
		tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_RSA_WITH_AES_128_CBC_SHA256,
		tls.TLS_RSA_WITH_AES_128_CBC_SHA,
		tls.TLS_RSA_WITH_AES_256_CBC_SHA,
	}
)

// SecureDefault returns a tls.Config with reasonable, secure defaults set.
// This variant allows only TLS 1.3.
func SecureDefaultTLS13() *tls.Config {
	return &tls.Config{
		// TLS 1.3 is the minimum we accept
		MinVersion: tls.VersionTLS13,
	}
}

// SecureDefaultWithTLS12 returns a tls.Config with reasonable, secure
// defaults set. This variant allows TLS 1.2.
func SecureDefaultWithTLS12() *tls.Config {
	// paranoia
	cs := make([]uint16, len(cipherSuites))
	copy(cs, cipherSuites)

	return &tls.Config{
		// TLS 1.2 is the minimum we accept
		MinVersion: tls.VersionTLS12,
		// The cipher suite lists built above. These are ignored in TLS 1.3.
		CipherSuites: cs,
		// We've put some thought into this choice and would like it to
		// matter.
		PreferServerCipherSuites: true,
	}
}

// NewCertificate generates and returns a new TLS certificate.
func NewCertificate(certFile, keyFile, commonName string, lifetimeDays int) (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, errors.Wrap(err, "generate key")
	}

	notBefore := time.Now().Truncate(24 * time.Hour)
	notAfter := notBefore.Add(time.Duration(lifetimeDays*24) * time.Hour)

	// NOTE: update lib/api.shouldRegenerateCertificate() appropriately if
	// you add or change attributes in here, especially DNSNames or
	// IPAddresses.
	template := x509.Certificate{
		SerialNumber: new(big.Int).SetUint64(rand.Uint64()),
		Subject: pkix.Name{
			CommonName:         commonName,
			Organization:       []string{"Syncthing"},
			OrganizationalUnit: []string{"Automatically Generated"},
		},
		DNSNames:              []string{commonName},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		SignatureAlgorithm:    x509.ECDSAWithSHA256,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, publicKey(priv), priv)
	if err != nil {
		return tls.Certificate{}, errors.Wrap(err, "create cert")
	}

	certOut, err := os.Create(certFile)
	if err != nil {
		return tls.Certificate{}, errors.Wrap(err, "save cert")
	}
	err = pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	if err != nil {
		return tls.Certificate{}, errors.Wrap(err, "save cert")
	}
	err = certOut.Close()
	if err != nil {
		return tls.Certificate{}, errors.Wrap(err, "save cert")
	}

	keyOut, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return tls.Certificate{}, errors.Wrap(err, "save key")
	}

	block, err := pemBlockForKey(priv)
	if err != nil {
		return tls.Certificate{}, errors.Wrap(err, "save key")
	}

	err = pem.Encode(keyOut, block)
	if err != nil {
		return tls.Certificate{}, errors.Wrap(err, "save key")
	}
	err = keyOut.Close()
	if err != nil {
		return tls.Certificate{}, errors.Wrap(err, "save key")
	}

	return tls.LoadX509KeyPair(certFile, keyFile)
}

type DowngradingListener struct {
	net.Listener
	TLSConfig *tls.Config
}

func (l *DowngradingListener) Accept() (net.Conn, error) {
	conn, isTLS, err := l.AcceptNoWrapTLS()

	// We failed to identify the socket type, pretend that everything is fine,
	// and pass it to the underlying handler, and let them deal with it.
	if err == ErrIdentificationFailed {
		return conn, nil
	}

	if err != nil {
		return conn, err
	}

	if isTLS {
		return tls.Server(conn, l.TLSConfig), nil
	}
	return conn, nil
}

func (l *DowngradingListener) AcceptNoWrapTLS() (net.Conn, bool, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, false, err
	}

	var first [1]byte
	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	n, err := conn.Read(first[:])
	conn.SetReadDeadline(time.Time{})
	if err != nil || n == 0 {
		// We hit a read error here, but the Accept() call succeeded so we must not return an error.
		// We return the connection as is with a special error which handles this
		// special case in Accept().
		return conn, false, ErrIdentificationFailed
	}

	return &UnionedConnection{&first, conn}, first[0] == 0x16, nil
}

type UnionedConnection struct {
	first *[1]byte
	net.Conn
}

func (c *UnionedConnection) Read(b []byte) (n int, err error) {
	if c.first != nil {
		if len(b) == 0 {
			// this probably doesn't happen, but handle it anyway
			return 0, nil
		}
		b[0] = c.first[0]
		c.first = nil
		return 1, nil
	}
	return c.Conn.Read(b)
}

func publicKey(priv interface{}) interface{} {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &k.PublicKey
	case *ecdsa.PrivateKey:
		return &k.PublicKey
	default:
		return nil
	}
}

func pemBlockForKey(priv interface{}) (*pem.Block, error) {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}, nil
	case *ecdsa.PrivateKey:
		b, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return nil, err
		}
		return &pem.Block{Type: "EC PRIVATE KEY", Bytes: b}, nil
	default:
		return nil, errors.New("unknown key type")
	}
}
