package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (a *App) manageMediaItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" && r.Method != "DELETE" {
		fail(w, 405, "PUT or DELETE required")
		return
	}
	var b struct {
		ID, Name        string
		DeleteDirectory bool
	}
	if !body(w, r, &b) {
		return
	}
	x, e := a.item(b.ID)
	if e != nil {
		fail(w, 404, "媒体不存在")
		return
	}
	if r.Method == "PUT" {
		b.Name = strings.TrimSpace(b.Name)
		if b.Name == "" || len([]rune(b.Name)) > 256 {
			fail(w, 400, "名称长度必须为1–256字")
			return
		}
		tx, err := a.db.Begin()
		if err != nil {
			fail(w, 500, "保存失败")
			return
		}
		defer tx.Rollback()
		_, e = tx.Exec("INSERT INTO media_display_names(item,name) VALUES(?,?) ON CONFLICT(item) DO UPDATE SET name=excluded.name", x.ID, b.Name)
		if e == nil {
			_, e = tx.Exec("UPDATE items SET name=? WHERE id=?", b.Name, x.ID)
		}
		if e == nil {
			e = tx.Commit()
		}
		if e != nil {
			fail(w, 500, "保存失败")
			return
		}
		respond(w, M{"ok": true})
		return
	}
	rows, e := a.db.Query("WITH RECURSIVE tree AS (SELECT "+cols+" FROM items WHERE id=? UNION ALL SELECT "+prefixedCols("i")+" FROM items i JOIN tree t ON i.parent=t.id) SELECT "+cols+" FROM tree", x.ID)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	items := []Item{}
	for rows.Next() {
		v, err := readItem(rows)
		if err != nil {
			rows.Close()
			fail(w, 500, err.Error())
			return
		}
		items = append(items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	targets := []string{}
	if b.DeleteDirectory {
		p := x.Path
		if x.URL != "" {
			p = filepath.Dir(p)
		}
		targets = append(targets, p)
	} else {
		for _, v := range items {
			if v.URL != "" {
				if !strings.EqualFold(filepath.Ext(v.Path), ".strm") {
					fail(w, 400, "仅允许删除 STRM")
					return
				}
				targets = append(targets, v.Path)
			}
		}
	}
	// Validate every target before removing anything; never delete a library root or shared directory.
	for _, p := range targets {
		real, err := filepath.EvalSymlinks(p)
		if os.IsNotExist(err) && !b.DeleteDirectory {
			continue
		}
		if err != nil || !allowedMediaPath(real) {
			fail(w, 400, "路径无效或超出媒体目录")
			return
		}
		for _, root := range mediaRoots() {
			if real == root {
				fail(w, 400, "不能删除媒体挂载根目录")
				return
			}
		}
		if b.DeleteDirectory {
			for _, l := range a.libraries() {
				for _, root := range l["Locations"].([]string) {
					if root == real || strings.HasPrefix(root, real+"/") {
						fail(w, 400, "不能删除媒体库根目录")
						return
					}
				}
			}
			var count int
			selected := map[string]bool{}
			for _, v := range items {
				selected[v.ID] = true
			}
			rs, err := a.db.Query("SELECT id,path FROM items WHERE path=? OR left(path,char_length(?))=?", real, real+"/", real+"/")
			if err != nil {
				fail(w, 500, err.Error())
				return
			}
			for rs.Next() {
				var id, path string
				rs.Scan(&id, &path)
				if !selected[id] {
					count++
				}
			}
			err = rs.Err()
			rs.Close()
			if err != nil || count > 0 {
				fail(w, 400, "目录包含其他媒体，请取消连同目录删除")
				return
			}
		}
	}
	for _, p := range targets {
		if b.DeleteDirectory {
			e = os.RemoveAll(p)
		} else {
			e = os.Remove(p)
		}
		if e != nil && !os.IsNotExist(e) {
			fail(w, 500, fmt.Sprintf("删除文件失败: %v", e))
			return
		}
	}
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer tx.Rollback()
	for i := len(items) - 1; i >= 0; i-- {
		if _, e = tx.Exec("DELETE FROM items WHERE id=?", items[i].ID); e != nil {
			fail(w, 500, e.Error())
			return
		}
	}
	if e = tx.Commit(); e != nil {
		fail(w, 500, e.Error())
		return
	}
	respond(w, M{"ok": true})
}
func prefixedCols(prefix string) string {
	parts := strings.Split(cols, ",")
	for i := range parts {
		parts[i] = prefix + "." + strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ",")
}
