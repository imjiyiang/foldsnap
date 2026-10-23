package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOccupiedPortFallbackKeepsOriginalService(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	address := occupied.Addr().String()
	if l, err := listenLocal(address, false); err == nil {
		l.Close()
		t.Fatal("explicit port unexpectedly replaced")
	}
	l, err := listenLocal(address, true)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l.Addr().String() == address {
		t.Fatal("reused occupied endpoint")
	}
	c, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("original service disrupted: %v", err)
	}
	c.Close()
}

func TestReuseAuthenticatesExactDataDirectoryEndpoint(t *testing.T) {
	const token = "a-private-test-token-1234567890"
	var received bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/system/status" || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "wrong identity", 401)
			return
		}
		received = true
		w.Write([]byte(`{"service":"ready"}`))
	}))
	defer s.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "access.token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeEndpoint(dir, strings.TrimPrefix(s.URL, "http://")); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(dir, "service-endpoint.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("endpoint permissions")
	}
	url, err := existingServiceURL(dir, 0)
	if err != nil || url != s.URL+"/#token="+token || !received {
		t.Fatalf("reuse: %q %v", url, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "access.token"), []byte("another-data-directory-token-12345"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := existingServiceURL(dir, 0); err == nil {
		t.Fatal("opened another directory's service")
	}
}

func TestRejectForeignOrStaleEndpoint(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "access.token"), []byte("test-token-at-least-24-characters"), 0600)
	for _, address := range []string{"example.com:8787", "192.0.2.1:8787", "127.0.0.1:0", "127.0.0.1:1"} {
		if err := writeEndpoint(dir, address); err != nil {
			t.Fatal(err)
		}
		if _, err := existingServiceURL(dir, 0); err == nil {
			t.Fatalf("accepted %s", address)
		}
	}
}
