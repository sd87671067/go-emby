package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func keeperMedia(b []byte) (M, error) {
	var raw any
	if e := json.Unmarshal(b, &raw); e != nil {
		return nil, e
	}
	if list, ok := raw.([]any); ok {
		if len(list) != 1 {
			return nil, fmt.Errorf("需要唯一媒体源")
		}
		raw = list[0]
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("无效媒体信息")
	}
	if v, ok := m["MediaSourceInfo"].(map[string]any); ok {
		m = v
	} else if v, ok := m["MediaSources"].([]any); ok && len(v) == 1 {
		m, _ = v[0].(map[string]any)
	}
	streams, ok := m["MediaStreams"].([]any)
	if !ok || len(streams) == 0 {
		return nil, fmt.Errorf("缺少媒体流")
	}
	result := M{}
	for _, k := range []string{"MediaStreams", "Container", "Size", "RunTimeTicks", "Bitrate", "Chapters"} {
		if v, ok := m[k]; ok {
			result[k] = v
		}
	}
	return result, nil
}
func (a *App) restoreMediaInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "POST required")
		return
	}
	dir := a.probeSettings().Directory
	imported, skipped, failed := 0, 0, 0
	// Match the catalog once per restore, avoiding a full table scan for every JSON.
	candidates := map[string][]Item{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		base := strings.TrimSuffix(strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())), "-mediainfo")
		candidates[base] = nil
		return nil
	})
	if err != nil {
		fail(w, 500, "读取媒体信息目录失败: "+err.Error())
		return
	}
	rows, e := a.db.Query("SELECT " + cols + " FROM items WHERE url<>''")
	if e != nil {
		fail(w, 500, "读取媒体索引失败")
		return
	}
	for rows.Next() {
		x, e := readItem(rows)
		if e != nil {
			rows.Close()
			fail(w, 500, "读取媒体索引失败")
			return
		}
		base := strings.TrimSuffix(filepath.Base(x.Path), filepath.Ext(x.Path))
		if list, ok := candidates[base]; ok && len(list) < 2 {
			candidates[base] = append(list, x)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		fail(w, 500, "读取媒体索引失败")
		return
	}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		st, e := d.Info()
		if e != nil || st.Size() > 8<<20 {
			failed++
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			failed++
			return nil
		}
		var own mediaRecord
		if json.Unmarshal(b, &own) == nil && own.ItemID != "" {
			skipped++
			return nil
		}
		m, e := keeperMedia(b)
		if e != nil {
			failed++
			return nil
		}
		base := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		base = strings.TrimSuffix(base, "-mediainfo")
		matches := candidates[base]
		if len(matches) != 1 {
			skipped++
			return nil
		}
		x := matches[0]
		if cached := a.cachedMedia(x); len(cached) > 0 && cached["Partial"] != true {
			skipped++
			return nil
		}
		if e = a.saveMedia(x, m); e != nil {
			failed++
		} else {
			imported++
		}
		return nil
	})
	if err != nil {
		fail(w, 500, "恢复失败: "+err.Error())
		return
	}
	respond(w, M{"Imported": imported, "Skipped": skipped, "Failed": failed})
}
