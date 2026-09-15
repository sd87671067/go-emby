package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Captures only existing responses: no network probes, database writes or body buffering on success.
func (a *App) beginRedirectLog(w *http.ResponseWriter, r *http.Request) func() {
	if !isVideoRequest(r.URL.Path) || strings.EqualFold(q(r, "GoEmbyProbe"), "true") {
		return nil
	}
	started := time.Now()
	tracked := &debugResponse{ResponseWriter: *w}
	*w = tracked
	return func() {
		elapsed := time.Since(started)
		_ = http.NewResponseController(tracked.ResponseWriter).Flush()
		status := tracked.status
		if status == 0 {
			status = 200
		}
		state := "complete"
		detail := M{"Method": r.Method, "Request": debugURL(r.URL.String()), "Client": r.UserAgent(), "RemoteAddr": r.RemoteAddr, "Status": status, "Location": debugURL(tracked.Header().Get("Location")), "DurationMs": float64(elapsed.Microseconds()) / 1000}
		if status < 300 || status >= 400 || tracked.Header().Get("Location") == "" || tracked.writeErr != nil {
			state = "error"
		}
		if tracked.writeErr != nil {
			detail["WriteError"] = safeProxyError(tracked.writeErr)
		}
		if r.Context().Err() != nil {
			detail["ContextError"] = r.Context().Err().Error()
			state = "error"
		}
		if tracked.data.Len() > 0 && !tracked.truncated {
			var v any
			if json.Unmarshal(tracked.data.Bytes(), &v) == nil {
				detail["Response"] = debugJSON(v)
			}
		}
		b, _ := json.MarshalIndent(detail, "", "  ")
		key := a.newActivity("redirect", "", fmt.Sprintf("播放重定向 · HTTP %d", status))
		a.changeActivity(key, func(v *activityEntry) { v.State = state; v.Current = string(b); v.Started = started })
	}
}
