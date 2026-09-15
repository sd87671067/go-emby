package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDirectSTRMResolverSelection(t *testing.T) {
	t.Setenv("NANSHARE_URL", "http://resolver:8115")
	t.Setenv("XIAOYA_URL", "http://resolver:5678")
	for _, tc := range []struct{ raw, want string }{
		{"http://xiaoya.host:5678/d/movie", "http://resolver:5678"},
		{"http://172.17.0.1:8115/api/?pickcode=secret", "http://resolver:8115"},
		{"http://untrusted.example:8115/api/", ""},
		{"http://172.17.0.1:8115/admin", ""},
		{"https://cdn.example/movie", ""},
	} {
		if got := strmResolver(tc.raw); got != tc.want {
			t.Fatalf("%s: %q", tc.raw, got)
		}
	}
}
func TestDirectSTRMCacheIsolation(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/" || r.URL.Query().Get("pickcode") != "source-secret" {
			t.Error("source was not preserved")
		}
		if r.Header.Get("X-Emby-Token") != "" {
			t.Error("viewer token leaked")
		}
		w.Header().Set("Location", "https://cdn.example/movie")
		w.WriteHeader(302)
	}))
	defer srv.Close()
	a := &App{}
	x := Item{URL: "http://172.17.0.1:8115/api/?pickcode=source-secret"}
	for _, tc := range []struct {
		token, ua string
		count     int32
	}{{"a", "player", 1}, {"a", "player", 1}, {"b", "player", 2}, {"b", "other", 3}} {
		r := httptest.NewRequest("GET", "/Videos/abc/stream", nil)
		r.Header.Set("X-Emby-Token", tc.token)
		r.Header.Set("User-Agent", tc.ua)
		w := httptest.NewRecorder()
		a.resolveSTRM(w, r, x, srv.URL)
		if w.Code != 302 || w.Header().Get("Location") != "https://cdn.example/movie" || calls.Load() != tc.count {
			t.Fatalf("status=%d calls=%d", w.Code, calls.Load())
		}
	}
}
func TestDirectSTRMRejectsMediaBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write([]byte("video-body"))
	}))
	defer srv.Close()
	a := &App{}
	w := httptest.NewRecorder()
	a.resolveSTRM(w, httptest.NewRequest("GET", "/Videos/abc/stream", nil), Item{URL: "http://xiaoya.host:5678/movie"}, srv.URL)
	if w.Code != 502 {
		t.Fatalf("media body accepted: %d", w.Code)
	}
}
