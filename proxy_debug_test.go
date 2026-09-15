package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyDebugRedaction(t *testing.T) {
	var v any
	json.Unmarshal([]byte(`{"AccessToken":"secret","MediaSources":[{"Path":"https://cdn/a?api_key=secret&name=movie"}]}`), &v)
	b, _ := json.Marshal(debugJSON(v))
	if strings.Contains(string(b), "secret") || !strings.Contains(string(b), "movie") {
		t.Fatal(string(b))
	}
	h := debugHeaders(map[string][]string{"Authorization": {"secret"}, "Location": {"https://cdn/a?token=secret"}, "User-Agent": {"player"}})
	if strings.Contains(h.Get("Location"), "secret") || h.Get("Authorization") != "[REDACTED]" || h.Get("User-Agent") != "player" {
		t.Fatal(h)
	}
}
func TestProxyDebugResponsePreservesBody(t *testing.T) {
	dst := httptest.NewRecorder()
	w := &debugResponse{ResponseWriter: dst}
	w.Header().Set("Content-Type", "application/json")
	payload := strings.Repeat("x", 40000)
	w.WriteHeader(400)
	w.Write([]byte(payload))
	w.Flush()
	if dst.Code != 400 || dst.Body.String() != payload || w.data.Len() != 32768 || !w.truncated {
		t.Fatal("response or capture limits changed")
	}
	dst = httptest.NewRecorder()
	w = &debugResponse{ResponseWriter: dst}
	w.Header().Set("Content-Type", "video/mp4")
	w.Write([]byte(payload))
	if w.data.Len() != 0 || dst.Body.Len() != 40000 {
		t.Fatal("video buffered")
	}
}
func TestProxyDebugDefaultOff(t *testing.T) {
	a := &App{}
	if a.proxyDebugEnabled() {
		t.Fatal("must default off")
	}
}
func TestProxyDebugSettingsAndCapture(t *testing.T) {
	a := testApp(t)
	if a.proxyDebugEnabled() {
		t.Fatal("default on")
	}
	settings := httptest.NewRecorder()
	a.enhancementSettings(settings, httptest.NewRequest("PUT", "/admin/enhancements", strings.NewReader(`{"ProxyDebug":true}`)))
	if settings.Code != 200 || !a.proxyDebugEnabled() {
		t.Fatalf("enable: %d", settings.Code)
	}
	r := httptest.NewRequest("GET", "/emby/System/Info/Public?api_key=hidden", nil)
	r.Header.Set("X-Emby-Token", "hidden")
	dst := httptest.NewRecorder()
	a.serve(dst, r)
	logs := httptest.NewRecorder()
	a.proxyDebugLogs(logs, httptest.NewRequest("GET", "/admin/logs?category=proxy", nil))
	if strings.Contains(logs.Body.String(), "hidden") || !strings.Contains(logs.Body.String(), "ServerName") || len(a.proxyDebug.entries) != 1 {
		t.Fatal("capture or redaction failed")
	}
	settings = httptest.NewRecorder()
	a.enhancementSettings(settings, httptest.NewRequest("PUT", "/admin/enhancements", strings.NewReader(`{"ProxyDebug":false}`)))
	a.serve(httptest.NewRecorder(), r)
	if len(a.proxyDebug.entries) != 1 {
		t.Fatal("captured while disabled")
	}
	a.proxyDebugLogs(httptest.NewRecorder(), httptest.NewRequest("DELETE", "/admin/logs?category=proxy", nil))
	if len(a.proxyDebug.entries) != 0 {
		t.Fatal("clear failed")
	}
}
