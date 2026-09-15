package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go-emby/internal/licensesdk"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var assets embed.FS

type M = map[string]any
type App struct {
	scanControlMu sync.Mutex
	scanResume    chan struct{}
	pages         pageCache
	cursorSecret  string
	proxyDebug    proxyDebugState
	activity      activityState
	probes        probeState
	db            *Database
	write         sync.Mutex
	libraryConfig sync.Mutex
	scan          sync.RWMutex
	jobs          libraryJobQueue
	playing       sync.Mutex
	loginMu       sync.Mutex
	attempts      map[string][]time.Time
	serverID      string
}
type User struct {
	ID     string
	Name   string
	Hash   string
	Admin  bool
	First  bool
	Max    int
	API    bool
	Device string
}

func id() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func must(e error) {
	if e != nil {
		log.Fatal(e)
	}
}
func (a *App) init() {
	_, e := a.db.Exec(`CREATE TABLE IF NOT EXISTS settings(k TEXT PRIMARY KEY,v TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,name TEXT UNIQUE NOT NULL ,hash TEXT NOT NULL,admin BIGINT NOT NULL DEFAULT 0,first_admin BIGINT NOT NULL DEFAULT 0,max_devices BIGINT NOT NULL DEFAULT 2);
 CREATE TABLE IF NOT EXISTS tokens(hash TEXT PRIMARY KEY,user_id TEXT REFERENCES users(id) ON DELETE CASCADE,device TEXT NOT NULL,expires BIGINT NOT NULL);
 CREATE TABLE IF NOT EXISTS api_keys(id TEXT PRIMARY KEY,name TEXT NOT NULL,hash TEXT UNIQUE NOT NULL,created BIGINT NOT NULL);
 CREATE TABLE IF NOT EXISTS libraries(id TEXT PRIMARY KEY,name TEXT NOT NULL,path TEXT UNIQUE NOT NULL,kind TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'idle',scanned BIGINT NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',count BIGINT NOT NULL DEFAULT 0,duration DOUBLE PRECISION NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS items(id TEXT PRIMARY KEY,lib TEXT NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,parent TEXT NOT NULL,name TEXT NOT NULL,kind TEXT NOT NULL,path TEXT UNIQUE NOT NULL,url TEXT NOT NULL DEFAULT '',overview TEXT NOT NULL DEFAULT '',poster TEXT NOT NULL DEFAULT '',year BIGINT NOT NULL DEFAULT 0,season BIGINT NOT NULL DEFAULT 0,episode BIGINT NOT NULL DEFAULT 0,mtime BIGINT NOT NULL DEFAULT 0,size BIGINT NOT NULL DEFAULT 0,seen TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS items_lib_name ON items(lib,name,id);
 CREATE INDEX IF NOT EXISTS items_parent_name ON items(parent,name,id);
 CREATE INDEX IF NOT EXISTS items_kind ON items(kind);
 CREATE INDEX IF NOT EXISTS items_seen ON items(lib,seen);
 CREATE TABLE IF NOT EXISTS plays(user_id TEXT REFERENCES users(id) ON DELETE CASCADE,device TEXT NOT NULL,item TEXT NOT NULL,updated BIGINT NOT NULL,PRIMARY KEY(user_id,device));
 CREATE TABLE IF NOT EXISTS userdata(user_id TEXT REFERENCES users(id) ON DELETE CASCADE,item TEXT REFERENCES items(id) ON DELETE CASCADE,position BIGINT NOT NULL DEFAULT 0,played BIGINT NOT NULL DEFAULT 0,PRIMARY KEY(user_id,item));`)
	must(e)
	must(a.extraSchema())
	must(a.browseSchema())
	must(a.postgresSchema())
	must(a.versionsSchema())
	must(a.initialsSchema())
	mustExecLogin := `CREATE TABLE IF NOT EXISTS user_logins(user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, last_login BIGINT NOT NULL)`
	_, loginErr := a.db.Exec(mustExecLogin)
	must(loginErr)
	must(a.db.QueryRow("SELECT v FROM settings WHERE k='image_secret'").Scan(&a.cursorSecret))
	if os.Getenv("SCHEMA_ONLY") == "1" {
		return
	}
	a.db.Exec("UPDATE libraries SET status='interrupted',error='服务重启，请重新扫描' WHERE status='scanning'")
	var n int
	must(a.db.QueryRow("SELECT count(*) FROM users").Scan(&n))
	if n == 0 {
		p := os.Getenv("ADMIN_PASSWORD")
		if len(p) < 12 {
			log.Fatal("ADMIN_PASSWORD must contain at least 12 characters")
		}
		h, e := bcrypt.GenerateFromPassword([]byte(p), 12)
		must(e)
		_, e = a.db.Exec("INSERT INTO users VALUES(?,?,?,?,?,?)", id(), "admin", string(h), 1, 1, 5)
		must(e)
	}
	if k := os.Getenv("BOOTSTRAP_API_KEY"); k != "" {
		var initialized string
		e := a.db.QueryRow("SELECT v FROM settings WHERE k='key_initialized'").Scan(&initialized)
		if e == sql.ErrNoRows {
			_, e = a.db.Exec("INSERT INTO api_keys VALUES(?,?,?,?) ON CONFLICT DO NOTHING", id(), "NanShare", digest(k), time.Now().Unix())
			must(e)
			_, e = a.db.Exec("INSERT INTO settings VALUES('key_initialized','1')")
			must(e)
		}
	}
	a.db.Exec("INSERT INTO settings VALUES('server_id',?) ON CONFLICT DO NOTHING", id())
	must(a.db.QueryRow("SELECT v FROM settings WHERE k='server_id'").Scan(&a.serverID))
}
func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, s string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(M{"error": s, "Message": s})
}
func body(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if e := json.NewDecoder(r.Body).Decode(v); e != nil {
		fail(w, 400, "无效 JSON")
		return false
	}
	return true
}
func q(r *http.Request, k string) string {
	for key, v := range r.URL.Query() {
		if strings.EqualFold(key, k) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

var authField = regexp.MustCompile(`(?i)(Token|DeviceId|Client|Device|Version)\s*=\s*"([^"]*)"`)

func field(r *http.Request, k string) string {
	for _, value := range []string{r.Header.Get("X-Emby-Authorization"), r.Header.Get("Authorization"), q(r, "X-Emby-Authorization")} {
		for _, v := range authField.FindAllStringSubmatch(value, -1) {
			if strings.EqualFold(v[1], k) {
				return v[2]
			}
		}
	}
	return ""
}
func token(r *http.Request) string {
	for _, s := range []string{r.Header.Get("X-Emby-Token"), r.Header.Get("X-MediaBrowser-Token"), q(r, "api_key"), q(r, "X-Emby-Token"), q(r, "X-MediaBrowser-Token"), field(r, "Token"), r.Header.Get("X-Emby-Api-Key"), q(r, "X-Emby-Api-Key")} {
		if s != "" {
			return s
		}
	}
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}
func device(r *http.Request) string {
	for _, s := range []string{field(r, "DeviceId"), q(r, "DeviceId"), q(r, "X-Emby-Device-Id"), r.Header.Get("X-Emby-Device-Id")} {
		if s != "" {
			return s
		}
	}
	return ""
}
func (a *App) auth(r *http.Request) (User, error) {
	var u User
	t := token(r)
	if t == "" {
		return u, errors.New("missing token")
	}
	e := a.db.QueryRow(`SELECT u.id,u.name,u.hash,u.admin,u.first_admin,u.max_devices,t.device FROM users u JOIN tokens t ON t.user_id=u.id WHERE t.hash=? AND t.expires>?`, digest(t), time.Now().Unix()).Scan(&u.ID, &u.Name, &u.Hash, &u.Admin, &u.First, &u.Max, &u.Device)
	if e == nil {
		return u, nil
	}
	var key string
	e = a.db.QueryRow("SELECT id FROM api_keys WHERE hash=?", digest(t)).Scan(&key)
	if e == nil {
		u.API = true
		return u, nil
	}
	return u, e
}
func (a *App) userDTO(u User) M {
	return M{"Id": u.ID, "Name": u.Name, "ServerId": a.serverID, "HasPassword": true, "HasConfiguredPassword": true, "HasConfiguredEasyPassword": false,
		"Policy": M{"IsAdministrator": u.Admin, "IsDisabled": false, "IsHidden": true, "IsHiddenRemotely": true, "IsHiddenFromUnusedDevices": true,
			"EnableMediaPlayback": a.canPlay(u.ID), "EnableVideoPlaybackTranscoding": false, "EnableAudioPlaybackTranscoding": false, "EnablePlaybackRemuxing": false,
			"EnableContentDeletion": false, "EnableContentDownloading": false, "EnableSubtitleDownloading": false, "EnableSubtitleManagement": false, "EnableSyncTranscoding": false,
			"EnableAllFolders": true, "EnableAllDevices": true, "EnableAllChannels": false, "EnableRemoteAccess": true,
			"EnableRemoteControlOfOtherUsers": false, "EnableSharedDeviceControl": false, "EnableLiveTvAccess": false, "EnableLiveTvManagement": false,
			"EnablePublicSharing": false, "EnableMediaConversion": false, "EnableUserPreferenceAccess": false,
			"BlockedTags": []string{}, "IncludeTags": []string{}, "AccessSchedules": []M{}, "BlockUnratedItems": []string{}, "EnabledFolders": []string{}, "EnabledDevices": []string{}, "EnabledChannels": []string{}, "ExcludedSubFolders": []string{}, "EnableContentDeletionFromFolders": []string{},
			"RemoteClientBitrateLimit": 0, "InvalidLoginAttemptCount": 0, "SimultaneousStreamLimit": u.Max},
		"Configuration": M{"OrderedViews": a.orderedViews(), "DisplayMissingEpisodes": false, "PlayDefaultAudioTrack": true, "SubtitleMode": "Smart",
			"LatestItemsExcludes": []string{}, "MyMediaExcludes": []string{}, "HidePlayedInLatest": false, "HidePlayedInMoreLikeThis": false, "HidePlayedInSuggestions": false,
			"RememberAudioSelections": false, "RememberSubtitleSelections": false, "EnableNextEpisodeAutoPlay": true, "ResumeRewindSeconds": 0, "IntroSkipMode": "ShowButton", "EnableLocalPassword": false},
		"FirstAdmin": u.First, "MaxDevices": u.Max}
}

// Some clients join an /emby base URL with an already prefixed API path.
func embyPath(p string) string {
	// Normalize duplicate API separators without redirecting POST requests.
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	p = strings.TrimRight(p, "/")
	for strings.HasPrefix(strings.ToLower(p), "/emby/") {
		p = p[5:]
	}
	return p
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "POST required")
		return
	}
	a.loginMu.Lock()
	ip := strings.Split(r.RemoteAddr, ":")[0]
	ts := a.attempts[ip]
	recent := ts[:0]
	for _, t := range ts {
		if time.Since(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 15 {
		a.loginMu.Unlock()
		a.authWarning(r, "", "登录过于频繁")
		fail(w, 429, "登录过于频繁，请稍后再试")
		return
	}
	a.attempts[ip] = append(recent, time.Now())
	a.loginMu.Unlock()
	var b loginCredentials
	if !loginBody(w, r, &b) {
		return
	}
	var u User
	e := a.db.QueryRow("SELECT id,name,hash,admin,first_admin,max_devices FROM users WHERE lower(name)=lower(?)", b.Username).Scan(&u.ID, &u.Name, &u.Hash, &u.Admin, &u.First, &u.Max)
	if e != nil || bcrypt.CompareHashAndPassword([]byte(u.Hash), []byte(b.Pw)) != nil {
		reason := "密码错误"
		if e == sql.ErrNoRows {
			reason = "用户不存在"
		} else if e != nil {
			reason = "用户查询失败"
		}
		a.authWarning(r, b.Username, reason)
		fail(w, 401, "用户名或密码错误")
		return
	}
	d := device(r)
	if d == "" {
		d = id()
	}
	t := id() + id()
	_, e = a.db.Exec("INSERT INTO tokens VALUES(?,?,?,?)", digest(t), u.ID, d, time.Now().Add(30*24*time.Hour).Unix())
	if e != nil {
		fail(w, 500, "登录写入失败")
		return
	}
	a.db.Exec("INSERT INTO user_logins VALUES(?,?) ON CONFLICT(user_id) DO UPDATE SET last_login=excluded.last_login", u.ID, time.Now().Unix())
	a.loginSuccess(r, u, d)
	respond(w, M{"User": a.userDTO(u), "AccessToken": t, "ServerId": a.serverID, "SessionInfo": a.loginSession(r, u, d)})
}
func (a *App) serverInfo() M {
	return M{"Id": a.serverID, "ServerName": a.displayName(), "Version": "4.8.0.80", "OperatingSystem": "Linux", "ProductName": "Go Emby STRM", "LocalAddress": os.Getenv("PUBLIC_URL"), "WanAddress": os.Getenv("PUBLIC_URL"), "LocalAddresses": []string{os.Getenv("PUBLIC_URL")}, "RemoteAddresses": []string{os.Getenv("PUBLIC_URL")}, "StartupWizardCompleted": true, "SupportsLibraryMonitor": false, "HasUpdateAvailable": false}
}
func (a *App) serve(w http.ResponseWriter, r *http.Request) {
	if done := a.beginProxyDebug(&w, r); done != nil {
		defer done()
	}
	if isVideoRequest(r.URL.Path) || strings.Contains(strings.ToLower(r.URL.Path), "/playbackinfo") {
		tracked := &errorResponse{ResponseWriter: w, status: 200}
		w = tracked
		started := time.Now()
		defer func() {
			if tracked.status >= 400 {
				a.recordError(r, "播放请求错误", fmt.Sprintf("%s %s · HTTP %d · 耗时 %s · %s", r.Method, r.URL.Path, tracked.status, time.Since(started), tracked.detail.String()))
			}
		}()
	}
	if client := strings.ToLower(r.UserAgent()); strings.Contains(client, "lenna") || strings.Contains(client, "infuse") || strings.Contains(client, "senplayer") {
		tracked := &clientResponse{ResponseWriter: w, status: 200}
		w = tracked
		started := time.Now()
		defer func() {
			log.Printf("client request ua=%q method=%s path=%q status=%d duration=%s", r.UserAgent(), r.Method, r.URL.Path, tracked.status, time.Since(started))
		}()
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Header.Get("Origin") == "emby-local://app" {
		w.Header().Set("Access-Control-Allow-Origin", "emby-local://app")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	w.Header().Add("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, DELETE, PATCH, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Accept, Accept-Language, Content-Type, Authorization, Cache-Control, Origin, OriginToken, Pragma, Range, If-Modified-Since, If-None-Match, X-Emby-Language, X-Emby-Authorization, X-Emby-Token, X-MediaBrowser-Token, X-Emby-Client, X-Emby-Client-Version, X-Emby-Device-Id, X-Emby-Device-Name, X-Emby-Api-Key")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	if a.filesRoute(w, r) {
		return
	}
	p := embyPath(r.URL.Path)
	l := strings.ToLower(p)
	if strings.Contains(l, "/download") || strings.Contains(l, "/subtitles") || strings.HasPrefix(l, "/sync") {
		fail(w, 403, "下载媒体与字幕已禁用")
		return
	}
	if l == "/health" {
		var v int
		if e := a.db.QueryRow("SELECT 1").Scan(&v); e != nil {
			fail(w, 503, "database unavailable")
			return
		}
		respond(w, M{"status": "ok"})
		return
	}
	if l == "/system/ping" {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("Emby Server"))
		return
	}
	if l == "/system/info/public" {
		respond(w, a.serverInfo())
		return
	}
	if l == "/users/authenticatebyname" {
		a.login(w, r)
		return
	}
	if strings.HasPrefix(l, "/web/strings/") {
		if r.Method != "GET" && r.Method != "HEAD" {
			fail(w, 405, "GET or HEAD required")
			return
		}
		name := strings.TrimPrefix(l, "/web/strings/")
		if strings.Contains(name, "/") || !strings.HasSuffix(name, ".json") {
			fail(w, 404, "Resource not found")
			return
		}
		entries, _ := assets.ReadDir("web/strings")
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(entry.Name(), name) {
				b, err := assets.ReadFile("web/strings/" + entry.Name())
				if err != nil {
					fail(w, 404, "Resource not found")
					return
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				if r.Method == "GET" {
					w.Write(b)
				}
				return
			}
		}
		fail(w, 404, "Resource not found")
		return
	}
	if l == "/web/manifest.json" && (r.Method == "GET" || r.Method == "HEAD") {
		respond(w, M{"name": a.displayName(), "short_name": "Emby", "start_url": "index.html", "display": "standalone", "icons": []M{}})
		return
	}
	if l == "/users/public" {
		respond(w, []any{})
		return
	}
	if p == "" || l == "/web" || l == "/web/index.html" || l == "/web/dashboard.html" || l == "/index.html" {
		b, _ := assets.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
		return
	}
	if l == "/auth/playback" {
		a.checkPlayback(w, r)
		return
	}
	if a.taggedImage(w, r, p) {
		return
	}
	u, e := a.auth(r)
	if e != nil {
		a.authWarning(r, "", "令牌缺失、无效或已过期")
		fail(w, 401, "请先登录")
		return
	}
	if a.imageUploadRoute(w, r, u, p) {
		return
	}
	if a.browseRoute(w, r, u, p) {
		return
	}
	if strings.HasPrefix(l, "/admin/") {
		if !u.Admin || u.API {
			fail(w, 403, "需要管理员账号")
			return
		}
		a.admin(w, r, u, l)
		return
	}
	if l == "/system/endpoint" {
		serveEndpoint(w, r)
		return
	}
	if l == "/system/info" {
		respond(w, a.serverInfo())
		return
	}
	if l == "/auth/keys" {
		if !u.Admin {
			fail(w, 403, "无权限")
			return
		}
		a.admin(w, r, u, "/admin/keys")
		return
	}
	if l == "/users/me" {
		if u.API {
			fail(w, 403, "API key has no user")
			return
		}
		respond(w, a.userDTO(u))
		return
	}
	if l == "/users" {
		if !u.Admin && !u.API {
			fail(w, 403, "无权限")
			return
		}
		respond(w, a.users())
		return
	}
	if l == "/sessions/logout" {
		a.db.Exec("DELETE FROM tokens WHERE hash=?", digest(token(r)))
		w.WriteHeader(204)
		return
	}
	if l == "/library/virtualfolders" {
		respond(w, a.libraries())
		return
	}
	if l == "/library/refresh" && r.Method == "POST" {
		if !u.Admin && !u.API {
			fail(w, 403, "无权限")
			return
		}
		go a.scanAll()
		w.WriteHeader(204)
		return
	}
	if l == "/items/counts" {
		m := M{}
		for k, v := range map[string]string{"MovieCount": "Movie", "EpisodeCount": "Episode", "SeriesCount": "Series"} {
			var n int
			a.db.QueryRow("SELECT count(*) FROM items WHERE kind=?", v).Scan(&n)
			m[k] = n
		}
		respond(w, m)
		return
	}
	if l == "/sessions" {
		respond(w, []any{})
		return
	}
	if strings.HasPrefix(l, "/sessions/playing") {
		if u.API {
			fail(w, 403, "播放需要用户令牌")
			return
		}
		var b struct {
			ItemId        string
			PositionTicks *clientTicks
			RunTimeTicks  clientTicks
			MediaSourceId string
		}
		if !body(w, r, &b) {
			return
		}
		if b.ItemId == "" {
			b.ItemId = b.MediaSourceId
		}
		if b.ItemId == "" {
			a.db.QueryRow("SELECT item FROM plays WHERE user_id=? AND device=?", u.ID, u.Device).Scan(&b.ItemId)
		}
		if l == "/sessions/playing/stopped" {
			a.db.Exec("DELETE FROM plays WHERE user_id=? AND device=?", u.ID, u.Device)
		} else if e := a.reserve(u, b.ItemId); e != nil {
			fail(w, 403, e.Error())
			return
		}
		if b.ItemId != "" {
			if x, err := a.item(b.ItemId); err == nil && l != "/sessions/playing/stopped" {
				a.logPlayback(r, u, x)
				if l == "/sessions/playing" {
					a.scheduleNext(x, r)
				}
			}
			if b.PositionTicks != nil {
				a.playbackProgress(u, b.ItemId, int64(*b.PositionTicks), int64(b.RunTimeTicks))
			}
			a.updatePlaybackActivity(u, b.ItemId, l == "/sessions/playing/stopped")
			if b.PositionTicks != nil {
				a.db.Exec("INSERT INTO userdata(user_id,item,position) VALUES(?,?,?) ON CONFLICT(user_id,item) DO UPDATE SET position=excluded.position", u.ID, b.ItemId, max(int64(*b.PositionTicks), 0))
			}
			a.db.Exec("INSERT INTO resume_activity VALUES(?,?,?) ON CONFLICT(user_id,item) DO UPDATE SET updated=excluded.updated", u.ID, b.ItemId, time.Now().UnixNano())
		}
		w.WriteHeader(204)
		return
	}
	if strings.HasPrefix(l, "/users/") {
		s := strings.Split(p, "/")
		if len(s) >= 3 {
			uid := s[2]
			if !u.API && !u.Admin && uid != u.ID {
				fail(w, 403, "无权限")
				return
			}
			if len(s) == 3 {
				var x User
				e := a.db.QueryRow("SELECT id,name,hash,admin,first_admin,max_devices FROM users WHERE id=?", uid).Scan(&x.ID, &x.Name, &x.Hash, &x.Admin, &x.First, &x.Max)
				if e != nil {
					fail(w, 404, "用户不存在")
					return
				}
				respond(w, a.userDTO(x))
				return
			}
			switch strings.ToLower(s[3]) {
			case "password":
				a.password(w, r, u, uid)
				return
			case "views":
				respond(w, M{"Items": a.libraryDTOs(), "TotalRecordCount": len(a.libraries())})
				return
			case "items":
				if len(s) > 4 && strings.ToLower(s[4]) != "latest" && strings.ToLower(s[4]) != "resume" {
					a.single(w, r, u, s[4])
					return
				}
				a.items(w, r, u, strings.HasSuffix(l, "/latest"))
				return
			}
		}
	}
	if l == "/items" || l == "/items/resume" {
		a.items(w, r, u, false)
		return
	}
	if strings.HasPrefix(l, "/items/") {
		s := strings.Split(p, "/")
		if len(s) == 3 {
			a.single(w, r, u, s[2])
			return
		}
		if len(s) > 3 {
			switch strings.ToLower(s[3]) {
			case "refreshmediainfo":
				a.probeMedia(w, r, u, s[2])
				return
			case "similar":
				a.similar(w, r, u, s[2])
				return
			case "playbackinfo":
				a.playback(w, r, u, s[2])
				return
			case "images":
				a.poster(w, r, s[2])
				return
			case "refresh":
				if !u.API && !u.Admin {
					fail(w, 403, "无权限")
					return
				}
				go a.scanAll()
				w.WriteHeader(204)
				return
			}
		}
	}
	if strings.HasPrefix(l, "/videos/") {
		s := strings.Split(p, "/")
		if len(s) >= 4 && (strings.HasPrefix(strings.ToLower(s[3]), "stream") || strings.EqualFold(s[3], "original")) {
			a.stream(w, r, u, s[2])
			return
		}
		fail(w, 400, "不支持转码或 HLS，请选择直接播放")
		return
	}
	if strings.HasPrefix(l, "/shows/") {
		s := strings.Split(p, "/")
		if len(s) > 3 {
			r.URL.RawQuery = r.URL.Query().Encode() + "&ParentId=" + url.QueryEscape(s[2])
			a.items(w, r, u, false)
			return
		}
	}
	fail(w, 404, "未实现此接口")
}
func (a *App) users() []M {
	out := []M{}
	rows, e := a.db.Query("SELECT id,name,hash,admin,first_admin,max_devices FROM users ORDER BY first_admin DESC,name")
	if e != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var u User
		if rows.Scan(&u.ID, &u.Name, &u.Hash, &u.Admin, &u.First, &u.Max) == nil {
			dto := a.userDTO(u)
			var last int64
			a.db.QueryRow("SELECT last_login FROM user_logins WHERE user_id=?", u.ID).Scan(&last)
			dto["LastLoginDate"] = last
			out = append(out, dto)
		}
	}
	return out
}
func (a *App) password(w http.ResponseWriter, r *http.Request, u User, uid string) {
	if r.Method != "POST" {
		fail(w, 405, "POST required")
		return
	}
	if u.API {
		fail(w, 403, "无权限")
		return
	}
	var b struct{ CurrentPw, NewPw string }
	if !body(w, r, &b) {
		return
	}
	if len(b.NewPw) < 10 || len(b.NewPw) > 72 {
		fail(w, 400, "密码需要 10–72 字节")
		return
	}
	if uid == u.ID && bcrypt.CompareHashAndPassword([]byte(u.Hash), []byte(b.CurrentPw)) != nil {
		fail(w, 403, "当前密码不正确")
		return
	}
	h, e := bcrypt.GenerateFromPassword([]byte(b.NewPw), 12)
	if e != nil {
		fail(w, 500, "密码生成失败")
		return
	}
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, "数据库繁忙")
		return
	}
	defer tx.Rollback()
	_, e = tx.Exec("UPDATE users SET hash=? WHERE id=?", string(h), uid)
	if e == nil {
		_, e = tx.Exec("DELETE FROM tokens WHERE user_id=?", uid)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		fail(w, 500, "保存失败")
		return
	}
	respond(w, M{"ok": true})
}
func (a *App) admin(w http.ResponseWriter, r *http.Request, u User, p string) {
	if r.Method != "GET" {
		a.write.Lock()
		defer a.write.Unlock()
	}
	switch p {
	case "/admin/media-info/restore":
		a.restoreMediaInfo(w, r)
	case "/admin/media-item":
		a.manageMediaItem(w, r)
	case "/admin/enhancements":
		a.enhancementSettings(w, r)
	case "/admin/media-info":
		a.mediaSettings(w, r)
	case "/admin/directories", "/admin/order", "/admin/cover":
		a.extraAdmin(w, r, p)
	case "/admin/library-folders":
		a.libraryFolders(w, r)
	case "/admin/libraries":
		if r.Method == "GET" {
			respond(w, a.libraries())
			return
		}
		var b struct {
			ID, Name, Path, Kind string
			Paths                []string
		}
		if !body(w, r, &b) {
			return
		}
		if r.Method == "DELETE" {
			if !a.scan.TryLock() {
				fail(w, 409, "扫描中，请完成后再删除")
				return
			}
			defer a.scan.Unlock()
			_, e := a.db.Exec("DELETE FROM libraries WHERE id=?", b.ID)
			if e != nil {
				fail(w, 500, "删除失败")
				return
			}
			if e := a.cleanupMedia(); e != nil {
				fail(w, 500, "索引已删除，媒体信息清理失败")
				return
			}
			respond(w, M{"ok": true})
			return
		}
		if r.Method != "POST" {
			fail(w, 405, "method")
			return
		}
		a.createLibrary(w, b.Name, b.Kind, b.Path, b.Paths)

	case "/admin/scan-control":
		a.scanControlAPI(w, r)
	case "/admin/scan-all":
		if r.Method != "POST" {
			fail(w, 405, "POST required")
			return
		}
		for _, lib := range a.libraries() {
			go a.scanLibraryMode(lib["Id"].(string), true)
		}
		respond(w, M{"queued": true})
	case "/admin/scan-schedule":
		a.scanScheduleAPI(w, r)
	case "/admin/scan":
		if r.Method != "POST" {
			fail(w, 405, "method")
			return
		}
		var b struct{ ID, Mode string }
		if !body(w, r, &b) {
			return
		}
		var exists int
		if a.db.QueryRow("SELECT 1 FROM libraries WHERE id=?", b.ID).Scan(&exists) != nil {
			fail(w, 404, "媒体库不存在")
			return
		}
		if b.Mode != "" && b.Mode != "scan" && b.Mode != "update" {
			fail(w, 400, "无效扫描模式")
			return
		}
		go a.scanLibraryMode(b.ID, b.Mode == "update")
		respond(w, M{"queued": true})
	case "/admin/user-identity":
		a.userIdentity(w, r)
	case "/admin/users":
		if r.Method == "GET" {
			respond(w, a.users())
			return
		}
		var b struct {
			ID, Name, Password string
			MaxDevices         int
			Admin              bool
			AllowPlayback      *bool
		}
		if !body(w, r, &b) {
			return
		}
		if r.Method == "DELETE" {
			var first bool
			e := a.db.QueryRow("SELECT first_admin FROM users WHERE id=?", b.ID).Scan(&first)
			if e != nil {
				fail(w, 404, "用户不存在")
				return
			}
			if first || b.ID == u.ID {
				fail(w, 403, "不能删除首位管理员或当前账号")
				return
			}
			_, e = a.db.Exec("DELETE FROM users WHERE id=?", b.ID)
			if e != nil {
				fail(w, 500, "删除失败")
				return
			}
			respond(w, M{"ok": true})
			return
		}
		if b.MaxDevices < 1 || b.MaxDevices > 100 {
			fail(w, 400, "设备数量范围 1–100")
			return
		}
		if r.Method == "PUT" {
			var first bool
			if a.db.QueryRow("SELECT first_admin FROM users WHERE id=?", b.ID).Scan(&first) != nil {
				fail(w, 404, "用户不存在")
				return
			}
			if first {
				b.Admin = true
			}
			_, e := a.db.Exec("UPDATE users SET max_devices=?,admin=? WHERE id=?", b.MaxDevices, b.Admin, b.ID)
			if e != nil {
				fail(w, 500, "保存失败")
				return
			}
			if b.AllowPlayback != nil {
				if _, e := a.db.Exec("INSERT INTO user_playback(user_id,allowed) VALUES(?,?) ON CONFLICT(user_id) DO UPDATE SET allowed=excluded.allowed", b.ID, *b.AllowPlayback); e != nil {
					fail(w, 500, "播放权限保存失败")
					return
				}
				if !*b.AllowPlayback {
					a.db.Exec("DELETE FROM plays WHERE user_id=?", b.ID)
				}
			}
			respond(w, M{"ok": true})
			return
		}
		if len(b.Password) < 10 || len(b.Password) > 72 || strings.TrimSpace(b.Name) == "" {
			fail(w, 400, "账号不能为空，密码需要 10–72 字节")
			return
		}
		h, e := bcrypt.GenerateFromPassword([]byte(b.Password), 12)
		if e != nil {
			fail(w, 500, "密码生成失败")
			return
		}
		uid := id()
		tx, e := a.db.Begin()
		if e != nil {
			fail(w, 500, "保存失败")
			return
		}
		defer tx.Rollback()
		_, e = tx.Exec("INSERT INTO users VALUES(?,?,?,?,?,?)", uid, b.Name, string(h), b.Admin, false, b.MaxDevices)
		if e == nil && b.AllowPlayback != nil {
			_, e = tx.Exec("INSERT INTO user_playback(user_id,allowed) VALUES(?,?)", uid, *b.AllowPlayback)
		}
		if e == nil {
			e = tx.Commit()
		}
		if e != nil {
			fail(w, 409, "用户名重复或保存失败")
			return
		}
		respond(w, M{"ok": true})
	case "/admin/keys":
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == "GET" {
			rows, e := a.db.Query("SELECT id,name,created FROM api_keys ORDER BY created")
			if e != nil {
				fail(w, 500, "读取失败")
				return
			}
			defer rows.Close()
			out := []M{}
			for rows.Next() {
				var i, n string
				var t int64
				rows.Scan(&i, &n, &t)
				out = append(out, M{"Id": i, "Name": n, "Created": t, "MaskedKey": a.maskedKey(i)})
			}
			respond(w, out)
			return
		}
		var b struct{ ID, Name string }
		if !body(w, r, &b) {
			return
		}
		if r.Method == "DELETE" {
			_, e := a.db.Exec("DELETE FROM api_keys WHERE id=?", b.ID)
			if e != nil {
				fail(w, 500, "删除失败")
				return
			}
			respond(w, M{"ok": true})
			return
		}
		if r.Method != "POST" {
			fail(w, 405, "POST required")
			return
		}
		if strings.TrimSpace(b.Name) == "" {
			fail(w, 400, "请输入名称")
			return
		}
		t := id() + id()
		keyID := id()
		tx, e := a.db.Begin()
		if e != nil {
			fail(w, 500, "写入失败")
			return
		}
		defer tx.Rollback()
		_, e = tx.Exec("INSERT INTO api_keys VALUES(?,?,?,?)", keyID, b.Name, digest(t), time.Now().Unix())
		if e == nil {
			_, e = tx.Exec("INSERT INTO settings(k,v) VALUES(?,?)", "key-mask:"+keyID, t[:6]+"••••••••••••"+t[len(t)-4:])
		}
		w.Header().Set("Cache-Control", "no-store")
		if e != nil {
			fail(w, 500, "写入失败")
			return
		}
		if e = tx.Commit(); e != nil {
			fail(w, 500, "写入失败")
			return
		}
		respond(w, M{"Token": t})
	case "/admin/backup":
		if r.Method != "POST" {
			fail(w, 405, "method")
			return
		}
		path, e := a.backup()
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
		respond(w, M{"Path": path})
	case "/admin/logs":
		a.activitySnapshot(w, r)
	case "/admin/status":
		var n int
		a.db.QueryRow("SELECT count(*) FROM items").Scan(&n)
		respond(w, M{"Items": n, "Database": "SQLite WAL / synchronous FULL", "Transcoding": false, "Playback": "302 redirect only", "DeviceLeaseSeconds": 180})
	default:
		fail(w, 404, "not found")
	}
}
func (a *App) libraries() []M {
	out := []M{}
	rows, e := a.db.Query("SELECT id,name,path,kind,status,scanned,error,count,duration FROM libraries ORDER BY COALESCE((SELECT position FROM library_order WHERE library_id=libraries.id),2147483647),name")
	if e != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var i, n, p, k, s, er string
		var t, c int64
		var d float64
		rows.Scan(&i, &n, &p, &k, &s, &t, &er, &c, &d)
		out = append(out, M{"Id": i, "ItemId": i, "Name": n, "Path": p, "Locations": []string{p}, "CollectionType": k, "Status": s, "Scanned": t, "Error": er, "Count": c, "Duration": d})
	}
	rows.Close()
	for _, lib := range out {
		lib["Locations"] = a.libraryPaths(lib["Id"].(string), lib["Path"].(string))
	}
	return out
}
func (a *App) libraryDTOs() []M {
	out := []M{}
	for _, v := range a.libraries() {
		out = append(out, M{"Id": v["Id"], "Name": v["Name"], "Type": "CollectionFolder", "IsFolder": true, "CollectionType": v["CollectionType"], "SortName": v["Name"], "ForcedSortName": v["Name"], "ExternalUrls": []M{}, "ProviderIds": M{}, "RemoteTrailers": []M{}, "Taglines": []string{}, "LockedFields": []string{}, "LockData": false, "CanDelete": false, "CanDownload": false, "PresentationUniqueKey": v["Id"], "ServerId": a.serverID, "ImageTags": a.libraryImageTags(v["Id"].(string)), "ParentId": "root", "LocationType": "Virtual", "ChildCount": v["Count"], "RecursiveItemCount": v["Count"], "PrimaryImageAspectRatio": 2.0 / 3, "BackdropImageTags": []string{}, "UserData": M{"Played": false, "IsFavorite": false, "PlaybackPositionTicks": 0}, "DisplayPreferencesId": v["Id"]})
	}
	return out
}

type Item struct {
	ID, Lib, Parent, Name, Kind, Path, URL, Overview, Poster string
	Year, Season, Episode                                    int
	Mtime, Size                                              int64
}

const cols = "id,lib,parent,name,kind,path,url,overview,poster,year,season,episode,mtime,size"

func readItem(s interface{ Scan(...any) error }) (Item, error) {
	var x Item
	e := s.Scan(&x.ID, &x.Lib, &x.Parent, &x.Name, &x.Kind, &x.Path, &x.URL, &x.Overview, &x.Poster, &x.Year, &x.Season, &x.Episode, &x.Mtime, &x.Size)
	return x, e
}
func (a *App) item(i string) (Item, error) {
	return readItem(a.db.QueryRow("SELECT "+cols+" FROM items WHERE id=?", i))
}
func (a *App) dto(x Item) M {
	folder := x.Kind == "Series" || x.Kind == "Season"
	tags := M{}
	if x.Poster != "" {
		tags["Primary"] = digest(x.Poster + strconv.FormatInt(x.Mtime, 10))[:12]
	}
	m := M{"Id": x.ID, "ServerId": a.serverID, "ParentId": x.Parent, "Name": x.Name, "SortName": x.Name, "Type": x.Kind, "IsFolder": folder, "Path": x.Path, "Overview": x.Overview, "ProductionYear": x.Year, "IndexNumber": x.Episode, "ParentIndexNumber": x.Season, "ImageTags": tags, "BackdropImageTags": []string{}, "MediaType": "Video", "LocationType": "FileSystem", "UserData": M{"PlaybackPositionTicks": 0, "Played": false, "IsFavorite": false}, "CanDownload": false}
	if !folder {
		m["MediaSources"] = []M{a.source(x, "")}
	}
	a.enrich(x, m)
	if x.Kind == "Movie" {
		delete(m, "IndexNumber")
		delete(m, "ParentIndexNumber")
	}
	return m
}
func (a *App) source(x Item, t string) M {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(strings.Split(x.URL, "?")[0]), "."))
	if ext == "" || len(ext) > 5 {
		ext = "mkv"
	}
	return M{"Id": x.ID, "Name": x.Name, "Path": x.URL, "Protocol": "Http", "Type": "Default", "Container": ext, "IsRemote": true, "SupportsDirectPlay": true, "SupportsDirectStream": true, "SupportsTranscoding": false, "RequiresOpening": false, "RequiresClosing": false, "MediaStreams": []any{}, "DirectStreamUrl": "/emby/Videos/" + x.ID + "/stream." + ext + "?Static=true&api_key=" + url.QueryEscape(t), "AddApiKeyToDirectStreamUrl": false}
}
func (a *App) single(w http.ResponseWriter, r *http.Request, u User, i string) {
	if i == "root" || i == a.serverID {
		respond(w, a.rootDTO())
		return
	}
	if strings.HasPrefix(i, "person-") {
		a.personDetail(w, r, i)
		return
	}
	x, e := a.item(i)
	if e != nil {
		for _, v := range a.libraryDTOs() {
			if v["Id"] == i {
				respond(w, v)
				return
			}
		}
		fail(w, 404, "媒体不存在")
		return
	}
	// Respond with local/cached metadata first; network probing never blocks detail rendering.
	if cfg := a.probeSettings(); !u.API && cfg.Browse {
		defer func() {
			a.queueProbe(x, token(r), r.UserAgent(), false)
		}()
	}
	m := a.viewerDTO(x, r, u)
	if !u.API {
		var pos int64
		var played bool
		a.db.QueryRow("SELECT position,played FROM userdata WHERE user_id=? AND item=?", u.ID, i).Scan(&pos, &played)
		m["UserData"] = M{"PlaybackPositionTicks": pos, "Played": played, "IsFavorite": false}
	}
	respond(w, m)
	_ = http.NewResponseController(w).Flush()
}
func (a *App) items(w http.ResponseWriter, r *http.Request, u User, latest bool) {
	a.normalizeBrowseTypes(r, latest)
	where := "1=1"
	resume := strings.HasSuffix(strings.ToLower(r.URL.Path), "/resume") || strings.Contains(strings.ToLower(q(r, "Filters")), "isresumable")
	args := []any{}
	if !resume && !latest && (parentlessBrowse(r) || libraryTypes(r)) {
		respond(w, M{"Items": a.libraryDTOs(), "TotalRecordCount": len(a.libraries()), "StartIndex": 0})
		return
	}
	if resume {
		where += " AND kind IN ('Movie','Episode') AND id IN (SELECT item FROM userdata WHERE user_id=? AND position>0 AND played=0)"
		args = append(args, u.ID)
	}
	parent := q(r, "ParentId")
	if parent == "root" || parent == a.serverID {
		parent = ""
		if !strings.EqualFold(q(r, "Recursive"), "true") && q(r, "IncludeItemTypes") == "" {
			respond(w, M{"Items": a.libraryDTOs(), "TotalRecordCount": len(a.libraries()), "StartIndex": 0})
			return
		}
	}
	if parent != "" {
		var n int
		a.db.QueryRow("SELECT count(*) FROM libraries WHERE id=?", parent).Scan(&n)
		if n > 0 {
			where += " AND lib=?"
			args = append(args, parent)
			if !resume && !latest && !strings.EqualFold(q(r, "Recursive"), "true") {
				where += " AND parent=?"
				args = append(args, parent)
			}
		} else if !u.API && a.versionGrouping() != "id" {
			group := a.versionGrouping()
			parents := "SELECT id FROM items WHERE " + group + "=(SELECT " + group + " FROM items WHERE id=?)"
			if strings.EqualFold(q(r, "Recursive"), "true") {
				where += " AND id IN (WITH RECURSIVE tree(id) AS (SELECT id FROM items WHERE parent IN (" + parents + ") UNION SELECT items.id FROM items JOIN tree ON items.parent=tree.id) SELECT id FROM tree)"
			} else {
				where += " AND parent IN (" + parents + ")"
			}
			args = append(args, parent)
		} else if strings.EqualFold(q(r, "Recursive"), "true") {
			where += " AND id IN (WITH RECURSIVE tree(id) AS (SELECT id FROM items WHERE parent=? UNION SELECT items.id FROM items JOIN tree ON items.parent=tree.id) SELECT id FROM tree)"
			args = append(args, parent)
		} else {
			where += " AND parent=?"
			args = append(args, parent)
		}
	}
	if ids := q(r, "Ids"); ids != "" {
		parts := strings.Split(ids, ",")
		if len(parts) > 200 {
			fail(w, 400, "too many ids")
			return
		}
		where += " AND id IN (" + strings.TrimRight(strings.Repeat("?,", len(parts)), ",") + ")"
		for _, v := range parts {
			args = append(args, v)
		}
	}
	if s := strings.TrimSpace(q(r, "SearchTerm")); s != "" {
		initials := a.defaultOn("search_by_initials") && isInitialsQuery(s)
		prefix := "%"
		if initials && len(s) <= 2 {
			prefix = ""
		}
		clause := "name ILIKE ? ESCAPE '\\'"
		s = strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(s)
		args = append(args, prefix+s+"%")
		if initials {
			clause += " OR media_initials(name) LIKE ? ESCAPE '\\'"
			args = append(args, prefix+strings.ToLower(s)+"%")
		}
		where += " AND (" + clause + ")"
	}
	if s := strings.TrimSpace(q(r, "NameStartsWith")); s != "" {
		s = strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(s)
		clause := "name ILIKE ? ESCAPE '\\'"
		args = append(args, s+"%")
		if a.defaultOn("search_by_initials") {
			clause += " OR media_initials(name) LIKE ? ESCAPE '\\'"
			args = append(args, strings.ToLower(s)+"%")
		}
		where += " AND (" + clause + ")"
	}
	if kinds := q(r, "IncludeItemTypes"); kinds != "" {
		parts := strings.Split(kinds, ",")
		if len(parts) > 10 {
			fail(w, 400, "too many types")
			return
		}
		where += " AND kind IN (" + strings.TrimRight(strings.Repeat("?,", len(parts)), ",") + ")"
		for _, v := range parts {
			args = append(args, v)
		}
	}
	where, args = a.browseFilters(r, u, where, args, latest)
	if !u.API && !resume && q(r, "Ids") == "" {
		where, args = a.mergeWhere(where, args)
	}
	limit, _ := strconv.Atoi(q(r, "Limit"))
	maxLimit := 200
	if p, err := a.item(parent); parent != "" && err == nil && (p.Kind == "Series" || p.Kind == "Season") {
		maxLimit = 10000
		if limit < 1 {
			limit = maxLimit
		}
	}
	if limit < 1 {
		limit = 60
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	order := browseOrder(r)
	if latest {
		order = "mtime DESC,id DESC"
	}
	if resume {
		order = "resume"
	}
	loaded, p, next, more, e := a.pageItems(r, u, where, args, order, limit)
	if e != nil {
		log.Printf("items query: %v", e)
		fail(w, 400, "查询失败: "+e.Error())
		return
	}
	if !u.API && browseMediaDetails(r) && len(loaded) == 1 && loaded[0].ID == strings.TrimSpace(q(r, "Ids")) && a.probeSettings().Browse {
		defer func() {
			_ = http.NewResponseController(w).Flush()
			a.queueProbe(loaded[0], token(r), r.UserAgent(), false)
		}()
	}
	out := a.listDTOs(loaded, r, u)
	if p.Count < 0 {
		p.Count = p.Position + len(out)
		if more {
			p.Count++
		}
	}
	if next != "" {
		w.Header().Set("X-Next-Cursor", next)
	}
	if latest {
		respond(w, out)
	} else {
		respond(w, M{"Items": out, "TotalRecordCount": p.Count, "StartIndex": p.Position, "NextCursor": next, "HasMore": more})
	}

}
func (a *App) reserve(u User, item string) error {
	if !a.canPlay(u.ID) {
		return errors.New("管理员已禁止该用户播放")
	}
	if u.API {
		return errors.New("播放需要用户令牌")
	}
	a.playing.Lock()
	defer a.playing.Unlock()
	tx, e := a.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, e = tx.Exec("DELETE FROM plays WHERE updated<?", now-180); e != nil {
		return e
	}
	var n int
	e = tx.QueryRow("SELECT count(*) FROM plays WHERE user_id=? AND device<>?", u.ID, u.Device).Scan(&n)
	if e != nil {
		return e
	}
	if n >= u.Max {
		return errors.New("已达到同时播放设备上限")
	}
	_, e = tx.Exec("INSERT INTO plays VALUES(?,?,?,?) ON CONFLICT(user_id,device) DO UPDATE SET item=excluded.item,updated=excluded.updated", u.ID, u.Device, item, now)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (a *App) playback(w http.ResponseWriter, r *http.Request, u User, i string) {
	x, e := a.item(i)
	if e != nil || x.URL == "" {
		fail(w, 404, "无可播放源")
		return
	}
	if !u.API {
		if e = a.reserve(u, i); e != nil {
			fail(w, 403, e.Error())
			return
		}
	}
	respond(w, M{"MediaSources": a.versionSources(x, r, u, false), "PlaySessionId": id()})
}
func (a *App) stream(w http.ResponseWriter, r *http.Request, u User, i string) {
	if r.Method != "GET" && r.Method != "HEAD" {
		fail(w, 405, "GET required")
		return
	}
	x, e := a.item(i)
	if e != nil || x.URL == "" {
		fail(w, 404, "无可播放源")
		return
	}
	if !u.API {
		if e = a.reserve(u, i); e != nil {
			fail(w, 403, e.Error())
			return
		}
	}
	a.logPlayback(r, u, x)
	if !u.API && r.Method == http.MethodGet {
		tracked := &clientResponse{ResponseWriter: w, status: http.StatusOK}
		w = tracked
		defer func() {
			if tracked.status == http.StatusFound || tracked.status == http.StatusTemporaryRedirect {
				a.scheduleNext(x, r)
			}
		}()
	}
	if !u.API {
		if target := a.thirdPartyPlaybackURL(r); target != "" {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Location", target)
			w.WriteHeader(http.StatusFound)
			return
		}
	}
	if endpoint := strmResolver(x.URL); !u.API && endpoint != "" {
		a.resolveSTRM(w, r, x, endpoint)
		return
	}
	if !u.API && os.Getenv("NANSHARE_URL") != "" {
		a.resolveNanShare(w, r)
		return
	}
	if !u.API && os.Getenv("PUBLIC_PLAYBACK_URL") != "" {
		w.Header().Set("Location", strings.TrimRight(os.Getenv("PUBLIC_PLAYBACK_URL"), "/")+a.playURL(x, token(r)))
		w.WriteHeader(302)
		return
	}
	target, e := url.Parse(x.URL)
	if e != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		fail(w, 422, "仅支持 HTTP/HTTPS STRM")
		return
	}
	w.Header().Set("Location", target.String())
	w.WriteHeader(http.StatusFound)
}
func (a *App) poster(w http.ResponseWriter, r *http.Request, i string) { a.serveImage(w, r, i) }
func (a *App) backup() (string, error) {
	p := "/app/backups/emby-" + time.Now().UTC().Format("20060102T150405.000000000") + ".dump"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	f, e := os.OpenFile(p+".partial", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", e
	}
	cmd := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--no-owner", "--no-acl")
	cmd.Env, e = postgresBackupEnv(os.Getenv("DATABASE_URL"))
	if e != nil {
		f.Close()
		os.Remove(p + ".partial")
		return "", e
	}
	cmd.Stdout = f
	e = cmd.Run()
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		os.Remove(p + ".partial")
		return "", e
	}
	if e = os.Rename(p+".partial", p); e != nil {
		return "", e
	}
	files, _ := filepath.Glob("/app/backups/emby-*.dump")
	if len(files) > 14 {
		for _, old := range files[:len(files)-14] {
			os.Remove(old)
		}
	}
	return p, nil
}
func main() {
	db, e := openDatabase(os.Getenv("DATABASE_URL"))
	must(e)
	a := &App{db: db, attempts: map[string][]time.Time{}}
	a.init()
	if os.Getenv("SCHEMA_ONLY") == "1" {
		db.Close()
		return
	}
	licenseClient, licenseErr := licensesdk.NewFromEnv()
	if licenseErr != nil {
		log.Fatalf("license configuration: %v", licenseErr)
	}
	licenseCtx, licenseCancel := context.WithCancel(context.Background())
	defer licenseCancel()
	licenseClient.OnError = func(detail string) { a.recordError(nil, "授权验证错误", detail) }
	licenseClient.Refresh(licenseCtx)
	go licenseClient.Run(licenseCtx)
	go a.reclaimIdleMemory()
	go a.watchMedia()
	go a.runScanSchedule()
	if e := a.migrateMedia(); e != nil {
		log.Printf("media cache migration: %v", e)
	}
	go func() {
		for range time.NewTicker(30 * time.Minute).C {
			if e := a.cleanupMedia(); e != nil {
				log.Printf("media cleanup: %v", e)
			}
		}
	}()
	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = ":8097"
	}
	srv := &http.Server{Addr: listen, Handler: licenseClient.Middleware(http.HandlerFunc(a.serve)), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() {
		log.Printf("Go Emby listening %s; redirect-only playback", listen)
		if e := srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
			log.Fatal(e)
		}
	}()
	go func() {
		for range time.NewTicker(24 * time.Hour).C {
			if _, e := a.backup(); e != nil {
				log.Printf("backup failed: %v", e)
			}
		}
	}()
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	<-c
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	a.scan.Lock()
	defer a.scan.Unlock()
	db.Close()
}
