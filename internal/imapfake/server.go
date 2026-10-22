// Package imapfake is an in-process IMAP server for tests.
// It speaks implicit TLS, STARTTLS, and cleartext, and it stores messages
// in memory. Passwords are accepted only for login checks and are not logged.
package imapfake

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"reflect"
	"sync"
	"time"
	"unsafe"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

const (
	ModeSSL      = "ssl"
	ModeStartTLS = "starttls"
	ModeNone     = "none"
)

// Options configures one fake server.
type Options struct {
	Mode     string
	Username string
	Password string
}

// Server is a started fake IMAP listener.
type Server struct {
	Addr        string
	Certificate []byte
	user        *imapmemserver.User
	server      *imapserver.Server
}

// Listen starts a server on 127.0.0.1 with an ephemeral port.
// Mode ssl is implicit TLS. The certificate includes 127.0.0.1.
func Listen(options Options) (*Server, error) {
	if options.Mode == "" {
		options.Mode = ModeSSL
	}
	switch options.Mode {
	case ModeSSL, ModeStartTLS, ModeNone:
	default:
		return nil, fmt.Errorf("invalid IMAP fake mode %q", options.Mode)
	}
	certificate, pemBytes, err := newCertificate()
	if err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	memServer := imapmemserver.New()
	user := imapmemserver.NewUser(options.Username, options.Password)
	if err = user.Create("INBOX", nil); err != nil {
		return nil, err
	}
	memServer.AddUser(user)
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memServer.NewSession(), nil, nil
		},
		TLSConfig:    tlsConfig,
		InsecureAuth: options.Mode == ModeNone,
		Caps: imap.CapSet{
			imap.CapIMAP4rev1:    {},
			imap.CapSpecialUse:   {},
			imap.CapListExtended: {},
		},
	})
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	listener := raw
	if options.Mode == ModeSSL {
		listener = tls.NewListener(raw, tlsConfig)
	}
	go func() {
		_ = server.Serve(listener)
	}()
	return &Server{Addr: raw.Addr().String(), Certificate: pemBytes, user: user, server: server}, nil
}

func (s *Server) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Close()
}

func (s *Server) Host() string {
	host, _, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return "127.0.0.1"
	}
	return host
}

func (s *Server) Port() int {
	_, portText, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return 0
	}
	var port int
	fmt.Sscanf(portText, "%d", &port)
	return port
}

// CreateMailbox adds a mailbox. Names such as "Sent Items" and "INBOX.Sent"
// are stored as given.
func (s *Server) CreateMailbox(name string) error {
	return s.user.Create(name, nil)
}

// Append stores one RFC 822 message and returns its UID.
func (s *Server) Append(mailbox string, raw []byte, when time.Time, flags []imap.Flag) (uint32, error) {
	data, err := s.user.Append(mailbox, bytes.NewReader(raw), &imap.AppendOptions{Time: when, Flags: flags})
	if err != nil {
		return 0, err
	}
	return uint32(data.UID), nil
}

// SetSpecialUse marks a mailbox with a SPECIAL-USE attribute such as \Sent.
// The in-memory backend keeps that attribute unexported, so tests set it
// directly. Call this before clients connect.
func (s *Server) SetSpecialUse(mailbox string, attrs ...imap.MailboxAttr) error {
	userValue := reflect.ValueOf(s.user).Elem()
	userMutex := (*sync.Mutex)(unsafe.Pointer(userValue.FieldByName("mutex").UnsafeAddr()))
	userMutex.Lock()
	defer userMutex.Unlock()

	mailboxesField := userValue.FieldByName("mailboxes")
	mailboxes := reflect.NewAt(mailboxesField.Type(), unsafe.Pointer(mailboxesField.UnsafeAddr())).Elem()
	mboxValue := mailboxes.MapIndex(reflect.ValueOf(mailbox))
	if !mboxValue.IsValid() || mboxValue.IsNil() {
		return fmt.Errorf("IMAP mailbox %q does not exist", mailbox)
	}
	mboxMutex := (*sync.Mutex)(unsafe.Pointer(mboxValue.Elem().FieldByName("mutex").UnsafeAddr()))
	mboxMutex.Lock()
	defer mboxMutex.Unlock()
	specialField := mboxValue.Elem().FieldByName("specialUse")
	special := reflect.NewAt(specialField.Type(), unsafe.Pointer(specialField.UnsafeAddr())).Elem()
	copied := append([]imap.MailboxAttr(nil), attrs...)
	special.Set(reflect.ValueOf(copied))
	return nil
}

func newCertificate() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certificate := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return certificate, pemBytes, nil
}
