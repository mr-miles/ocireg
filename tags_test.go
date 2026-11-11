package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// storeImage writes a manifest blob straight into the layout, as an existing
// directory (e.g. from `podman save`) would have it, and returns its digest.
func storeImage(t *testing.T, body string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	dir := filepath.Join(root, "blobs", "sha256")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, hex.EncodeToString(sum[:])), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return "sha256:" + hex.EncodeToString(sum[:])
}

// writeIndex sets index.json to the given ref name -> digest entries, in order.
func writeIndex(t *testing.T, entries ...[2]string) {
	t.Helper()
	idx := indexDoc{SchemaVersion: 2}
	for _, e := range entries {
		idx.Manifests = append(idx.Manifests, descriptor{
			MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: e[1], Size: 1,
			Annotations: map[string]string{"org.opencontainers.image.ref.name": e[0]},
		})
	}
	data, _ := json.Marshal(idx)
	if err := os.WriteFile(filepath.Join(root, "index.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func pushManifest(t *testing.T, name, tag, body string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/v2/"+name+"/manifests/"+tag, strings.NewReader(body))
	req.SetBasicAuth("ocireg", "testpass123")
	rec := httptest.NewRecorder()
	handle(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("push %s:%s: got %d: %s", name, tag, rec.Code, rec.Body)
	}
	return rec.Header().Get("Docker-Content-Digest")
}

// pull returns the digest served for name:tag, or "" on 404.
func pull(t *testing.T, name, tag string) string {
	t.Helper()
	rec := do(http.MethodGet, "/v2/"+name+"/manifests/"+tag, nil)
	switch rec.Code {
	case http.StatusOK:
		return rec.Header().Get("Docker-Content-Digest")
	case http.StatusNotFound:
		return ""
	default:
		t.Fatalf("pull %s:%s: got %d", name, tag, rec.Code)
		return ""
	}
}

// Pushing a tag must make pulls of that tag serve the new image, even when the
// layout already holds entries whose names merely end with the same name:tag.
// Before the lookup was exact, those older entries kept winning.
func TestPushedTagIsServedOverSimilarNames(t *testing.T) {
	for _, other := range []string{"localhost/app:latest", "myapp:latest", "team/app:latest"} {
		t.Run(other, func(t *testing.T) {
			setup(t, "testpass123", "")
			old := storeImage(t, `{"mediaType":"application/vnd.oci.image.manifest.v1+json","old":"`+other+`"}`)
			writeIndex(t, [2]string{other, old})

			pushed := pushManifest(t, "app", "latest", `{"mediaType":"application/vnd.oci.image.manifest.v1+json","new":1}`)
			if got := pull(t, "app", "latest"); got != pushed {
				t.Fatalf("app:latest served %s, want the pushed %s (old %s entry won)", got, pushed, other)
			}
			// The other image is untouched and still reachable by its own name.
			otherName, otherTag, _ := strings.Cut(other, ":")
			if got := pull(t, otherName, otherTag); got != old {
				t.Fatalf("%s served %s, want %s", other, got, old)
			}
		})
	}
}

// Pushing the same tag again must move it to the new image.
func TestRepushMovesTag(t *testing.T) {
	setup(t, "testpass123", "")
	first := pushManifest(t, "app", "latest", `{"mediaType":"application/vnd.oci.image.manifest.v1+json","v":1}`)
	second := pushManifest(t, "app", "latest", `{"mediaType":"application/vnd.oci.image.manifest.v1+json","v":2}`)
	if first == second {
		t.Fatal("test images should differ")
	}
	if got := pull(t, "app", "latest"); got != second {
		t.Fatalf("served %s, want %s", got, second)
	}
}

// A name that only ends with the requested one is a different repository and
// must never answer for it.
func TestLookupIsExact(t *testing.T) {
	setup(t, "testpass123", "")
	mine := storeImage(t, `{"mediaType":"application/vnd.oci.image.manifest.v1+json","repo":"myapp"}`)
	theirs := storeImage(t, `{"mediaType":"application/vnd.oci.image.manifest.v1+json","repo":"other"}`)
	writeIndex(t, [2]string{"myapp:latest", mine}, [2]string{"other:latest", theirs})

	for _, tc := range []struct{ name, tag, want string }{
		{"myapp", "latest", mine},
		{"app", "latest", ""}, // only myapp:latest ends with app:latest
		{"myapp", "test", ""}, // tags are exact too
		{"localhost/myapp", "latest", ""},
	} {
		if got := pull(t, tc.name, tc.tag); got != tc.want {
			t.Errorf("%s:%s served %q, want %q", tc.name, tc.tag, got, tc.want)
		}
	}
}

// A layout holding a single image (e.g. from `podman save --format oci-dir`)
// still answers for any name and tag.
func TestSingleImageLayoutAnswersAnyTag(t *testing.T) {
	setup(t, "testpass123", "")
	only := storeImage(t, `{"mediaType":"application/vnd.oci.image.manifest.v1+json","only":1}`)
	writeIndex(t, [2]string{"localhost/app:latest", only})
	for _, ref := range [][2]string{{"app", "latest"}, {"anything", "v1"}, {"localhost/app", "latest"}} {
		if got := pull(t, ref[0], ref[1]); got != only {
			t.Errorf("%s:%s served %q, want %q", ref[0], ref[1], got, only)
		}
	}
}
