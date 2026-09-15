package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type proxyDebugState struct {
	mu      sync.Mutex
	entries []activityEntry
}

func (a *App) proxyDebugEnabled() bool {
	if a.db == nil {
		return false
	}
	var v string
	return a.db.QueryRow("SELECT v FROM settings WHERE k='proxy_debug'").Scan(&v) == nil && v == "true"
}
func debugSecret(k string) bool {
	k = strings.ToLower(k)
	return strings.Contains(k, "token") || strings.Contains(k, "password") || strings.Contains(k, "secret") || strings.Contains(k, "authorization") || strings.Contains(k, "cookie") || strings.Contains(k, "signature") || strings.Contains(k, "api-key") || strings.Contains(k, "api_key") || strings.Contains(k, "apikey") || k == "pw" || k == "key"
}
func debugURL(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return "[invalid URL]"
	}
	u.User = nil
	q := u.Query()
	for k := range q {
		if debugSecret(k) {
			q.Set(k, "[REDACTED]")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
func debugHeaders(h http.Header) http.Header {
	out := h.Clone()
	for k, values := range out {
		if debugSecret(k) {
			out[k] = []string{"[REDACTED]"}
		} else {
			for i, v := range values {
				if k == "Location" || k == "Referer" {
					v = debugURL(v)
				}
				if len(v) > 2048 {
					v = v[:2048] + "…"
				}
				out[k][i] = v
			}
		}
	}
	return out
}
func debugJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if debugSecret(k) {
				x[k] = "[REDACTED]"
			} else {
				x[k] = debugJSON(val)
			}
		}
	case []any:
		for i, val := range x {
			x[i] = debugJSON(val)
		}
	case string:
		if strings.Contains(x, "://") || strings.Contains(x, "?") {
			return debugURL(x)
		}
	}
	return v
}

type debugResponse struct {
	http.ResponseWriter
	status    int
	size      int64
	data      bytes.Buffer
	truncated bool
	writeErr  error
}

func (w *debugResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *debugResponse) WriteHeader(s int) {
	if s >= 100 && s < 200 {
		w.ResponseWriter.WriteHeader(s)
		return
	}
	if w.status == 0 {
		w.status = s
		w.ResponseWriter.WriteHeader(s)
	}
}
func (w *debugResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, e := w.ResponseWriter.Write(b)
	w.size += int64(n)
	w.writeErr = e
	ct := strings.ToLower(w.Header().Get("Content-Type"))
	if strings.Contains(ct, "json") {
		left := 32768 - w.data.Len()
		take := n
		if take > left {
			take = left
			w.truncated = true
		}
		w.data.Write(b[:take])
	}
	return n, e
}
func (w *debugResponse) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if e := http.NewResponseController(w.ResponseWriter).Flush(); e != nil {
		w.writeErr = e
	}
}
func (a *App) beginProxyDebug(w *http.ResponseWriter, r *http.Request) func() {
	redirectDone := a.beginRedirectLog(w, r)
	p := strings.ToLower(r.URL.Path)
	if p == "/health" || p == "/" || strings.HasPrefix(p, "/admin") || strings.HasPrefix(p, "/files") || !a.proxyDebugEnabled() {
		return redirectDone
	}
	started := time.Now()
	request := M{"Method": r.Method, "URL": debugURL(r.URL.String()), "Host": r.Host, "RemoteAddr": r.RemoteAddr, "Headers": debugHeaders(r.Header)}
	tracked := &debugResponse{ResponseWriter: *w}
	*w = tracked
	return func() {
		if redirectDone != nil {
			defer redirectDone()
		}
		status := tracked.status
		if status == 0 {
			status = 200
		}
		response := M{"Status": status, "Headers": debugHeaders(tracked.Header()), "Bytes": tracked.size, "DurationMs": time.Since(started).Milliseconds()}
		if tracked.truncated {
			response["JSON"] = "[响应超过32 KiB，省略正文以避免输出未脱敏的截断JSON]"
		} else if tracked.data.Len() > 0 {
			var v any
			if json.Unmarshal(tracked.data.Bytes(), &v) == nil {
				response["JSON"] = debugJSON(v)
			} else {
				response["JSON"] = "[无效JSON或压缩响应，正文省略]"
			}
		}
		if tracked.writeErr != nil {
			response["WriteError"] = safeProxyError(tracked.writeErr)
		}
		if r.Context().Err() != nil {
			response["ContextError"] = r.Context().Err().Error()
		}
		detail, _ := json.MarshalIndent(M{"Request": request, "Response": response}, "", "  ")
		state := "complete"
		if status >= 400 || tracked.writeErr != nil {
			state = "error"
		}
		entry := activityEntry{ID: id(), Category: "proxy", Name: fmt.Sprintf("%s %s · HTTP %d", r.Method, r.URL.Path, status), State: state, Current: string(detail), Started: started, Updated: time.Now()}
		a.proxyDebug.mu.Lock()
		defer a.proxyDebug.mu.Unlock()
		if len(a.proxyDebug.entries) >= 100 {
			copy(a.proxyDebug.entries, a.proxyDebug.entries[1:])
			a.proxyDebug.entries = a.proxyDebug.entries[:99]
		}
		a.proxyDebug.entries = append(a.proxyDebug.entries, entry)
	}
}
func (a *App) proxyDebugLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	enabled := a.proxyDebugEnabled()
	a.proxyDebug.mu.Lock()
	defer a.proxyDebug.mu.Unlock()
	if r.Method == "DELETE" {
		a.proxyDebug.entries = nil
		respond(w, M{"ok": true})
		return
	}
	if r.Method != "GET" {
		fail(w, 405, "GET required")
		return
	}
	entries := make([]activityEntry, 0, len(a.proxyDebug.entries))
	for i := len(a.proxyDebug.entries) - 1; i >= 0; i-- {
		entries = append(entries, a.proxyDebug.entries[i])
	}
	respond(w, M{"Enabled": enabled, "Entries": entries})
}
