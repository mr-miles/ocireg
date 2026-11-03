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
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const nameRe = `([a-z0-9][a-z0-9._/-]*)`

var (
	root      string
	pushToken string
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

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func authorized(r *http.Request) bool {
	if pushToken == "" {
		return false
	}
	_, pass, ok := r.BasicAuth()
	return ok && subtle.ConstantTimeCompare([]byte(pass), []byte(pushToken)) == 1
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
	if err := writeAtomic(filepath.Join(dir, hex.EncodeToString(sum[:])), data); err != nil {
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
		err = writeAtomic(filepath.Join(root, "index.json"), out)
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
		w.WriteHeader(http.StatusOK)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		if pushToken == "" {
			http.Error(w, "push is disabled (start with -push-token)", http.StatusMethodNotAllowed)
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
	flag.StringVar(&pushToken, "push-token", os.Getenv("OCIREG_PUSH_TOKEN"), "password required to push (push is disabled when empty)")
	flag.Parse()
	http.HandleFunc("/", handle)
	log.Printf("ocireg serving %s on %s (push %v)", root, *addr, pushToken != "")
	log.Fatal(http.ListenAndServe(*addr, nil))
}
