package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAfuseKtDuplicateSlashLogin(t *testing.T) {
	a := testApp(t)
	for _, path := range []string{"//emby/Users/authenticatebyname", "///emby//emby/Users//authenticatebyname/"} {
		for _, password := range []string{"test-password-12345", "wrong"} {
			r := httptest.NewRequest("POST", path, strings.NewReader("Username=admin&Pw="+password))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("X-Emby-Client", "AfuseKt")
			w := httptest.NewRecorder()
			a.serve(w, r)
			if password == "wrong" {
				if w.Code != 401 {
					t.Fatalf("wrong password: %d", w.Code)
				}
				continue
			}
			if w.Code != 200 {
				t.Fatalf("login: %d %s", w.Code, w.Body.String())
			}
			var b struct{ AccessToken string }
			json.Unmarshal(w.Body.Bytes(), &b)
			r = httptest.NewRequest("GET", "//emby/Users/Me", nil)
			r.Header.Set("X-Emby-Token", b.AccessToken)
			w = httptest.NewRecorder()
			a.serve(w, r)
			if w.Code != 200 {
				t.Fatalf("session: %d", w.Code)
			}
		}
	}
}
func TestProxyDebugHyphenatedAPIKey(t *testing.T) {
	h := debugHeaders(map[string][]string{"X-Emby-Api-Key": {"sensitive"}})
	if h.Get("X-Emby-Api-Key") != "[REDACTED]" {
		t.Fatal("API key not redacted")
	}
	if strings.Contains(debugURL("/Items?X-Emby-Api-Key=sensitive"), "sensitive") {
		t.Fatal("query key not redacted")
	}
}
