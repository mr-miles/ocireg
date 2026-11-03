// ocireg: a tiny OCI registry (distribution API) over an OCI image-layout
// directory. HTTP only. Pull is open; push is off unless -push-token is set.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const nameRe = `([a-z0-9][a-z0-9._/-]*)`

var (
	root      string
	pushToken string // fixed by -push-token / OCIREG_PUSH_TOKEN; cannot be changed from the page
	username  = "ocireg"
	tokenFile string // optional; holds the push password and is changed from the page
	mu        sync.Mutex
	hexRe     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	uploadRe  = regexp.MustCompile(`^/v2/` + nameRe + `/blobs/uploads/([0-9a-f]{32})?$`)
	routeRe   = regexp.MustCompile(`^/v2/` + nameRe + `/(manifests|blobs)/([^/]+)$`)
)

type descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type indexDoc struct {
	SchemaVersion int          `json:"schemaVersion"`
	Manifests     []descriptor `json:"manifests"`
}

func readIndex() indexDoc {
	idx := indexDoc{SchemaVersion: 2}
	if data, err := os.ReadFile(filepath.Join(root, "index.json")); err == nil {
		_ = json.Unmarshal(data, &idx)
	}
	return idx
}

func blobPath(digest string) string {
	algo, h, ok := strings.Cut(digest, ":")
	if !ok || algo != "sha256" || !hexRe.MatchString(h) {
		return ""
	}
	p := filepath.Join(root, "blobs", algo, h)
	if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
		return ""
	}
	return p
}

// resolve maps name:tag to a digest. A layout holding a single image answers
// for any tag, so a directory made by `podman save --format oci-dir` just works.
func resolve(name, ref string) string {
	if strings.HasPrefix(ref, "sha256:") {
		return ref
	}
	idx := readIndex()
	for _, m := range idx.Manifests {
		if strings.HasSuffix(m.Annotations["org.opencontainers.image.ref.name"], name+":"+ref) {
			return m.Digest
		}
	}
	if len(idx.Manifests) == 1 {
		return idx.Manifests[0].Digest
	}
	return ""
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// token returns the push password (empty means push is off) and whether the
// page may change it. The file is re-read on every call, so a change applies
// at once.
func token() (value string, editable bool) {
	if pushToken != "" {
		return pushToken, false
	}
	if tokenFile == "" {
		return "", false
	}
	data, _ := os.ReadFile(tokenFile)
	return strings.TrimSpace(string(data)), true
}

func authorized(r *http.Request) bool {
	t, _ := token()
	if t == "" {
		return false
	}
	user, pass, ok := r.BasicAuth()
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(username))
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(t))
	return ok && userOK&passOK == 1
}

var messages = map[string]string{
	"set":      "Push password saved.",
	"cleared":  "Push is now disabled.",
	"wrong":    "The current password is wrong.",
	"short":    "The new password must be at least 8 characters on one line.",
	"mismatch": "The two new passwords are not the same.",
	"fixed":    "The password is fixed by the server's start options and cannot be changed here.",
	"error":    "Could not save the password.",
}

var page = template.Must(template.New("page").Parse(`<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>OCI Registry</title>
<style>
 body{font:16px/1.5 system-ui,sans-serif;max-width:36rem;margin:2rem auto;padding:0 1rem}
 code{background:#eee;padding:.1rem .3rem;border-radius:3px}
 input{display:block;margin:.4rem 0 1rem;padding:.4rem;width:100%;box-sizing:border-box}
 button{padding:.5rem 1rem;margin-right:.5rem}
 .msg{background:#eef;padding:.5rem 1rem;border-radius:4px}
</style>
<h1>OCI Registry</h1>
{{if .Message}}<p class="msg">{{.Message}}</p>{{end}}
<p>Serving plain HTTP on <code>{{.Host}}</code>. Pulling needs no password.</p>
<p>Pushing is <strong>{{if .Enabled}}enabled{{else}}disabled{{end}}</strong>.</p>
<p>Username: <code>{{.Username}}</code> (fixed)</p>
{{if .Editable}}
<h2>{{if .Enabled}}Change or disable the push password{{else}}Set a push password{{end}}</h2>
<form method="post" action="/admin/token">
 {{if .Enabled}}<label>Current password<input type="password" name="current" autocomplete="current-password" required></label>{{end}}
 <label>New password (at least 8 characters)<input type="password" name="new" autocomplete="new-password"></label>
 <label>Repeat new password<input type="password" name="confirm" autocomplete="new-password"></label>
 <button type="submit" name="action" value="set">Save</button>
 {{if .Enabled}}<button type="submit" name="action" value="clear" formnovalidate>Disable push</button>{{end}}
</form>
<p><small>The password is sent unencrypted over your network. {{if not .Enabled}}Until one is set, anyone who can open this page can set it.{{end}}</small></p>
{{else}}
<p><small>The password is fixed by the server's start options.</small></p>
{{end}}
<p>Push with: <code>podman push --tls-verify=false --creds {{.Username}}:PASSWORD IMAGE {{.Host}}/NAME:TAG</code></p>
`))

func adminPage(w http.ResponseWriter, r *http.Request) {
	t, editable := token()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = page.Execute(w, struct {
		Host, Username    string
		Enabled, Editable bool
		Message           string
	}{r.Host, username, t != "", editable, messages[r.URL.Query().Get("msg")]})
}

func setToken(w http.ResponseWriter, r *http.Request) {
	back := func(code string) { http.Redirect(w, r, "/?msg="+code, http.StatusSeeOther) }
	// A browser sends Origin on a form post; refuse one from another site.
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host != r.Host {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
	}
	current, editable := token()
	if !editable {
		back("fixed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if current != "" && subtle.ConstantTimeCompare([]byte(r.PostForm.Get("current")), []byte(current)) != 1 {
		time.Sleep(time.Second) // slow down guessing
		back("wrong")
		return
	}
	if r.PostForm.Get("action") == "clear" {
		if err := os.Remove(tokenFile); err != nil && !os.IsNotExist(err) {
			back("error")
			return
		}
		back("cleared")
		return
	}
	next := r.PostForm.Get("new")
	if next != r.PostForm.Get("confirm") {
		back("mismatch")
		return
	}
	if len(next) < 8 || strings.ContainsAny(next, "\r\n") {
		back("short")
		return
	}
	if err := writeAtomic(tokenFile, []byte(next+"\n"), 0o600); err != nil {
		log.Printf("saving push password: %v", err)
		back("error")
		return
	}
	back("set")
}

func putManifest(w http.ResponseWriter, r *http.Request, name, ref string) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	dir := filepath.Join(root, "blobs", "sha256")
	_ = os.MkdirAll(dir, 0o755)
	if err := writeAtomic(filepath.Join(dir, hex.EncodeToString(sum[:])), data, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !strings.HasPrefix(ref, "sha256:") {
		var doc struct {
			MediaType string `json:"mediaType"`
		}
		_ = json.Unmarshal(data, &doc)
		mu.Lock()
		idx := readIndex()
		tag := name + ":" + ref
		kept := idx.Manifests[:0]
		for _, m := range idx.Manifests {
			if m.Annotations["org.opencontainers.image.ref.name"] != tag {
				kept = append(kept, m)
			}
		}
		idx.Manifests = append(kept, descriptor{
			MediaType: doc.MediaType, Digest: digest, Size: int64(len(data)),
			Annotations: map[string]string{"org.opencontainers.image.ref.name": tag},
		})
		out, _ := json.Marshal(idx)
		err = writeAtomic(filepath.Join(root, "index.json"), out, 0o644)
		mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Location", r.URL.Path)
	w.WriteHeader(http.StatusCreated)
}

func handleUpload(w http.ResponseWriter, r *http.Request, name, id string) {
	loc := func(id string) string { return "/v2/" + name + "/blobs/uploads/" + id }
	path := func(id string) string { return filepath.Join(root, "uploads", id) }
	appendBody := func() (int64, error) {
		f, err := os.OpenFile(path(id), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		if _, err := io.Copy(f, r.Body); err != nil {
			return 0, err
		}
		st, err := f.Stat()
		if err != nil {
			return 0, err
		}
		return st.Size(), nil
	}
	switch {
	case r.Method == http.MethodPost && id == "":
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		id = hex.EncodeToString(b)
		_ = os.MkdirAll(filepath.Join(root, "uploads"), 0o755)
		if err := os.WriteFile(path(id), nil, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Location", loc(id))
		w.Header().Set("Range", "0-0")
		w.Header().Set("Docker-Upload-UUID", id)
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodPatch && id != "":
		size, err := appendBody()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Location", loc(id))
		w.Header().Set("Range", fmt.Sprintf("0-%d", max(size-1, 0)))
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodPut && id != "":
		if _, err := appendBody(); err != nil {
			http.NotFound(w, r)
			return
		}
		want := r.URL.Query().Get("digest")
		algo, hexWant, ok := strings.Cut(want, ":")
		if !ok || algo != "sha256" || !hexRe.MatchString(hexWant) {
			http.Error(w, "bad digest", http.StatusBadRequest)
			return
		}
		f, err := os.Open(path(id))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		h := sha256.New()
		_, _ = io.Copy(h, f)
		f.Close()
		if hex.EncodeToString(h.Sum(nil)) != hexWant {
			http.Error(w, "digest mismatch", http.StatusBadRequest)
			return
		}
		dir := filepath.Join(root, "blobs", "sha256")
		_ = os.MkdirAll(dir, 0o755)
		if err := os.Rename(path(id), filepath.Join(dir, hexWant)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Docker-Content-Digest", want)
		w.Header().Set("Location", "/v2/"+name+"/blobs/"+want)
		w.WriteHeader(http.StatusCreated)
	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

func handle(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/v2/" || p == "/v2" {
		// Pulls stay anonymous (200). The challenge header is what makes
		// Podman/containers-image send its stored login on later requests;
		// without it a push is attempted with no credentials and gets a 401.
		if t, _ := token(); t != "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="ocireg"`)
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if p == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		adminPage(w, r)
		return
	}
	if p == "/admin/token" && r.Method == http.MethodPost {
		setToken(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		if t, _ := token(); t == "" {
			http.Error(w, "push is disabled (set a password on the registry's home page)", http.StatusMethodNotAllowed)
			return
		}
		if !authorized(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="ocireg"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if m := uploadRe.FindStringSubmatch(p); m != nil {
			handleUpload(w, r, m[1], m[2])
			return
		}
		if m := routeRe.FindStringSubmatch(p); m != nil && m[2] == "manifests" && r.Method == http.MethodPut {
			putManifest(w, r, m[1], m[3])
			return
		}
		http.NotFound(w, r)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	m := routeRe.FindStringSubmatch(p)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	name, kind, digest := m[1], m[2], m[3]
	if kind == "manifests" {
		digest = resolve(name, digest)
	}
	path := blobPath(digest)
	if path == "" {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	ctype := "application/octet-stream"
	if kind == "manifests" {
		var doc struct {
			MediaType string `json:"mediaType"`
		}
		data, _ := os.ReadFile(path)
		_ = json.Unmarshal(data, &doc)
		ctype = doc.MediaType
		if ctype == "" {
			ctype = "application/vnd.oci.image.manifest.v1+json"
		}
	}
	st, _ := f.Stat()
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Docker-Content-Digest", digest)
	http.ServeContent(w, r, "", st.ModTime(), f) // handles HEAD and Range
}

func main() {
	flag.StringVar(&root, "dir", "./oci", "OCI image-layout directory")
	addr := flag.String("addr", ":5000", "listen address")
	flag.StringVar(&pushToken, "push-token", os.Getenv("OCIREG_PUSH_TOKEN"), "fixed push password (overrides -token-file; push is off when both are empty)")
	flag.StringVar(&username, "username", username, "username required to push")
	flag.StringVar(&tokenFile, "token-file", "", "file holding the push password, changeable from the home page")
	flag.Parse()
	http.HandleFunc("/", handle)
	t, _ := token()
	log.Printf("ocireg serving %s on %s (push %v)", root, *addr, t != "")
	log.Fatal(http.ListenAndServe(*addr, nil))
}
