package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setup points the server's globals at a fresh temp directory and restores
// them when the test ends.
func setup(t *testing.T, fixedToken, fileToken string) {
	t.Helper()
	oldRoot, oldPush, oldFile, oldTLS, oldUser, oldDelay := root, pushToken, tokenFile, tlsDir, username, authFailDelay
	t.Cleanup(func() {
		root, pushToken, tokenFile, tlsDir, username, authFailDelay = oldRoot, oldPush, oldFile, oldTLS, oldUser, oldDelay
	})
	dir := t.TempDir()
	root, pushToken, tokenFile, tlsDir, username = filepath.Join(dir, "oci"), fixedToken, "", "", "ocireg"
	authFailDelay = 0 // these tests change globals, so they must not use t.Parallel
	if fileToken != "" {
		tokenFile = filepath.Join(dir, "push-token")
		if err := os.WriteFile(tokenFile, []byte(fileToken+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The tests turn the delay off, so check the shipped default separately: a
// change that left it at zero would remove the brake on password guessing.
func TestAuthFailDelayDefault(t *testing.T) {
	if authFailDelay < time.Second {
		t.Fatalf("authFailDelay = %v, want at least 1s", authFailDelay)
	}
}

type creds struct{ user, pass string }

func do(method, path string, c *creds) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if c != nil {
		req.SetBasicAuth(c.user, c.pass)
	}
	rec := httptest.NewRecorder()
	handle(rec, req)
	return rec
}

// podman/docker login sends the credentials to GET /v2/ and treats 200 as
// success, so /v2/ must reject wrong credentials or a bad login is saved and
// only fails later on push.
func TestV2ChecksCredentials(t *testing.T) {
	setup(t, "testpass123", "")
	cases := []struct {
		name  string
		creds *creds
		want  int
	}{
		{"no credentials (anonymous pull)", nil, http.StatusOK},
		{"correct credentials", &creds{"ocireg", "testpass123"}, http.StatusOK},
		{"wrong password", &creds{"ocireg", "wrong"}, http.StatusUnauthorized},
		{"wrong username", &creds{"someone", "testpass123"}, http.StatusUnauthorized},
		{"empty password", &creds{"ocireg", ""}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(http.MethodGet, "/v2/", tc.creds)
			if rec.Code != tc.want {
				t.Fatalf("GET /v2/: got %d, want %d", rec.Code, tc.want)
			}
			// The challenge must always be present, or podman never sends
			// its stored login on later push requests.
			if got := rec.Header().Get("WWW-Authenticate"); got != `Basic realm="ocireg"` {
				t.Fatalf("WWW-Authenticate = %q", got)
			}
		})
	}
}

// The scenario from the bug report: if login accepts credentials, push with
// the same credentials must work; if push would reject them, login must too.
func TestLoginAgreesWithPush(t *testing.T) {
	setup(t, "testpass123", "")
	for _, c := range []creds{{"ocireg", "testpass123"}, {"ocireg", "wrong"}, {"other", "testpass123"}} {
		login := do(http.MethodGet, "/v2/", &c).Code == http.StatusOK
		push := do(http.MethodPost, "/v2/foo/blobs/uploads/", &c).Code == http.StatusAccepted
		if login != push {
			t.Errorf("%s:%s login ok=%v but push ok=%v", c.user, c.pass, login, push)
		}
	}
}

// A password changed on the home page applies at once, so a login made with
// the old one must start failing.
func TestV2RejectsOldPasswordAfterChange(t *testing.T) {
	setup(t, "", "oldpassword")
	old := &creds{"ocireg", "oldpassword"}
	if code := do(http.MethodGet, "/v2/", old).Code; code != http.StatusOK {
		t.Fatalf("before change: got %d, want 200", code)
	}
	if err := os.WriteFile(tokenFile, []byte("newpassword\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := do(http.MethodGet, "/v2/", old).Code; code != http.StatusUnauthorized {
		t.Fatalf("old password after change: got %d, want 401", code)
	}
	if code := do(http.MethodGet, "/v2/", &creds{"ocireg", "newpassword"}).Code; code != http.StatusOK {
		t.Fatalf("new password: got %d, want 200", code)
	}
}

// With push disabled there is nothing to log in to; /v2/ stays open and sends
// no challenge.
func TestV2PushDisabled(t *testing.T) {
	setup(t, "", "")
	rec := do(http.MethodGet, "/v2/", &creds{"anyone", "anything"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("unexpected challenge %q", got)
	}
}
