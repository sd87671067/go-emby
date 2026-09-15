package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (a *App) extraSchema() error {
	_, e := a.db.Exec(`CREATE TABLE IF NOT EXISTS user_playback(user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,allowed BIGINT NOT NULL DEFAULT 1);
 CREATE TABLE IF NOT EXISTS library_order(library_id TEXT PRIMARY KEY REFERENCES libraries(id) ON DELETE CASCADE,position BIGINT NOT NULL);
 CREATE TABLE IF NOT EXISTS resume_activity(user_id TEXT REFERENCES users(id) ON DELETE CASCADE,item TEXT REFERENCES items(id) ON DELETE CASCADE,updated BIGINT NOT NULL,PRIMARY KEY(user_id,item));
 CREATE TABLE IF NOT EXISTS media_probe(item TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,source TEXT NOT NULL,data TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS media_probe_source_idx ON media_probe(source);
 CREATE TABLE IF NOT EXISTS media_display_names(item TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,name TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS covers(id TEXT PRIMARY KEY,mime TEXT NOT NULL,data BYTEA NOT NULL);`)
	return e
}
func (a *App) canPlay(uid string) bool {
	var allowed bool
	e := a.db.QueryRow("SELECT allowed FROM user_playback WHERE user_id=?", uid).Scan(&allowed)
	return e == sql.ErrNoRows || (e == nil && allowed)
}
func mediaPath(p string) string {
	p = filepath.Clean(p)
	if p == "/vol1/1000/strm" {
		return "/media"
	}
	if strings.HasPrefix(p, "/vol1/1000/strm/") {
		return "/media/" + strings.TrimPrefix(p, "/vol1/1000/strm/")
	}
	return p
}
func (a *App) extraAdmin(w http.ResponseWriter, r *http.Request, p string) {
	switch p {
	case "/admin/directories":
		if r.Method != "GET" {
			fail(w, 405, "GET required")
			return
		}
		path := q(r, "Path")
		if path == "" {
			path = "/media"
		}
		path = mediaPath(path)
		real, e := filepath.EvalSymlinks(path)
		if e != nil || !allowedMediaPath(real) {
			fail(w, 400, "目录必须位于已配置的媒体挂载目录")
			return
		}
		f, e := os.Open(real)
		if e != nil {
			fail(w, 400, "无法读取目录")
			return
		}
		defer f.Close()
		dirs := []M{}
		after := q(r, "After")
		next := ""
		// Page by filesystem offset to bound RAM even for enormous flat directories.
		offset := 0
		start := 0
		if after != "" {
			for _, c := range after {
				if c < '0' || c > '9' {
					fail(w, 400, "无效游标")
					return
				}
				start = start*10 + int(c-'0')
				if start > 10000000 {
					fail(w, 400, "游标超限")
					return
				}
			}
		}
		for {
			entries, err := f.ReadDir(256)
			for _, d := range entries {
				offset++
				if offset <= start {
					continue
				}
				if d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
					dirs = append(dirs, M{"Name": d.Name(), "Path": filepath.Join(real, d.Name())})
				}
				if len(dirs) == 200 {
					next = strconv.Itoa(offset)
					break
				}
			}
			if next != "" || err == io.EOF {
				break
			}
			if err != nil {
				fail(w, 500, "读取目录失败")
				return
			}
		}
		respond(w, M{"Path": real, "Directories": dirs, "Next": next})
	case "/admin/order":
		if r.Method != "PUT" {
			fail(w, 405, "PUT required")
			return
		}
		var b struct{ IDs []string }
		if !body(w, r, &b) {
			return
		}
		tx, e := a.db.Begin()
		if e != nil {
			fail(w, 500, "数据库繁忙")
			return
		}
		defer tx.Rollback()
		var count int
		if tx.QueryRow("SELECT count(*) FROM libraries").Scan(&count) != nil || count != len(b.IDs) {
			fail(w, 409, "媒体库已变化，请刷新")
			return
		}
		seen := map[string]bool{}
		for n, id := range b.IDs {
			if seen[id] {
				fail(w, 400, "重复媒体库")
				return
			}
			seen[id] = true
			if _, e = tx.Exec("INSERT INTO library_order VALUES(?,?) ON CONFLICT(library_id) DO UPDATE SET position=excluded.position", id, n); e != nil {
				fail(w, 400, "无效媒体库")
				return
			}
		}
		if tx.Commit() != nil {
			fail(w, 500, "保存失败")
			return
		}
		respond(w, M{"ok": true})
	case "/admin/cover":
		if r.Method != "POST" {
			fail(w, 405, "POST required")
			return
		}
		id := q(r, "Id")
		var exists int
		if a.db.QueryRow("SELECT 1 FROM libraries WHERE id=? UNION ALL SELECT 1 FROM items WHERE id=? LIMIT 1", id, id).Scan(&exists) != nil {
			fail(w, 404, "媒体不存在")
			return
		}
		data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
		if e != nil {
			fail(w, 413, "封面最大5MB")
			return
		}
		mime := http.DetectContentType(data)
		if mime != "image/jpeg" && mime != "image/png" && mime != "image/webp" {
			fail(w, 400, "仅支持JPEG、PNG、WebP")
			return
		}
		if _, e = a.db.Exec("INSERT INTO covers VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET mime=excluded.mime,data=excluded.data", id, mime, data); e != nil {
			fail(w, 500, "保存失败")
			return
		}
		respond(w, M{"ok": true})
	}
}

// Used by the NanShare Caddy forward_auth before consulting any redirect cache.
func (a *App) checkPlayback(w http.ResponseWriter, r *http.Request) {
	original, e := url.ParseRequestURI(r.Header.Get("X-Go-Emby-URI"))
	if e != nil {
		fail(w, 400, "missing original URI")
		return
	}
	clone := r.Clone(r.Context())
	clone.URL = original
	u, e := a.auth(clone)
	if e != nil || u.API {
		fail(w, 401, "播放需要用户登录")
		return
	}
	path := strings.ToLower(embyPath(original.Path))
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || (parts[0] != "videos" && parts[0] != "items" && parts[0] != "audio") {
		fail(w, 400, "invalid playback path")
		return
	}
	if _, e = a.item(parts[1]); e != nil {
		fail(w, 404, "媒体不存在")
		return
	}
	if e = a.reserve(u, parts[1]); e != nil {
		fail(w, 403, e.Error())
		return
	}
	if x, err := a.item(parts[1]); err == nil {
		a.logPlayback(clone, u, x)
	}
	w.WriteHeader(http.StatusNoContent)
}
