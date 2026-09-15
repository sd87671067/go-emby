package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *App) browseSchema() error {
	_, e := a.db.Exec(`CREATE TABLE IF NOT EXISTS item_metadata(item TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS item_people(item TEXT REFERENCES items(id) ON DELETE CASCADE,person TEXT,name TEXT,role TEXT,type TEXT,thumb TEXT,PRIMARY KEY(item,person,type));
 CREATE INDEX IF NOT EXISTS people_person ON item_people(person);
 CREATE TABLE IF NOT EXISTS actor_image_failures(person TEXT PRIMARY KEY, path TEXT NOT NULL, expires BIGINT NOT NULL);`)
	if e == nil {
		_, e = a.db.Exec("INSERT INTO settings(k,v) VALUES('image_secret',?) ON CONFLICT DO NOTHING", id()+id())
	}
	return e
}
func personID(name string) string { return "person-" + digest(strings.TrimSpace(name))[:32] }
func (a *App) metadata(x Item) sidecar {
	var raw string
	var n sidecar
	if a.db.QueryRow("SELECT data FROM item_metadata WHERE item=?", x.ID).Scan(&raw) == nil && json.Unmarshal([]byte(raw), &n) == nil {
		return n
	}
	return readSidecar(x)
}
func (a *App) metadataJSON(x Item) string     { b, _ := json.Marshal(a.metadata(x)); return string(b) }
func (a *App) indexMetadata(lib string) error { return a.indexMetadataGeneration(lib, "") }
func (a *App) indexMetadataGeneration(lib, generation string, jobs ...string) error {
	job := ""
	if len(jobs) > 0 {
		job = jobs[0]
	}
	// Page the catalog and close rows before writing, keeping memory and locks bounded.
	after := ""
	for {
		rows, e := a.db.Query("SELECT "+cols+" FROM items WHERE id>? AND (?='' OR lib=?) AND (?='' OR seen=?) ORDER BY id LIMIT 100", after, lib, lib, generation, generation)
		if e != nil {
			return e
		}
		batch := []Item{}
		for rows.Next() {
			x, e := readItem(rows)
			if e != nil {
				rows.Close()
				return e
			}
			batch = append(batch, x)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(batch) == 0 {
			return nil
		}
		// Parse files before taking the SQLite writer lock.
		metas := make([]sidecar, len(batch))
		for j, x := range batch {
			a.waitScan(job)
			metas[j] = readSidecar(x)
		}
		tx, e := a.db.Begin()
		if e != nil {
			return e
		}
		for j, x := range batch {
			n := metas[j]
			b, _ := json.Marshal(n)
			var previous string
			if tx.QueryRow("SELECT data FROM item_metadata WHERE item=?", x.ID).Scan(&previous) == nil && previous == string(b) {
				continue
			}
			if _, e = tx.Exec("INSERT INTO item_metadata VALUES(?,?) ON CONFLICT(item) DO UPDATE SET data=excluded.data", x.ID, string(b)); e != nil {
				break
			}
			if _, e = tx.Exec("DELETE FROM item_people WHERE item=?", x.ID); e != nil {
				break
			}
			for _, p := range n.Actors {
				if p.Name == "" {
					continue
				}
				_, e = tx.Exec("INSERT INTO item_people VALUES(?,?,?,?,?,?) ON CONFLICT(item,person,type) DO UPDATE SET name=excluded.name,role=excluded.role,thumb=excluded.thumb", x.ID, personID(p.Name), p.Name, p.Role, "Actor", actorImage(x, p.Name, p.Thumb))
				if e != nil {
					break
				}
			}
			if e != nil {
				break
			}
			for _, name := range n.Directors {
				_, e = tx.Exec("INSERT INTO item_people VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING", x.ID, personID(name), name, "", "Director", "")
				if e != nil {
					break
				}
			}
			if e != nil {
				break
			}
		}
		if e != nil {
			tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		after = batch[len(batch)-1].ID
	}
}
func safeImage(p string) string {
	real, e := filepath.EvalSymlinks(p)
	if e != nil || !allowedMediaPath(real) {
		return ""
	}
	st, e := os.Stat(real)
	if e != nil || !st.Mode().IsRegular() || st.Size() > 20<<20 {
		return ""
	}
	switch strings.ToLower(filepath.Ext(real)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return real
	}
	return ""
}
func actorImage(x Item, name, thumb string) string {
	dir := filepath.Dir(x.Path)
	if x.Kind == "Series" || x.Kind == "Season" {
		dir = x.Path
	}
	for _, p := range []string{filepath.Join(dir, ".actors", strings.ReplaceAll(name, " ", "_")+".jpg"), filepath.Join(dir, thumb)} {
		if p = safeImage(p); p != "" {
			return p
		}
	}
	u, e := url.Parse(thumb)
	if e == nil && u.Scheme == "https" && u.Host == "image.tmdb.org" && strings.HasPrefix(u.Path, "/t/p/") {
		return u.String()
	}
	return ""
}
func (a *App) imagePath(i, kind string) string {
	if kind == "Primary" {
		var n int
		if a.db.QueryRow("SELECT 1 FROM covers WHERE id=?", i).Scan(&n) == nil {
			return "cover"
		}
		if strings.HasPrefix(i, "person-") {
			var p string
			a.db.QueryRow("SELECT thumb FROM item_people WHERE person=? AND thumb<>'' ORDER BY item LIMIT 1", i).Scan(&p)
			var failed int
			if a.db.QueryRow("SELECT 1 FROM actor_image_failures WHERE person=? AND path=? AND expires>?", i, p, time.Now().Unix()).Scan(&failed) == nil {
				return ""
			}
			if strings.HasPrefix(p, "https://image.tmdb.org/") {
				return p
			}
			return safeImage(p)
		}
	}
	x, e := a.item(i)
	if e != nil {
		if kind != "Primary" {
			return ""
		}
		x, e = readItem(a.db.QueryRow("SELECT "+cols+" FROM items WHERE lib=? AND poster<>'' ORDER BY name,id LIMIT 1", i))
	}
	if e != nil {
		return ""
	}
	if x.Kind == "Episode" && (kind == "Primary" || kind == "Thumb") {
		if p := episodeThumb(x); p != "" {
			return p
		}
	}
	if kind == "Primary" {
		if x.Kind == "Series" && x.Poster == "" {
			if p := safeImage(seriesFallbackPoster(x.Path)); p != "" {
				return p
			}
		}
		if x.Kind == "Season" {
			if p := safeImage(seasonPoster(x.Path, x.Season)); p != "" {
				return p
			}
		}
		if p := safeImage(x.Poster); p != "" {
			return p
		}
	}
	dir := filepath.Dir(x.Path)
	if x.Kind == "Series" || x.Kind == "Season" {
		dir = x.Path
	}
	base := strings.TrimSuffix(x.Path, filepath.Ext(x.Path))
	names := map[string][]string{"Primary": {"poster", "folder"}, "Backdrop": {"backdrop", "fanart"}, "Thumb": {"thumb", "landscape"}, "Logo": {"logo", "clearlogo"}, "Banner": {"banner"}}[kind]
	for _, n := range names {
		for _, ext := range []string{".jpg", ".png", ".webp", ".jpeg"} {
			for _, p := range []string{base + "-" + n + ext, filepath.Join(dir, n+ext)} {
				if p = safeImage(p); p != "" {
					return p
				}
			}
		}
	}
	if kind == "Primary" && x.Parent != x.Lib && x.Parent != x.ID {
		if parent, e := a.item(x.Parent); e == nil {
			return a.imagePath(parent.ID, kind)
		}
	}
	return ""
}
func (a *App) imageTag(i, kind, p string) string {
	secret := a.cursorSecret
	if secret == "" {
		a.db.QueryRow("SELECT v FROM settings WHERE k='image_secret'").Scan(&secret)
	}
	revision := p
	if p == "cover" {
		var b []byte
		a.db.QueryRow("SELECT data FROM covers WHERE id=?", i).Scan(&b)
		revision = digest(string(b))
	} else if st, e := os.Stat(p); e == nil {
		revision += fmt.Sprint(st.ModTime().UnixNano(), st.Size())
	}
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(i + "|" + kind + "|" + revision))
	return hex.EncodeToString(h.Sum(nil))[:32]
}
func (a *App) decorateImages(x Item, m M) {
	tags := M{}
	back := []string{}
	for _, kind := range []string{"Primary", "Backdrop", "Thumb", "Logo", "Banner"} {
		if p := a.imagePath(x.ID, kind); p != "" {
			tag := a.imageTag(x.ID, kind, p)
			if kind == "Backdrop" {
				back = append(back, tag)
			} else {
				tags[kind] = tag
			}
		}
	}
	m["ImageTags"] = tags
	m["BackdropImageTags"] = back
	m["PrimaryImageAspectRatio"] = 2.0 / 3
	if x.Kind == "Episode" {
		m["PrimaryImageAspectRatio"] = 16.0 / 9
	}
	m["DateCreated"] = time.Unix(0, x.Mtime).UTC().Format(time.RFC3339)
	if x.Kind == "Series" || x.Kind == "Season" {
		var n int
		a.db.QueryRow("SELECT count(*) FROM items WHERE parent=?", x.ID).Scan(&n)
		m["ChildCount"] = n
	}
	if x.Kind == "Season" {
		m["IndexNumber"] = x.Season
		if p, e := a.item(x.Parent); e == nil {
			m["SeriesId"] = p.ID
			m["SeriesName"] = p.Name
		}
	}
}
func imageRequest(p string) (string, string, bool) {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 4 || !strings.EqualFold(parts[0], "items") || !strings.EqualFold(parts[2], "images") {
		return "", "", false
	}
	kind := ""
	for _, k := range []string{"Primary", "Backdrop", "Thumb", "Logo", "Banner"} {
		if strings.EqualFold(parts[3], k) {
			kind = k
		}
	}
	return parts[1], kind, kind != ""
}
func (a *App) taggedImage(w http.ResponseWriter, r *http.Request, p string) bool {
	i, k, ok := imageRequest(p)
	if !ok || (r.Method != "GET" && r.Method != "HEAD") {
		return false
	}
	tag := q(r, "tag")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if tag == "" && len(parts) > 5 {
		tag = parts[5]
	}
	if tag == "" {
		return false
	}
	if k == "Primary" {
		if x, e := a.item(i); e == nil && x.Poster != "" && hmac.Equal([]byte(tag), []byte(a.listImageTag(x))) {
			a.serveImage(w, r, i)
			return true
		}
	}
	path := a.imagePath(i, k)
	if path == "" || !hmac.Equal([]byte(tag), []byte(a.imageTag(i, k, path))) {
		return false
	}
	a.serveImage(w, r, i)
	return true
}

var posterHTTP = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
	if len(via) > 2 || r.URL.Scheme != "https" || r.URL.Host != "image.tmdb.org" {
		return fmt.Errorf("image redirect rejected")
	}
	return nil
}}

func (a *App) serveImage(w http.ResponseWriter, r *http.Request, i string) {
	if r.Method != "GET" && r.Method != "HEAD" {
		fail(w, 405, "GET or HEAD required")
		return
	}
	p := strings.TrimPrefix(strings.ToLower(r.URL.Path), "/emby")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) == 3 {
		out := []M{}
		for _, k := range []string{"Primary", "Backdrop", "Thumb", "Logo", "Banner"} {
			if path := a.imagePath(i, k); path != "" {
				out = append(out, M{"ImageType": k, "ImageIndex": 0, "ImageTag": a.imageTag(i, k, path)})
			}
		}
		respond(w, out)
		return
	}
	_, kind, ok := imageRequest(p)
	if !ok {
		fail(w, 404, "图片不存在")
		return
	}
	if len(parts) > 4 && parts[4] != "0" {
		fail(w, 404, "图片索引不存在")
		return
	}
	path := a.imagePath(i, kind)
	if path == "" {
		if kind != "Primary" {
			fail(w, 404, "图片不存在")
			return
		}
		var n int
		if a.db.QueryRow("SELECT 1 FROM items WHERE id=? UNION ALL SELECT 1 FROM libraries WHERE id=? LIMIT 1", i, i).Scan(&n) != nil {
			fail(w, 404, "图片不存在")
			return
		}
		img := image.NewRGBA(image.Rect(0, 0, 360, 540))
		for y := 0; y < 540; y++ {
			for x := 0; x < 360; x++ {
				img.SetRGBA(x, y, color.RGBA{uint8(30 + y/12), uint8(50 + y/18), 80, 255})
			}
		}
		w.Header().Set("Content-Type", "image/png")
		if r.Method != "HEAD" {
			png.Encode(w, img)
		}
		return
	}
	tag := a.imageTag(i, kind, path)
	w.Header().Set("ETag", `"`+tag+`"`)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if r.Header.Get("If-None-Match") == `"`+tag+`"` {
		w.WriteHeader(304)
		return
	}
	if path == "cover" {
		var b []byte
		var mime string
		if a.db.QueryRow("SELECT data,mime FROM covers WHERE id=?", i).Scan(&b, &mime) != nil {
			fail(w, 404, "图片不存在")
			return
		}
		w.Header().Set("Content-Type", mime)
		if serveThumbnail(w, r, bytes.NewReader(b)) {
			return
		}
		http.ServeContent(w, r, "cover", time.Time{}, bytes.NewReader(b))
		return
	}
	if strings.HasPrefix(path, "https://image.tmdb.org/") {
		req, e := http.NewRequestWithContext(r.Context(), "GET", path, nil)
		if e != nil {
			fail(w, 502, "图片地址无效")
			return
		}
		res, e := posterHTTP.Do(req)
		if e != nil {
			a.markActorImageFailure(i, path)
			fail(w, 502, "演员图片暂不可用")
			return
		}
		defer res.Body.Close()
		if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "image/") {
			a.markActorImageFailure(i, path)
			fail(w, 502, "演员图片暂不可用")
			return
		}
		b, e := io.ReadAll(io.LimitReader(res.Body, 5<<20+1))
		if e != nil || len(b) > 5<<20 {
			fail(w, 502, "图片过大")
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(b))
		if serveThumbnail(w, r, bytes.NewReader(b)) {
			return
		}
		http.ServeContent(w, r, "actor", time.Time{}, bytes.NewReader(b))
		return
	}
	if path = safeImage(path); path == "" {
		fail(w, 404, "图片不存在")
		return
	}
	if f, e := os.Open(path); e == nil {
		defer f.Close()
		if serveThumbnail(w, r, f) {
			return
		}
	}
	http.ServeFile(w, r, path)
}
func (a *App) rootDTO() M {
	return M{"Id": "root", "Name": "媒体库", "Type": "UserRootFolder", "IsFolder": true, "DisplayPreferencesId": "root", "SortName": "媒体库", "ExternalUrls": []M{}, "ProviderIds": M{}, "Taglines": []string{}, "RemoteTrailers": []M{}, "ServerId": a.serverID, "ChildCount": len(a.libraries()), "ImageTags": M{}, "BackdropImageTags": []string{}, "UserData": M{"Played": false, "IsFavorite": false}}
}
func setQuery(r *http.Request, k, v string) {
	qv := r.URL.Query()
	for key := range qv {
		if strings.EqualFold(key, k) {
			qv.Del(key)
		}
	}
	qv.Set(k, v)
	r.URL.RawQuery = qv.Encode()
}
func (a *App) browseRoute(w http.ResponseWriter, r *http.Request, u User, p string) bool {
	l := strings.ToLower(p)
	if l == "/enhancements" && r.Method == "GET" {
		respond(w, a.enhancementValues())
		return true
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	// Keep per-user authorization checks consistent with the original dispatcher.
	if len(parts) >= 2 && strings.EqualFold(parts[0], "users") && !u.Admin && !u.API && parts[1] != u.ID && parts[1] != "me" {
		return false
	}
	if l == "/library/mediafolders" {
		respond(w, M{"Items": a.libraryDTOs(), "TotalRecordCount": len(a.libraries()), "StartIndex": 0})
		return true
	}
	if len(parts) == 4 && strings.EqualFold(parts[0], "users") && strings.EqualFold(parts[2], "items") && strings.EqualFold(parts[3], "root") {
		respond(w, a.rootDTO())
		return true
	}
	if len(parts) == 3 && strings.EqualFold(parts[0], "users") && strings.EqualFold(parts[2], "groupingoptions") {
		respond(w, []M{})
		return true
	}
	if l == "/sessions/capabilities" || l == "/sessions/capabilities/full" {
		w.WriteHeader(204)
		return true
	}
	if strings.HasPrefix(l, "/displaypreferences/") {
		respond(w, M{"Id": parts[1], "SortBy": "SortName", "SortOrder": "Ascending", "ViewType": "Poster", "CustomPrefs": M{}, "RememberIndexing": false, "RememberSorting": true})
		return true
	}
	if l == "/items/latest" {
		a.items(w, r, u, true)
		return true
	}
	if l == "/shows/nextup" {
		respond(w, M{"Items": []M{}, "TotalRecordCount": 0, "StartIndex": 0, "HasMore": false})
		return true
	}
	if len(parts) == 3 && strings.EqualFold(parts[0], "shows") && (strings.EqualFold(parts[2], "seasons") || strings.EqualFold(parts[2], "episodes")) {
		setQuery(r, "ParentId", parts[1])
		if strings.EqualFold(parts[2], "seasons") {
			setQuery(r, "IncludeItemTypes", "Season")
		} else {
			setQuery(r, "IncludeItemTypes", "Episode")
			setQuery(r, "Recursive", "true")
			if s := q(r, "SeasonId"); s != "" {
				setQuery(r, "ParentId", s)
			}
		}
		setQuery(r, "SortBy", "ParentIndexNumber,IndexNumber")
		a.items(w, r, u, false)
		return true
	}
	if l == "/persons" {
		a.persons(w, r)
		return true
	}
	if len(parts) >= 2 && strings.EqualFold(parts[0], "persons") {
		i := parts[1]
		if !strings.HasPrefix(i, "person-") {
			i = personID(i)
		}
		if len(parts) >= 4 && strings.EqualFold(parts[2], "images") {
			clone := r.Clone(r.Context())
			v := *r.URL
			clone.URL = &v
			clone.URL.Path = "/Items/" + i + "/Images/" + parts[3]
			a.serveImage(w, clone, i)
		} else {
			a.personDetail(w, r, i)
		}
		return true
	}
	if l == "/genres" || l == "/items/filters" || l == "/items/filters2" {
		a.genres(w, r, l == "/genres")
		return true
	}
	if l == "/movies/recommendations" {
		var seed, name string
		err := a.db.QueryRow("SELECT i.id,i.name FROM items i JOIN userdata d ON d.item=i.id LEFT JOIN resume_activity ra ON ra.item=i.id AND ra.user_id=d.user_id WHERE d.user_id=? AND (d.played=1 OR d.position>0) AND i.kind='Movie' AND (?='' OR i.lib=?) ORDER BY ra.updated DESC,i.id LIMIT 1", u.ID, q(r, "ParentId"), q(r, "ParentId")).Scan(&seed, &name)
		if err != nil {
			respond(w, []M{})
			return true
		}
		rr := r.Clone(r.Context())
		v := *r.URL
		rr.URL = &v
		if n := q(r, "ItemLimit"); n != "" {
			setQuery(rr, "Limit", n)
		}
		rec := &jsonCapture{header: http.Header{}}
		a.similar(rec, rr, u, seed)
		var result struct{ Items []M }
		if json.Unmarshal(rec.buf.Bytes(), &result) != nil || result.Items == nil {
			fail(w, 500, "推荐查询失败")
			return true
		}
		respond(w, []M{{"BaselineItemName": name, "CategoryId": 1, "RecommendationType": "SimilarToRecentlyPlayed", "Items": result.Items}})
		return true
	}

	return false
}

type jsonCapture struct {
	header http.Header
	buf    bytes.Buffer
}

func (c *jsonCapture) Header() http.Header         { return c.header }
func (c *jsonCapture) Write(b []byte) (int, error) { return c.buf.Write(b) }
func (c *jsonCapture) WriteHeader(int)             {}
func (a *App) personDTO(i, name string) M {
	m := M{"Id": i, "Name": name, "Type": "Person", "ServerId": a.serverID, "IsFolder": false, "ImageTags": a.libraryImageTags(i), "BackdropImageTags": []string{}, "Overview": "", "PrimaryImageAspectRatio": 2.0 / 3, "UserData": M{"Played": false, "IsFavorite": false}}
	var n int
	a.db.QueryRow("SELECT count(DISTINCT item) FROM item_people WHERE person=?", i).Scan(&n)
	m["MovieCount"] = n
	return m
}
func (a *App) personDetail(w http.ResponseWriter, r *http.Request, i string) {
	var name string
	if a.db.QueryRow("SELECT name FROM item_people WHERE person=? LIMIT 1", i).Scan(&name) != nil {
		fail(w, 404, "演员不存在")
		return
	}
	respond(w, a.personDTO(i, name))
}
func (a *App) persons(w http.ResponseWriter, r *http.Request) {
	where := "1=1"
	if a.hideMissingActors() {
		where += " AND EXISTS (SELECT 1 FROM item_people visible WHERE visible.person=item_people.person AND visible.thumb<>'' AND NOT EXISTS (SELECT 1 FROM actor_image_failures f WHERE f.person=visible.person AND f.path=visible.thumb AND f.expires>extract(epoch from now())))"
	}
	args := []any{}
	if s := q(r, "SearchTerm"); s != "" {
		where += " AND name ILIKE ?"
		args = append(args, "%"+s+"%")
	}
	if p := q(r, "PersonIds"); p != "" {
		where += " AND person=?"
		args = append(args, p)
	}
	limit, _ := strconv.Atoi(q(r, "Limit"))
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	key := a.pageSignature(r, where, "persons", args)
	p, e := a.pageStart(r, key)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	if p.Count < 0 && !strings.EqualFold(q(r, "EnableTotalRecordCount"), "false") {
		if e = a.db.QueryRow("SELECT count(DISTINCT person) FROM item_people WHERE "+where, args...).Scan(&p.Count); e != nil {
			fail(w, 500, "演员查询失败")
			return
		}
	}
	if len(p.Values) > 0 {
		if len(p.Values) != 2 {
			fail(w, 400, "invalid cursor")
			return
		}
		where += " AND (name,person)>(?,?)"
		args = append(args, p.Values[0], p.Values[1])
	}
	sql := "SELECT DISTINCT person,name FROM item_people WHERE " + where + " ORDER BY name,person LIMIT ?"
	args = append(args, limit+1)
	if len(p.Values) == 0 && p.Position > 0 {
		sql += " OFFSET ?"
		args = append(args, p.Position)
	}
	rows, e := a.db.Query(sql, args...)
	if e != nil {
		fail(w, 500, "演员查询失败")
		return
	}
	pairs := [][2]string{}
	for rows.Next() {
		var pair [2]string
		if e = rows.Scan(&pair[0], &pair[1]); e != nil {
			rows.Close()
			fail(w, 500, "演员读取失败")
			return
		}
		pairs = append(pairs, pair)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		fail(w, 500, "演员读取失败")
		return
	}
	more := len(pairs) > limit
	if more {
		pairs = pairs[:limit]
	}
	next := ""
	if more {
		n := p
		n.Position += len(pairs)
		last := pairs[len(pairs)-1]
		n.Values = []string{last[1], last[0]}
		next = a.savePage(n)
	}
	out := []M{}
	for _, pair := range pairs {
		out = append(out, M{"Id": pair[0], "Name": pair[1], "Type": "Person", "ImageTags": a.libraryImageTags(pair[0]), "IsFolder": false})
	}
	if p.Count < 0 {
		p.Count = p.Position + len(out)
		if more {
			p.Count++
		}
	}
	respond(w, M{"Items": out, "TotalRecordCount": p.Count, "StartIndex": p.Position, "NextCursor": next, "HasMore": more})

}
func (a *App) genres(w http.ResponseWriter, r *http.Request, list bool) {
	rows, e := a.db.Query("SELECT DISTINCT g.genre FROM item_genres g JOIN items i ON i.id=g.item WHERE (?='' OR i.lib=?) ORDER BY g.genre", q(r, "ParentId"), q(r, "ParentId"))
	if e != nil {
		fail(w, 500, "类型查询失败")
		return
	}
	defer rows.Close()
	names := []string{}
	out := []M{}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
		out = append(out, M{"Id": "genre-" + digest(n)[:32], "Name": n, "Type": "Genre", "ImageTags": M{}})
	}
	if list {
		respond(w, M{"Items": out, "TotalRecordCount": len(out), "StartIndex": 0})
	} else {
		if strings.HasSuffix(strings.ToLower(r.URL.Path), "filters2") {
			pairs := []M{}
			for _, v := range out {
				pairs = append(pairs, M{"Name": v["Name"], "Id": v["Id"]})
			}
			respond(w, M{"Genres": pairs, "Tags": []string{}})
		} else {
			respond(w, M{"Genres": names, "Tags": []string{}, "Years": []int{}, "OfficialRatings": []string{}})
		}
	}
}
func (a *App) browseFilters(r *http.Request, u User, where string, args []any, latest bool) (string, []any) {
	filters := strings.ToLower(q(r, "Filters"))
	if strings.EqualFold(q(r, "IsPlayed"), "true") || strings.Contains(filters, "isplayed") {
		where += " AND id IN (SELECT item FROM userdata WHERE user_id=? AND played=1)"
		args = append(args, u.ID)
	}
	if strings.EqualFold(q(r, "IsPlayed"), "false") || strings.Contains(filters, "isunplayed") {
		where += " AND id NOT IN (SELECT item FROM userdata WHERE user_id=? AND played=1)"
		args = append(args, u.ID)
	}
	if strings.Contains(strings.ToLower(r.URL.Path), "/shows/nextup") {
		where += ` AND id IN (
 SELECT pick.id FROM (SELECT id FROM items WHERE kind='Series' UNION ALL SELECT id FROM libraries) s
 CROSS JOIN LATERAL (
 SELECT e.id FROM (SELECT s.id UNION ALL SELECT id FROM items WHERE parent=s.id AND kind='Season') parents
 CROSS JOIN LATERAL (SELECT i.id,i.season,i.episode FROM items i WHERE i.parent=parents.id AND i.kind='Episode'
 AND NOT EXISTS (SELECT 1 FROM userdata d WHERE d.user_id=? AND d.item=i.id AND d.played=1)
 ORDER BY i.season,i.episode,i.id LIMIT 1) e
 ORDER BY e.season,e.episode,e.id LIMIT 1) pick)`
		args = append(args, u.ID)
	}
	if latest && q(r, "IncludeItemTypes") == "" {
		where += " AND kind IN ('Movie','Series')"
	}
	if v := q(r, "IsFolder"); v != "" {
		if strings.EqualFold(v, "true") {
			where += " AND kind IN ('Series','Season')"
		} else {
			where += " AND kind NOT IN ('Series','Season')"
		}
	}
	for _, pair := range [][2]string{{"PersonIds", "person"}, {"Person", "name"}} {
		if v := q(r, pair[0]); v != "" {
			values := strings.Split(v, ",")
			if len(values) > 100 {
				values = values[:100]
			}
			where += " AND id IN (SELECT item FROM item_people WHERE " + pair[1] + " IN (" + strings.TrimRight(strings.Repeat("?,", len(values)), ",") + "))"
			for _, s := range values {
				args = append(args, s)
			}
		}
	}
	if v := q(r, "GenreIds"); v != "" {
		wanted := map[string]bool{}
		for _, v := range strings.Split(v, ",") {
			wanted[v] = true
		}
		names := []string{}
		rows, e := a.db.Query("SELECT DISTINCT genre FROM item_genres")
		if e == nil {
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil && wanted["genre-"+digest(name)[:32]] {
					names = append(names, name)
				}
			}
			rows.Close()
		}
		if len(names) == 0 {
			where += " AND 0=1"
		} else {
			setQuery(r, "Genres", strings.Join(names, "|"))
		}
	}
	if v := q(r, "Genres"); v != "" {
		values := strings.FieldsFunc(v, func(c rune) bool { return c == '|' || c == ',' })
		if len(values) > 0 {
			where += " AND id IN (SELECT item FROM item_genres WHERE genre IN (" + strings.TrimRight(strings.Repeat("?,", len(values)), ",") + "))"
			for _, s := range values {
				args = append(args, s)
			}
		}
	}
	if v := q(r, "Season"); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			where += " AND season=?"
			args = append(args, n)
		}
	}
	if v := q(r, "Years"); v != "" {
		values := strings.Split(v, ",")
		where += " AND year IN (" + strings.TrimRight(strings.Repeat("?,", len(values)), ",") + ")"
		for _, s := range values {
			args = append(args, s)
		}
	}
	return where, args
}
func browseOrder(r *http.Request) string {
	out := []string{}
	dir := " ASC"
	if strings.EqualFold(q(r, "SortOrder"), "Descending") {
		dir = " DESC"
	}
	for _, key := range strings.Split(strings.ToLower(q(r, "SortBy")), ",") {
		if col := map[string]string{"sortname": "name", "name": "name", "productionyear": "year", "datecreated": "mtime", "premieredate": "year", "indexnumber": "episode", "parentindexnumber": "season", "random": "id"}[key]; col != "" {
			out = append(out, col+dir)
		}
	}
	if len(out) == 0 {
		out = append(out, "name"+dir)
	}
	return strings.Join(append(out, "id"+dir), ",")
}

func (a *App) markActorImageFailure(person, path string) {
	if strings.HasPrefix(person, "person-") {
		a.db.Exec("INSERT INTO actor_image_failures(person,path,expires) VALUES(?,?,?) ON CONFLICT(person) DO UPDATE SET path=excluded.path,expires=excluded.expires", person, path, time.Now().Add(time.Hour).Unix())
	}
}

func episodeThumb(x Item) string {
	base := strings.TrimSuffix(x.Path, filepath.Ext(x.Path))
	for _, suffix := range []string{"-thumb.jpg", ".jpg", ".jpeg", ".png", ".webp"} {
		if p := safeImage(base + suffix); p != "" {
			return p
		}
	}
	return ""
}
