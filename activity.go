package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type activityEntry struct {
	ID, Category, ItemID, Name, State, Current, Error string
	DeviceKey                                         string
	UserID                                            string
	PositionTicks, RunTimeTicks                       int64
	Progress                                          float64
	Online                                            bool
	Hidden                                            bool
	Username, IP, Device, Client                      string
	Total, Done                                       int
	Started, Updated                                  time.Time
}
type activityState struct {
	mu      sync.Mutex
	entries []*activityEntry
}

func (a *App) newActivity(category, item, name string) string {
	a.activity.mu.Lock()
	defer a.activity.mu.Unlock()
	// Retain recent completed entries; never discard active work.
	if len(a.activity.entries) >= 1000 {
		for i, v := range a.activity.entries {
			if v.State == "complete" || v.State == "error" || v.Category == "playback" {
				a.activity.entries = append(a.activity.entries[:i], a.activity.entries[i+1:]...)
				break
			}
		}
	}
	key := id()
	now := time.Now()
	a.activity.entries = append(a.activity.entries, &activityEntry{ID: key, Category: category, ItemID: item, Name: name, State: "waiting", Started: now, Updated: now})
	return key
}
func (a *App) changeActivity(key string, change func(*activityEntry)) {
	a.activity.mu.Lock()
	defer a.activity.mu.Unlock()
	for _, v := range a.activity.entries {
		if v.ID == key {
			change(v)
			v.Updated = time.Now()
			return
		}
	}
}
func (a *App) finishActivity(key string, err error) {
	a.changeActivity(key, func(v *activityEntry) {
		v.State = "complete"
		if err != nil {
			v.State = "error"
			v.Error = err.Error()
		}
	})
}
func (a *App) activitySnapshot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("category") == "proxy" {
		a.proxyDebugLogs(w, r)
		return
	}
	if r.Method == "DELETE" {
		a.activity.mu.Lock()
		for _, v := range a.activity.entries {
			v.Hidden = true
		}
		a.activity.mu.Unlock()
		respond(w, M{"ok": true})
		return
	}
	if r.Method != "GET" {
		fail(w, 405, "GET required")
		return
	}
	a.activity.mu.Lock()
	defer a.activity.mu.Unlock()
	entries := make([]activityEntry, 0, len(a.activity.entries))
	waiting, running := 0, 0
	online := map[string]bool{}
	for i := len(a.activity.entries) - 1; i >= 0; i-- {
		v := a.activity.entries[i]
		if v.Hidden {
			continue
		}
		snapshot := *v
		snapshot.Online = v.Category == "playback" && v.State == "requested" && time.Since(v.Updated) < 90*time.Second
		entries = append(entries, snapshot)
		if snapshot.Online {
			online[v.UserID+":"+v.DeviceKey] = true
		}
		if v.Category == "probe" {
			if v.State == "waiting" {
				waiting++
			}
			if v.State == "running" {
				running++
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, M{"OnlinePlayback": len(online), "Entries": entries, "ProbeConcurrency": a.probeSettings().Concurrency, "ProbeRunning": running, "ProbeWaiting": waiting, "Retention": "最近1000条，服务重启清空"})
}
func (a *App) logPlayback(r *http.Request, u User, x Item) {
	if u.API || strings.EqualFold(q(r, "GoEmbyProbe"), "true") {
		return
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	// Caddy overwrites X-Forwarded-For; use its rightmost address only on the local proxy hop.
	peer := net.ParseIP(ip)
	if peer != nil && (peer.IsLoopback() || ip == "172.18.0.1") {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		candidate := strings.TrimSpace(parts[len(parts)-1])
		if net.ParseIP(candidate) != nil {
			ip = candidate
		}
	}
	device := u.Device
	client := r.UserAgent()
	auth := r.Header.Get("X-Emby-Authorization")
	if auth == "" {
		auth = r.Header.Get("Authorization")
	}
	for _, part := range strings.Split(auth, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, "\"")
		if k == "Device" {
			device = v + " (" + u.Device + ")"
		}
		if k == "Emby Client" || k == "Client" {
			client = v
		}
	}
	a.activity.mu.Lock()
	for i := len(a.activity.entries) - 1; i >= 0; i-- {
		v := a.activity.entries[i]
		if v.Category == "playback" && v.State == "requested" && v.Username == u.Name && v.ItemID == x.ID && v.DeviceKey == u.Device && v.IP == ip && time.Since(v.Updated) < 180*time.Second {
			v.Updated = time.Now()
			a.activity.mu.Unlock()
			return
		}
	}
	if len(a.activity.entries) >= 1000 {
		for i, v := range a.activity.entries {
			if v.Category == "playback" || v.State == "complete" || v.State == "error" {
				a.activity.entries = append(a.activity.entries[:i], a.activity.entries[i+1:]...)
				break
			}
		}
	}
	now := time.Now()
	a.activity.entries = append(a.activity.entries, &activityEntry{ID: id(), Category: "playback", ItemID: x.ID, Name: x.Name, State: "requested", UserID: u.ID, Username: u.Name, IP: ip, Device: device, DeviceKey: u.Device, Client: client, Started: now, Updated: now})
	a.activity.mu.Unlock()
}

func (a *App) updatePlaybackActivity(u User, item string, stopped bool) {
	a.activity.mu.Lock()
	defer a.activity.mu.Unlock()
	for _, v := range a.activity.entries {
		if v.Category == "playback" && v.Username == u.Name && v.DeviceKey == u.Device && v.ItemID == item && v.State == "requested" {
			v.Updated = time.Now()
			if stopped {
				v.State = "complete"
			}
		}
	}
}
