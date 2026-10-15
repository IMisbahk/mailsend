// Package smtpfake is an in-process SMTP server for tests.
// It speaks plain SMTP, STARTTLS, and implicit TLS, and it can reject
// authentication or a message with a chosen reply code.
package smtpfake

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ModeStartTLS = "starttls"
	ModeSSL      = "ssl"
	ModeNone     = "none"
)

// Options configures one fake server. Username and password are the only
// credentials AUTH PLAIN and AUTH LOGIN will accept.
type Options struct {
	Mode          string
	Username      string
	Password      string
	FailStage     string
	FailCode      int
	FailMessage   string
	DropAfterData bool
	FailRcpts     int
	LoginOnly     bool
}

// Submission is one accepted or rejected DATA payload.
type Submission struct {
	MailFrom   string
	Recipients []string
	Data       []byte
}

// Server is a started fake SMTP listener.
type Server struct {
	Addr        string
	Certificate []byte
	Options     Options

	Connections atomic.Int32
	TLSStarted  atomic.Bool

	listener    net.Listener
	tlsConfig   *tls.Config
	mu          sync.Mutex
	submissions []Submission
	notify      chan struct{}
	rcptSeen    atomic.Int32
}

// Listen starts a server on 127.0.0.1 with an ephemeral port.
func Listen(options Options) (*Server, error) {
	if options.Mode == "" {
		options.Mode = ModeStartTLS
	}
	certificate, pemBytes, err := newCertificate()
	if err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	listener := raw
	if options.Mode == ModeSSL {
		listener = tls.NewListener(raw, tlsConfig)
	}
	server := &Server{
		Addr:        raw.Addr().String(),
		Certificate: pemBytes,
		Options:     options,
		listener:    listener,
		tlsConfig:   tlsConfig,
		notify:      make(chan struct{}, 16),
	}
	go server.serve()
	return server, nil
}

func (s *Server) Close() error {
	if s == nil || s.listener == nil {
		return nil
	}
	return s.listener.Close()
}

func (s *Server) Host() string {
	host, _, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return s.Addr
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

// WaitSubmission returns the next DATA payload or times out.
func (s *Server) WaitSubmission(timeout time.Duration) (Submission, error) {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		if len(s.submissions) > 0 {
			item := s.submissions[0]
			s.submissions = s.submissions[1:]
			s.mu.Unlock()
			return item, nil
		}
		s.mu.Unlock()
		remain := time.Until(deadline)
		if remain <= 0 {
			return Submission{}, fmt.Errorf("timed out waiting for an SMTP submission")
		}
		select {
		case <-s.notify:
		case <-time.After(remain):
		}
	}
}

func (s *Server) record(item Submission) {
	s.mu.Lock()
	s.submissions = append(s.submissions, item)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *Server) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.Connections.Add(1)
		if s.Options.Mode == ModeSSL {
			s.TLSStarted.Store(true)
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	writef := func(format string, args ...any) {
		fmt.Fprintf(writer, format+"\r\n", args...)
		_ = writer.Flush()
	}
	readLine := func() (string, error) {
		line, err := reader.ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
	}
	writef("220 127.0.0.1 ESMTP mailsend-test")
	tlsReady := s.Options.Mode != ModeStartTLS
	authed := false
	var mailFrom string
	var recipients []string
	for {
		line, err := readLine()
		if err != nil {
			return
		}
		command, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(command) {
		case "EHLO", "HELO":
			writef("250-127.0.0.1 Hello")
			if !tlsReady && s.Options.Mode == ModeStartTLS {
				writef("250-STARTTLS")
			}
			if tlsReady {
				if s.Options.LoginOnly {
					writef("250-AUTH LOGIN")
				} else {
					writef("250-AUTH PLAIN LOGIN")
				}
				writef("250 8BITMIME")
			} else {
				writef("250 AUTH PLAIN")
			}
		case "STARTTLS":
			if s.Options.Mode != ModeStartTLS || tlsReady {
				writef("454 TLS not available")
				continue
			}
			writef("220 Ready to start TLS")
			tlsConn := tls.Server(conn, s.tlsConfig)
			if err = tlsConn.Handshake(); err != nil {
				return
			}
			s.TLSStarted.Store(true)
			conn = tlsConn
			reader = bufio.NewReader(conn)
			writer = bufio.NewWriter(conn)
			_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
			tlsReady = true
		case "AUTH":
			if s.Options.FailStage == "auth" {
				s.fail(writef, "auth")
				continue
			}
			if !tlsReady && s.Options.Mode == ModeStartTLS {
				writef("530 Must issue a STARTTLS command first")
				continue
			}
			username, password, ok := s.readAuth(arg, readLine, writef)
			if !ok {
				return
			}
			if username != s.Options.Username || password != s.Options.Password {
				writef("535 authentication failed")
				continue
			}
			authed = true
			writef("235 Authentication successful")
		case "MAIL":
			if !authed {
				writef("530 Authentication required")
				continue
			}
			mailFrom = extractAddress(arg)
			recipients = nil
			writef("250 OK")
		case "RCPT":
			if !authed {
				writef("530 Authentication required")
				continue
			}
			seen := int(s.rcptSeen.Add(1))
			if s.Options.FailStage == "rcpt" || (s.Options.FailRcpts > 0 && seen <= s.Options.FailRcpts) {
				s.fail(writef, "rcpt")
				continue
			}
			recipients = append(recipients, extractAddress(arg))
			writef("250 OK")
		case "DATA":
			if !authed {
				writef("530 Authentication required")
				continue
			}
			writef("354 End data with <CR><LF>.<CR><LF>")
			payload, err := readData(reader)
			if err != nil {
				return
			}
			item := Submission{MailFrom: mailFrom, Recipients: append([]string{}, recipients...), Data: payload}
			s.record(item)
			if s.Options.DropAfterData {
				return
			}
			if s.Options.FailStage == "data" {
				s.fail(writef, "data")
				continue
			}
			writef("250 Queued")
		case "RSET":
			mailFrom = ""
			recipients = nil
			writef("250 OK")
		case "NOOP":
			writef("250 OK")
		case "QUIT":
			writef("221 Bye")
			return
		default:
			writef("500 Command unrecognized")
		}
	}
}

func (s *Server) fail(writef func(string, ...any), stage string) {
	code := s.Options.FailCode
	if code == 0 {
		if stage == "auth" {
			code = 535
		} else if stage == "rcpt" {
			code = 450
		} else {
			code = 550
		}
	}
	message := s.Options.FailMessage
	if message == "" {
		message = "rejected at " + stage
	}
	writef("%d %s", code, message)
}

func (s *Server) readAuth(arg string, readLine func() (string, error), writef func(string, ...any)) (string, string, bool) {
	mechanism, rest, _ := strings.Cut(arg, " ")
	switch strings.ToUpper(mechanism) {
	case "PLAIN":
		encoded := strings.TrimSpace(rest)
		if encoded == "" {
			writef("334 ")
			next, err := readLine()
			if err != nil {
				return "", "", false
			}
			encoded = strings.TrimSpace(next)
		}
		username, password, ok := decodePlain(encoded)
		if !ok {
			writef("501 malformed AUTH PLAIN")
			return "", "", false
		}
		return username, password, true
	case "LOGIN":
		writef("334 %s", base64.StdEncoding.EncodeToString([]byte("Username:")))
		userLine, err := readLine()
		if err != nil {
			return "", "", false
		}
		writef("334 %s", base64.StdEncoding.EncodeToString([]byte("Password:")))
		passLine, err := readLine()
		if err != nil {
			return "", "", false
		}
		username, err := base64.StdEncoding.DecodeString(strings.TrimSpace(userLine))
		if err != nil {
			writef("501 malformed AUTH LOGIN")
			return "", "", false
		}
		password, err := base64.StdEncoding.DecodeString(strings.TrimSpace(passLine))
		if err != nil {
			writef("501 malformed AUTH LOGIN")
			return "", "", false
		}
		return string(username), string(password), true
	default:
		writef("504 unsupported authentication mechanism")
		return "", "", false
	}
}

func decodePlain(encoded string) (string, string, bool) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", false
	}
	parts := bytes.Split(raw, []byte{0})
	if len(parts) < 3 {
		return "", "", false
	}
	return string(parts[1]), string(parts[2]), true
}

func readData(reader *bufio.Reader) ([]byte, error) {
	var payload bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "." {
			return payload.Bytes(), nil
		}
		if strings.HasPrefix(trimmed, "..") {
			trimmed = trimmed[1:]
		}
		payload.WriteString(trimmed)
		payload.WriteString("\r\n")
	}
}

func extractAddress(value string) string {
	upper := strings.ToUpper(value)
	if strings.HasPrefix(upper, "FROM:") {
		value = value[len("FROM:"):]
	} else if strings.HasPrefix(upper, "TO:") {
		value = value[len("TO:"):]
	}
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "<>")
	return value
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
