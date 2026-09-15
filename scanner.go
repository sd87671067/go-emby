package main

import (
	"database/sql"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type nfo struct {
	Title   string `xml:"title"`
	Plot    string `xml:"plot"`
	Year    int    `xml:"year"`
	Season  int    `xml:"season"`
	Episode int    `xml:"episode"`
}

var epPattern = regexp.MustCompile(`(?i)S(\d{1,3})E(\d{1,4})`)

func localInfo(path string, x *Item) {
	base := strings.TrimSuffix(path, filepath.Ext(path))
	dir := filepath.Dir(path)
	for _, p := range []string{base + ".nfo", filepath.Join(dir, "movie.nfo"), filepath.Join(dir, "tvshow.nfo")} {
		f, e := os.Open(p)
		if e != nil {
			continue
		}
		var n nfo
		e = xml.NewDecoder(io.LimitReader(f, 2<<20)).Decode(&n)
		f.Close()
		if e == nil {
			if n.Title != "" {
				x.Name = n.Title
			}
			x.Overview = n.Plot
			x.Year = n.Year
			if n.Season > 0 {
				x.Season = n.Season
			}
			if n.Episode > 0 {
				x.Episode = n.Episode
			}
			break
		}
	}
	for _, p := range []string{base + "-poster.jpg", base + ".jpg", filepath.Join(dir, "poster.jpg"), filepath.Join(dir, "folder.jpg"), filepath.Join(dir, "poster.png")} {
		if fi, e := os.Stat(p); e == nil && !fi.IsDir() {
			x.Poster = p
			break
		}
	}
}
func (a *App) scanAll() {
	for _, l := range a.libraries() {
		go a.scanLibrary(l["Id"].(string))
	}
}
func (a *App) scanLibrary(lib string) { a.scanLibraryMode(lib, false) }
func (a *App) scanLibraryMode(lib string, incremental bool) {
	a.scanLibraryOptions(lib, incremental, false)
}
func (a *App) scanLibraryOptions(lib string, incremental, allowEmpty bool) {
	a.scanLibraryScoped(lib, incremental, allowEmpty, nil)
}
func (a *App) scanLibraryScoped(lib string, incremental, allowEmpty bool, scopes []string) {
	category := "scan"
	if incremental || len(scopes) > 0 {
		category = "update"
	}
	var queuedName string
	a.db.QueryRow("SELECT name FROM libraries WHERE id=?", lib).Scan(&queuedName)
	job := a.newActivity(category, lib, queuedName)
	release := a.acquireLibraryJob(lib, incremental || len(scopes) > 0)
	defer release()
	a.scan.RLock()
	defer a.scan.RUnlock()
	var jobErr error
	defer func() { a.finishActivity(job, jobErr) }()
	var root, kind, libraryName string
	if a.db.QueryRow("SELECT path,kind,name FROM libraries WHERE id=?", lib).Scan(&root, &kind, &libraryName) != nil {
		jobErr = fmt.Errorf("媒体库不存在")
		return
	}
	a.changeActivity(job, func(v *activityEntry) { v.State = "counting"; v.Name = libraryName })
	start := time.Now()
	gen := id()
	if _, e := a.db.Exec("UPDATE libraries SET status='scanning',error='' WHERE id=?", lib); e != nil {
		jobErr = e
		return
	}
	var scanErr error
	defer func() {
		jobErr = scanErr
		status := "idle"
		message := ""
		if scanErr != nil {
			status = "error"
			message = scanErr.Error()
		}
		var count int
		a.db.QueryRow("SELECT count(*) FROM items WHERE lib=? AND kind IN ('Movie','Episode')", lib).Scan(&count)
		a.db.Exec("UPDATE libraries SET status=?,error=?,count=?,duration=?,scanned=? WHERE id=?", status, message, count, time.Since(start).Seconds(), time.Now().Unix(), lib)
	}()
	previous, e := a.previousSnapshot(lib)
	if e != nil {
		scanErr = e
		return
	}
	current := fileSnapshot{}
	roots := a.libraryPaths(lib, root)
	for _, p := range roots {
		if fi, e := os.Stat(p); e != nil || !fi.IsDir() {
			scanErr = fmt.Errorf("媒体目录不可用，保留原索引")
			return
		}
	}
	walk := func(fn fs.WalkDirFunc) error {
		for _, p := range roots {
			root = p
			targets := []string{p}
			if len(scopes) > 0 {
				targets = nil
				for _, scope := range scopes {
					if scope == p || strings.HasPrefix(scope, p+"/") {
						targets = append(targets, scope)
					}
				}
			}
			for _, target := range targets {
				if _, err := os.Stat(target); os.IsNotExist(err) && len(scopes) > 0 {
					continue
				}
				if e := boundedWalk(target, fn); e != nil {
					return e
				}
			}
		}
		return nil
	}

	total := 0
	scanErr = walk(func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		a.waitScan(job)
		fi, e := d.Info()
		if e != nil {
			return e
		}
		current[path] = fileStamp{fi.Size(), fi.ModTime().UnixNano()}
		if strings.EqualFold(filepath.Ext(path), ".strm") {
			total++
		}
		return nil
	})
	if scanErr != nil {
		return
	}
	if len(scopes) > 0 {
		for path, stamp := range previous {
			if !withinScanScopes(path, scopes) {
				current[path] = stamp
			}
		}
	}
	a.changeActivity(job, func(v *activityEntry) { v.Total = total; v.State = "running" })
	var tx *Transaction
	var stmt *sql.Stmt
	batch := 0
	begin := func() error {
		var e error
		tx, e = a.db.Begin()
		if e != nil {
			return e
		}
		stmt, e = tx.Prepare(`INSERT INTO items(` + cols + `,seen) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET parent=excluded.parent,name=COALESCE((SELECT name FROM media_display_names WHERE item=excluded.id),excluded.name),kind=excluded.kind,url=excluded.url,overview=excluded.overview,poster=excluded.poster,year=excluded.year,season=excluded.season,episode=excluded.episode,mtime=excluded.mtime,size=excluded.size,seen=excluded.seen`)
		return e
	}
	flush := func() error {
		if stmt != nil {
			stmt.Close()
		}
		if tx == nil {
			return nil
		}
		e := tx.Commit()
		tx = nil
		batch = 0
		return e
	}
	defer func() {
		if stmt != nil {
			stmt.Close()
		}
		if tx != nil {
			tx.Rollback()
		}
	}()
	save := func(x Item) error {
		if tx == nil {
			if e := begin(); e != nil {
				return e
			}
		}
		_, e := stmt.Exec(x.ID, x.Lib, x.Parent, x.Name, x.Kind, x.Path, x.URL, x.Overview, x.Poster, x.Year, x.Season, x.Episode, x.Mtime, x.Size, gen)
		if e != nil {
			return e
		}
		batch++
		if batch >= 500 {
			return flush()
		}
		return nil
	}
	lastSeries, lastSeason := "", ""
	dirtyDirs := changedSidecars(previous, current)
	// A directory-sized working set, no full-library item map.
	scanErr = walk(func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".strm") {
			return nil
		}
		a.changeActivity(job, func(v *activityEntry) { v.Current = d.Name() })
		defer func() { a.changeActivity(job, func(v *activityEntry) { v.Done++ }) }()
		fi, e := d.Info()
		if e != nil {
			return e
		}
		x := Item{ID: digest(path)[:32], Lib: lib, Parent: lib, Name: strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())), Kind: "Movie", Path: path, Mtime: fi.ModTime().UnixNano(), Size: fi.Size()}
		a.waitScan(job)
		if (incremental || len(scopes) > 0) && previous[path] == current[path] && !dependencyChanged(path, dirtyDirs) {
			var exists int
			if e := a.db.QueryRow("SELECT 1 FROM items WHERE id=?", x.ID).Scan(&exists); e == nil {
				return nil
			} else if e != sql.ErrNoRows {
				return e
			}
		}
		if kind == "tvshows" {
			x.Kind = "Episode"
			if m := epPattern.FindStringSubmatch(x.Name); len(m) == 3 {
				x.Season, _ = strconv.Atoi(m[1])
				x.Episode, _ = strconv.Atoi(m[2])
			}
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			if rel != "." {
				seriesPath := seriesDirectory(root, path)
				series := Item{ID: digest(seriesPath)[:32], Lib: lib, Parent: lib, Name: filepath.Base(seriesPath), Kind: "Series", Path: seriesPath}
				if lastSeries != seriesPath {
					localInfo(filepath.Join(seriesPath, "tvshow.strm"), &series)
					if series.Poster == "" {
						series.Poster = seriesFallbackPoster(seriesPath)
					}
					if e := save(series); e != nil {
						return e
					}
					lastSeries = seriesPath
				}
				x.Parent = series.ID
				if filepath.Dir(path) != seriesPath {
					seasonPath := filepath.Dir(path)
					season := Item{ID: digest(seasonPath)[:32], Lib: lib, Parent: series.ID, Name: filepath.Base(seasonPath), Kind: "Season", Path: seasonPath, Season: x.Season, Episode: x.Season}
					if m := seasonDirPattern.FindStringSubmatch(filepath.Base(seasonPath)); len(m) == 2 {
						season.Season, _ = strconv.Atoi(m[1])
						season.Episode = season.Season
						if x.Season == 0 {
							x.Season = season.Season
						}
					}
					if lastSeason != seasonPath {
						localInfo(filepath.Join(seasonPath, "season.strm"), &season)
						season.Poster = seasonPoster(seasonPath, season.Season)
						if e := save(season); e != nil {
							return e
						}
						lastSeason = seasonPath
					}
					x.Parent = season.ID
				}
			}
		}
		// Reuse unchanged STRM payload; sidecar changes are picked up on refresh.

		f, e := os.Open(path)
		if e != nil {
			return e
		}
		b, e := io.ReadAll(io.LimitReader(f, 65537))
		f.Close()
		if e != nil {
			return e
		}
		if len(b) > 65536 {
			return fmt.Errorf("STRM 超过 64KB: %s", path)
		}
		for _, line := range strings.Split(strings.TrimPrefix(string(b), "\ufeff"), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				x.URL = line
				break
			}
		}
		localInfo(path, &x)
		return save(x)
	})
	if scanErr != nil {
		return
	}
	if scanErr = flush(); scanErr != nil {
		return
	}
	a.changeActivity(job, func(v *activityEntry) { v.State = "indexing"; v.Current = "索引本次更新的元数据" })
	if scanErr = a.indexMetadataGeneration(lib, gen, job); scanErr != nil {
		return
	}
	a.waitScan(job)
	a.changeActivity(job, func(v *activityEntry) { v.State = "cleaning"; v.Current = "提交文件快照并清理已删除条目" })
	scanErr = a.commitSnapshot(lib, current, allowEmpty, scopes)
	if scanErr == nil {
		scanErr = a.cleanupMedia()
	}

}

// Read directory entries in fixed batches instead of sorting a whole directory in RAM.
func boundedWalk(root string, fn fs.WalkDirFunc) error {
	info, e := os.Lstat(root)
	if e != nil {
		return fn(root, nil, e)
	}
	var visit func(string, fs.DirEntry) error
	visit = func(path string, d fs.DirEntry) error {
		if e := fn(path, d, nil); e != nil {
			if e == filepath.SkipDir && d.IsDir() {
				return nil
			}
			return e
		}
		if !d.IsDir() {
			return nil
		}
		f, e := os.Open(path)
		if e != nil {
			return fn(path, d, e)
		}
		defer f.Close()
		for {
			entries, e := f.ReadDir(256)
			for _, entry := range entries {
				if err := visit(filepath.Join(path, entry.Name()), entry); err != nil {
					return err
				}
			}
			if e == io.EOF {
				return nil
			}
			if e != nil {
				return fn(path, d, e)
			}
		}
	}
	return visit(root, fs.FileInfoToDirEntry(info))
}
