package main

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type sidecar struct {
	Plot    string   `xml:"plot"`
	Runtime float64  `xml:"runtime"`
	Rating  float64  `xml:"rating"`
	Genres  []string `xml:"genre"`
	Studios []string `xml:"studio"`
	Actors  []struct {
		Name  string `xml:"name"`
		Role  string `xml:"role"`
		Thumb string `xml:"thumb"`
	} `xml:"actor"`
	OriginalTitle string   `xml:"originaltitle"`
	Premiered     string   `xml:"premiered"`
	MPAA          string   `xml:"mpaa"`
	Tagline       string   `xml:"tagline"`
	Directors     []string `xml:"director"`
	UniqueIDs     []struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"uniqueid"`
	TMDB    string `xml:"tmdbid"`
	IMDB    string `xml:"imdbid"`
	Streams struct {
		Video []struct {
			Codec    string  `xml:"codec"`
			Width    int     `xml:"width"`
			Height   int     `xml:"height"`
			Duration float64 `xml:"durationinseconds"`
			Aspect   string  `xml:"aspect"`
		} `xml:"video"`
		Audio []struct {
			Codec    string `xml:"codec"`
			Language string `xml:"language"`
			Channels int    `xml:"channels"`
		} `xml:"audio"`
		Subtitle []struct {
			Codec    string `xml:"codec"`
			Language string `xml:"language"`
		} `xml:"subtitle"`
	} `xml:"fileinfo>streamdetails"`
}

func readSidecar(x Item) sidecar {
	base := strings.TrimSuffix(x.Path, filepath.Ext(x.Path))
	dir := filepath.Dir(x.Path)
	paths := []string{base + ".nfo", filepath.Join(dir, "movie.nfo")}
	if x.Kind == "Season" {
		paths = []string{filepath.Join(x.Path, "season.nfo"), filepath.Join(filepath.Dir(x.Path), "tvshow.nfo")}
	}
	if x.Kind == "Series" {
		paths = []string{filepath.Join(x.Path, "tvshow.nfo")}
	}
	for _, p := range paths {
		real, e := filepath.EvalSymlinks(p)
		if e != nil || !allowedMediaPath(real) {
			continue
		}
		f, e := os.Open(real)
		if e != nil {
			continue
		}
		var n sidecar
		e = xml.NewDecoder(io.LimitReader(f, 2<<20)).Decode(&n)
		f.Close()
		if e == nil {
			return n
		}
	}
	return sidecar{}
}
func (a *App) enrich(x Item, m M) {
	n := a.metadata(x)
	people := []M{}
	hideMissing := a.hideMissingActors()
	for _, p := range n.Actors {
		if p.Name != "" {
			v := M{"Id": personID(p.Name), "Name": p.Name, "Role": p.Role, "Type": "Actor"}
			if path := a.imagePath(personID(p.Name), "Primary"); path != "" {
				v["PrimaryImageTag"] = a.imageTag(personID(p.Name), "Primary", path)
			} else if hideMissing {
				continue
			}
			people = append(people, v)
		}
	}
	for _, name := range n.Directors {
		people = append(people, M{"Id": personID(name), "Name": name, "Type": "Director"})
	}
	m["People"] = people
	m["HideMissingActorImages"] = hideMissing
	m["OriginalTitle"] = n.OriginalTitle
	m["OfficialRating"] = n.MPAA
	if n.Premiered != "" {
		m["PremiereDate"] = n.Premiered + "T00:00:00.0000000Z"
	}
	m["Taglines"] = []string{}
	if n.Tagline != "" {
		m["Taglines"] = []string{n.Tagline}
	}
	m["Genres"] = append([]string{}, n.Genres...)
	studios := []M{}
	for _, s := range n.Studios {
		if name := strings.TrimSpace(s); name != "" {
			// Keep stable numeric IDs exact in both int64 and JavaScript clients.
			studioID, _ := strconv.ParseInt(digest("studio:" + name)[:13], 16, 64)
			studios = append(studios, M{"Name": name, "Id": studioID})
		}
	}
	m["Studios"] = studios
	ids := M{}
	if n.TMDB != "" {
		ids["Tmdb"] = n.TMDB
	}
	if n.IMDB != "" {
		ids["Imdb"] = n.IMDB
	}
	for _, v := range n.UniqueIDs {
		if strings.EqualFold(v.Type, "tmdb") {
			ids["Tmdb"] = v.Value
		}
		if strings.EqualFold(v.Type, "imdb") {
			ids["Imdb"] = v.Value
		}
	}
	m["ProviderIds"] = ids
	if n.Plot != "" {
		m["Overview"] = n.Plot
	}
	if n.Rating > 0 {
		m["CommunityRating"] = n.Rating
	}
	a.enrichMedia(x, n, m)
	a.decorateImages(x, m)
	var cover int
	if a.db.QueryRow("SELECT 1 FROM covers WHERE id=?", x.ID).Scan(&cover) == nil {
		m["ImageTags"].(M)["Primary"] = a.imageTag(x.ID, "Primary", "cover")
	}
	if x.Kind == "Episode" {
		parent, _ := a.item(x.Parent)
		if parent.Kind == "Season" {
			m["SeasonId"] = parent.ID
			m["SeasonName"] = parent.Name
			parent, _ = a.item(parent.Parent)
		}
		if parent.Kind == "Series" {
			m["SeriesId"] = parent.ID
			m["SeriesName"] = parent.Name
			if parent.Poster != "" {
				m["SeriesPrimaryImageTag"] = a.imageTag(parent.ID, "Primary", parent.Poster)
			}
		}
	}
}
func (a *App) enrichMedia(x Item, n sidecar, m M) {
	streams := []M{}
	duration := n.Runtime * 60
	for _, v := range n.Streams.Video {
		streams = append(streams, M{"Type": "Video", "Index": len(streams), "Codec": v.Codec, "Width": v.Width, "Height": v.Height, "AspectRatio": v.Aspect, "IsExternal": false})
		if v.Duration > 0 {
			duration = v.Duration
		}
	}
	for _, v := range n.Streams.Audio {
		streams = append(streams, M{"Type": "Audio", "Index": len(streams), "Codec": v.Codec, "Language": v.Language, "Channels": v.Channels, "IsExternal": false})
	}
	for _, v := range n.Streams.Subtitle {
		streams = append(streams, M{"Type": "Subtitle", "Index": len(streams), "Codec": v.Codec, "Language": v.Language, "IsExternal": false})
	}
	m["MediaStreams"] = streams
	if duration > 0 {
		m["RunTimeTicks"] = int64(duration * 1e7)
	}
	mergeCachedMedia(m, a.cachedMedia(x))
}
func (a *App) playURL(x Item, t string) string {
	// The STRM filename usually preserves the original container; opaque URLs do not.
	ext := "mkv"
	for _, v := range []string{"mp4", "mkv", "avi", "ts", "m2ts", "mov", "webm"} {
		if strings.Contains(strings.ToLower(x.Name+" "+filepath.Base(x.Path)), "."+v) || strings.HasSuffix(strings.Split(strings.ToLower(x.URL), "?")[0], "."+v) {
			ext = v
			break
		}
	}
	if c, ok := a.cachedMedia(x)["Container"].(string); ok {
		for _, v := range []string{"mp4", "mkv", "avi", "ts", "m2ts", "mov", "webm"} {
			if c == v {
				ext = c
			}
		}
	}
	return "/emby/Videos/" + x.ID + "/stream." + ext + "?Static=true&api_key=" + url.QueryEscape(t)
}
func (a *App) viewerSource(x Item, r *http.Request, u User) M {
	m := a.source(x, token(r))
	meta := M{}
	a.enrichMedia(x, a.metadata(x), meta)
	m["MediaStreams"] = meta["MediaStreams"]
	if v, ok := meta["RunTimeTicks"]; ok {
		m["RunTimeTicks"] = v
	}
	mergeCachedMedia(m, a.cachedMedia(x))
	if !u.API {
		p := a.playURL(x, token(r))
		m["DirectStreamUrl"] = strings.TrimPrefix(p, "/emby")
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		// Reverse proxies terminate TLS before forwarding requests to this HTTP server.
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "https" || forwarded == "http" {
			scheme = forwarded
		}
		host := r.Host
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); forwarded != "" && !strings.ContainsAny(forwarded, "/\\?#@ \t\r\n") {
			host = forwarded
		}
		m["Path"] = scheme + "://" + host + p
		m["Container"] = strings.TrimPrefix(filepath.Ext(strings.Split(p, "?")[0]), ".")
		m["SupportsDirectPlay"] = true
	}
	return m
}
func (a *App) viewerDTO(x Item, r *http.Request, u User) M {
	m := a.dto(x)
	if !u.API {
		delete(m, "Path")
		var pos int64
		var played bool
		a.db.QueryRow("SELECT position,played FROM userdata WHERE user_id=? AND item=?", u.ID, x.ID).Scan(&pos, &played)
		m["UserData"] = M{"PlaybackPositionTicks": pos, "Played": played, "IsFavorite": false, "Key": x.ID}
	}
	if x.URL != "" {
		m["MediaSources"] = a.versionSources(x, r, u, true)
		m["MediaSourceCount"] = len(m["MediaSources"].([]M))
	}
	return m
}
func (a *App) similar(w http.ResponseWriter, r *http.Request, u User, i string) {
	x, e := a.item(i)
	if e != nil {
		fail(w, 404, "媒体不存在")
		return
	}
	limit, _ := strconv.Atoi(q(r, "Limit"))
	if limit < 1 {
		limit = 12
	}
	if limit > 50 {
		limit = 50
	}

	filter := "id IN (SELECT id FROM candidates) AND kind=? AND id<>?"
	filterArgs := []any{x.Kind, x.ID}
	if !u.API {
		group := a.versionGrouping()
		filter = "id IN (SELECT id FROM candidates) AND kind=? AND " + group + "<>(SELECT " + group + " FROM items WHERE id=?)"
		filter, filterArgs = a.mergeWhere(filter, filterArgs)
	}
	params := []any{x.ID, x.Kind, x.Year, x.ID, x.Kind, x.ID}
	params = append(params, filterArgs...)
	params = append(params, x.ID, x.Year, limit)
	rows, e := a.db.Query(`WITH candidates AS (
 (SELECT item AS id FROM item_genres WHERE genre IN (SELECT genre FROM item_genres WHERE item=?) LIMIT 512)
 UNION
 (SELECT id FROM items WHERE kind=? AND year=? AND id<>? ORDER BY name,id LIMIT 128)
 UNION
 (SELECT id FROM items WHERE kind=? AND id<>? ORDER BY name,id LIMIT 128)
 ) SELECT `+cols+` FROM items WHERE `+filter+`
 ORDER BY (SELECT count(*) FROM item_genres g WHERE g.item=items.id AND g.genre IN (SELECT genre FROM item_genres WHERE item=?)) DESC,abs(year-?),name,id LIMIT ?`, params...)
	if e != nil {
		fail(w, 500, "查询失败")
		return
	}
	items := []Item{}
	for rows.Next() {
		v, e := readItem(rows)
		if e != nil {
			rows.Close()
			fail(w, 500, "读取失败")
			return
		}
		items = append(items, v)
	}
	rows.Close()
	out := a.listDTOs(items, r, u)
	respond(w, M{"Items": out, "TotalRecordCount": len(out)})
}

func (a *App) orderedViews() []string {
	out := []string{}
	for _, l := range a.libraries() {
		out = append(out, l["Id"].(string))
	}
	return out
}
func parentlessBrowse(r *http.Request) bool {
	if !strings.Contains(strings.ToLower(r.URL.Path), "/users/") && q(r, "UserId") == "" {
		return false
	}
	for _, k := range []string{"ParentId", "SearchTerm", "Ids", "IncludeItemTypes", "Filters"} {
		if q(r, k) != "" {
			return false
		}
	}
	return !strings.EqualFold(q(r, "Recursive"), "true")
}

func (a *App) libraryImageTags(i string) M {
	p := a.imagePath(i, "Primary")
	if p != "" {
		return M{"Primary": a.imageTag(i, "Primary", p)}
	}
	return M{}
}

func libraryTypes(r *http.Request) bool {
	for _, kind := range strings.Split(q(r, "IncludeItemTypes"), ",") {
		if strings.EqualFold(strings.TrimSpace(kind), "CollectionFolder") || strings.EqualFold(strings.TrimSpace(kind), "UserView") {
			return true
		}
	}
	return false
}

// Partial discovery must not replace codec/audio/subtitle details from local metadata.
func mergeCachedMedia(dst, cache M) {
	for k, v := range cache {
		if k == "Partial" {
			continue
		}
		if cache["Partial"] == true && k == "MediaStreams" {
			continue
		}
		dst[k] = v
	}
	if cache["Partial"] == true {
		raw, _ := json.Marshal(cache["MediaStreams"])
		var partial []M
		json.Unmarshal(raw, &partial)
		raw, _ = json.Marshal(dst["MediaStreams"])
		var streams []M
		json.Unmarshal(raw, &streams)
		for _, p := range partial {
			if p["Type"] == "Video" {
				for _, v := range streams {
					if v["Type"] == "Video" {
						for _, k := range []string{"Width", "Height"} {
							if n, ok := p[k]; ok {
								v[k] = n
							}
						}
						break
					}
				}
			}
		}
		dst["MediaStreams"] = streams
	}
}

// Emby item types are case-insensitive. Lenna sends Movie even for TV libraries;
// correct only its library entry request, preserving episode/season navigation.
func (a *App) normalizeBrowseTypes(r *http.Request, latest bool) {
	raw := q(r, "IncludeItemTypes")
	if raw == "" {
		return
	}
	parts := strings.Split(raw, ",")
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
		for _, kind := range []string{"Movie", "Series", "Season", "Episode", "Folder", "CollectionFolder", "UserView", "MusicVideo", "Audio", "MusicAlbum", "MusicArtist", "BoxSet"} {
			if strings.EqualFold(parts[i], kind) {
				parts[i] = kind
				break
			}
		}
	}
	client := field(r, "Client")
	if client == "" {
		client = r.Header.Get("X-Emby-Client")
	}
	lenna := strings.EqualFold(client, "Lenna") || strings.HasPrefix(strings.ToLower(r.UserAgent()), "lenna/")
	if lenna && !latest && len(parts) == 1 && parts[0] == "Movie" && q(r, "ParentId") != "" && q(r, "Ids") == "" && q(r, "SearchTerm") == "" && q(r, "Filters") == "" && !strings.HasSuffix(strings.ToLower(r.URL.Path), "/resume") {
		var kind string
		if a.db.QueryRow("SELECT kind FROM libraries WHERE id=?", q(r, "ParentId")).Scan(&kind) == nil && strings.EqualFold(kind, "tvshows") {
			parts[0] = "Series"
		}
	}
	setQuery(r, "IncludeItemTypes", strings.Join(parts, ","))
}
