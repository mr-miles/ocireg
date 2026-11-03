package main

// Optional HTTPS. The listener sniffs the first byte of each connection, so one
// port serves both HTTPS (a TLS handshake starts with 0x16) and plain HTTP.
// Certificates live in -tls-dir and are re-read when the files change, so
// uploading one on the home page takes effect without a restart.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	tlsDir    string
	tlsMu     sync.Mutex
	tlsCached struct {
		loaded          bool
		cert            *tls.Certificate
		certMod, keyMod time.Time
	}
)

func tlsPath(name string) string { return filepath.Join(tlsDir, name) }

// loadTLS returns the installed certificate, or nil when there is none (or it
// is unusable). The result is cached until either file changes.
func loadTLS() *tls.Certificate {
	if tlsDir == "" {
		return nil
	}
	cs, err1 := os.Stat(tlsPath("server.crt"))
	ks, err2 := os.Stat(tlsPath("server.key"))
	tlsMu.Lock()
	defer tlsMu.Unlock()
	if err1 != nil || err2 != nil {
		tlsCached.loaded, tlsCached.cert = false, nil
		return nil
	}
	if tlsCached.loaded && cs.ModTime().Equal(tlsCached.certMod) && ks.ModTime().Equal(tlsCached.keyMod) {
		return tlsCached.cert
	}
	tlsCached.loaded, tlsCached.certMod, tlsCached.keyMod = true, cs.ModTime(), ks.ModTime()
	pair, err := tls.LoadX509KeyPair(tlsPath("server.crt"), tlsPath("server.key"))
	if err != nil {
		log.Printf("tls: cannot use the installed certificate: %v", err)
		tlsCached.cert = nil
		return nil
	}
	pair.Leaf, _ = x509.ParseCertificate(pair.Certificate[0])
	tlsCached.cert = &pair
	return &pair
}

var tlsConfig = &tls.Config{
	MinVersion: tls.VersionTLS12,
	NextProtos: []string{"http/1.1"},
	GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		if c := loadTLS(); c != nil {
			return c, nil
		}
		return nil, errors.New("no certificate installed")
	},
}

type muxListener struct{ net.Listener }

func (l muxListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &muxConn{Conn: c}, nil
}

// muxConn decides on its first Read whether the peer speaks TLS.
type muxConn struct {
	net.Conn
	once sync.Once
	br   *bufio.Reader
	tc   *tls.Conn
}

type bufConn struct {
	net.Conn
	br *bufio.Reader
}

func (c bufConn) Read(p []byte) (int, error) { return c.br.Read(p) }

func (c *muxConn) Read(p []byte) (int, error) {
	c.once.Do(func() {
		c.br = bufio.NewReader(c.Conn)
		if b, err := c.br.Peek(1); err == nil && b[0] == 0x16 {
			c.tc = tls.Server(bufConn{c.Conn, c.br}, tlsConfig)
		}
	})
	if c.tc != nil {
		return c.tc.Read(p)
	}
	return c.br.Read(p)
}

func (c *muxConn) Write(p []byte) (int, error) {
	if c.tc != nil {
		return c.tc.Write(p)
	}
	return c.Conn.Write(p)
}

func (c *muxConn) Close() error {
	if c.tc != nil {
		return c.tc.Close()
	}
	return c.Conn.Close()
}

type connKey struct{}

func connContext(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

func isSecure(r *http.Request) bool {
	c, ok := r.Context().Value(connKey{}).(*muxConn)
	return ok && c.tc != nil
}

// redirectToHTTPS sends plain-HTTP requests to HTTPS once a certificate is
// installed. The public certificate downloads are exempt so a client can fetch
// the CA before it trusts the server.
func redirectToHTTPS(w http.ResponseWriter, r *http.Request) bool {
	if tlsDir == "" || isSecure(r) || loadTLS() == nil || strings.HasPrefix(r.URL.Path, "/tls/") {
		return false
	}
	http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusPermanentRedirect)
	return true
}

// --- home page: certificate section ---------------------------------------

type certInfo struct {
	Names, Expires, Fingerprint string
}

type tlsView struct {
	Available bool // -tls-dir was given
	CanEdit   bool // a password exists, so changes are allowed
	Active    bool
	Secure    bool // this page was loaded over HTTPS
	Cert      *certInfo
	CA        *certInfo
}

func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, 32)
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

func firstCert(data []byte) *x509.Certificate {
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil
		}
		if block.Type == "CERTIFICATE" {
			if c, err := x509.ParseCertificate(block.Bytes); err == nil {
				return c
			}
		}
	}
}

func describe(c *x509.Certificate) *certInfo {
	names := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
	}
	if len(names) == 0 {
		names = append(names, c.Subject.CommonName)
	}
	return &certInfo{strings.Join(names, ", "), c.NotAfter.Format("2006-01-02"), fingerprint(c.Raw)}
}

func viewTLS(r *http.Request) tlsView {
	v := tlsView{Available: tlsDir != "", Secure: isSecure(r)}
	if !v.Available {
		return v
	}
	v.Active = loadTLS() != nil
	if data, err := os.ReadFile(tlsPath("server.crt")); err == nil {
		if c := firstCert(data); c != nil {
			v.Cert = describe(c)
		}
	}
	if data, err := os.ReadFile(tlsPath("ca.crt")); err == nil {
		if c := firstCert(data); c != nil {
			v.CA = describe(c)
		}
	}
	return v
}

var _ = template.Must(page.New("tls").Parse(`{{if .Available}}
<h2>HTTPS</h2>
{{if .Active}}<p>HTTPS is <strong>active</strong>. Plain HTTP requests are redirected.</p>
{{else}}<p>HTTPS is <strong>off</strong>: no certificate is installed. OpenShell pulls images over HTTPS only.</p>{{end}}
{{if .Cert}}<p>Server certificate: <code>{{.Cert.Names}}</code>, expires {{.Cert.Expires}}.</p>{{end}}
{{if or .CA .Cert}}
<h3>Certificates to trust</h3>
<p>Download these on each machine that pulls, check the fingerprint, then trust it
(on Debian/Ubuntu: copy to <code>/usr/local/share/ca-certificates/</code> and run <code>sudo update-ca-certificates</code>).</p>
<ul>
{{if .CA}}<li><a href="/tls/ca.crt" download="ocireg-ca.crt">CA certificate</a><br><small>SHA-256 {{.CA.Fingerprint}}</small></li>{{end}}
{{if .Cert}}<li><a href="/tls/server.crt" download="ocireg-server.crt">Server certificate</a><br><small>SHA-256 {{.Cert.Fingerprint}}</small></li>{{end}}
</ul>
{{end}}
{{if .CanEdit}}
<h3>Install or replace certificates</h3>
{{if not .Secure}}<p><small>You are on plain HTTP, so the private key travels unencrypted. Do this only on a network you trust, or put the files on the NAS by other means.</small></p>{{end}}
<form method="post" action="/admin/tls" enctype="multipart/form-data">
 <label>Server certificate (PEM)<input type="file" name="cert"></label>
 <label>Private key (PEM)<input type="file" name="key"></label>
 <label>CA certificate for clients to trust (PEM, optional)<input type="file" name="ca"></label>
 <label>Push password<input type="password" name="current" autocomplete="current-password" required></label>
 <button type="submit" name="action" value="save">Save</button>
 {{if or .Active .Cert .CA}}<button type="submit" name="action" value="remove" formnovalidate>Remove certificates</button>{{end}}
</form>
{{else}}<p><small>Set a push password above first: certificate changes need it.</small></p>{{end}}
{{end}}`))

// --- handlers ---------------------------------------------------------------

func serveCert(w http.ResponseWriter, r *http.Request, name string) {
	data, err := os.ReadFile(tlsPath(name))
	if tlsDir == "" || err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	_, _ = w.Write(data)
}

func formFile(r *http.Request, name string) []byte {
	f, _, err := r.FormFile(name)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 64<<10))
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	return b
}

func sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host != r.Host {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return false
		}
	}
	return true
}

func setCerts(w http.ResponseWriter, r *http.Request) {
	back := func(code string) { http.Redirect(w, r, "/?msg="+code, http.StatusSeeOther) }
	if !sameOrigin(w, r) {
		return
	}
	if tlsDir == "" {
		back("tlsoff")
		return
	}
	current, _ := token()
	if current == "" {
		back("needpass")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := r.ParseMultipartForm(256 << 10); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !passwordMatches(r.FormValue("current"), current) {
		time.Sleep(time.Second)
		back("wrong")
		return
	}
	if r.FormValue("action") == "remove" {
		for _, n := range []string{"server.crt", "server.key", "ca.crt"} {
			if err := os.Remove(tlsPath(n)); err != nil && !os.IsNotExist(err) {
				back("tlserror")
				return
			}
		}
		back("tlsremoved")
		return
	}
	crt, key, ca := formFile(r, "cert"), formFile(r, "key"), formFile(r, "ca")
	switch {
	case crt == nil && key == nil && ca == nil:
		back("nofiles")
		return
	case (crt == nil) != (key == nil):
		back("pair")
		return
	}
	if crt != nil {
		if _, err := tls.X509KeyPair(crt, key); err != nil {
			back("badpair")
			return
		}
	}
	if ca != nil && firstCert(ca) == nil {
		back("badca")
		return
	}
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		back("tlserror")
		return
	}
	for _, f := range []struct {
		name string
		data []byte
		perm os.FileMode
	}{{"server.key", key, 0o600}, {"server.crt", crt, 0o644}, {"ca.crt", ca, 0o644}} {
		if f.data == nil {
			continue
		}
		if err := writeAtomic(tlsPath(f.name), f.data, f.perm); err != nil {
			log.Printf("saving %s: %v", f.name, err)
			back("tlserror")
			return
		}
	}
	back("tlsset")
}
