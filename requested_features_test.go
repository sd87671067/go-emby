package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompleteSeriesEpisodesAndExplicitPages(t *testing.T) {
	a, _, dir := catalogFixture(t)
	for i := 3; i <= 684; i++ {
		_, e := a.db.Exec("INSERT INTO items(id,lib,parent,name,kind,path,url,season,episode,seen) VALUES(?,'l','s1',?,'Episode',?,'https://example.invalid/video',1,?,'g')", fmt.Sprint("ep", i), fmt.Sprint(i), filepath.Join(dir, fmt.Sprintf("%d.strm", i)), i)
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, tc := range []struct {
		path string
		n    int
	}{{"/Shows/s/Episodes", 684}, {"/Items?ParentId=s1", 683}, {"/Shows/s/Episodes?Limit=500", 500}, {"/Shows/s/Episodes?Limit=60&StartIndex=60", 60}} {
		w := browseGet(a, tc.path, true)
		var v struct {
			Items            []M
			TotalRecordCount int
		}
		if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil || w.Code != 200 || len(v.Items) != tc.n {
			t.Fatalf("%s HTTP %d got %d: %s", tc.path, w.Code, len(v.Items), w.Body.String())
		}
	}
}
func TestRedirectLogSuccessFailureAndNoNetwork(t *testing.T) {
	a := &App{}
	for _, status := range []int{302, 502} {
		w := httptest.NewRecorder()
		var rw http.ResponseWriter = w
		r := httptest.NewRequest("GET", "/emby/Videos/x/stream?api_key=secret", nil)
		done := a.beginRedirectLog(&rw, r)
		start := time.Now()
		if status == 302 {
			rw.Header().Set("Location", "https://unreachable.invalid/play?token=private")
			rw.WriteHeader(status)
		} else {
			fail(rw, status, "解析上游失败")
		}
		done()
		if time.Since(start) > time.Second {
			t.Fatal("logging blocked")
		}
		v := a.activity.entries[len(a.activity.entries)-1]
		if v.Category != "redirect" || strings.Contains(v.Current, "private") || strings.Contains(v.Current, "secret") {
			t.Fatal(v)
		}
		if status == 502 && (v.State != "error" || !strings.Contains(v.Current, "解析上游失败")) {
			t.Fatal(v)
		}
		if !w.Flushed {
			t.Fatal("response not flushed before log")
		}
	}
}
func TestKeeperRestoreAndAmbiguousSafety(t *testing.T) {
	a := testApp(t)
	x := probeFixture(t, a)
	dir := a.probeSettings().Directory
	os.MkdirAll(dir, 0700)
	b := []byte(`[{"MediaSourceInfo":{"Container":"mkv","RunTimeTicks":123000000,"MediaStreams":[{"Type":"Video","Codec":"h264"}]}}]`)
	if _, e := keeperMedia(b); e != nil {
		t.Fatal(e)
	}
	if _, e := keeperMedia([]byte(`{"MediaStreams":[]}`)); e == nil {
		t.Fatal("accepted empty streams")
	}
	os.WriteFile(filepath.Join(dir, "video.mp4-mediainfo.json"), b, 0600)
	w := httptest.NewRecorder()
	a.restoreMediaInfo(w, httptest.NewRequest("POST", "/admin/media-info/restore", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"Imported":1`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if a.cachedMedia(x)["Container"] != "mkv" {
		t.Fatal("not cached")
	}
	w = httptest.NewRecorder()
	a.restoreMediaInfo(w, httptest.NewRequest("POST", "/admin/media-info/restore", nil))
	if !strings.Contains(w.Body.String(), `"Imported":0`) {
		t.Fatal(w.Body.String())
	}
}
func TestMediaRenameAndDeleteSafety(t *testing.T) {
	a := testApp(t)
	x := probeFixture(t, a)
	root := filepath.Dir(x.Path)
	t.Setenv("MEDIA_ROOTS", root)
	request := func(method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.manageMediaItem(w, httptest.NewRequest(method, "/admin/media-item", strings.NewReader(body)))
		return w
	}
	w := request("PUT", fmt.Sprintf(`{"ID":%q,"Name":"新的显示名称"}`, x.ID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var name string
	a.db.QueryRow("SELECT name FROM media_display_names WHERE item=?", x.ID).Scan(&name)
	if name != "新的显示名称" {
		t.Fatal(name)
	}
	w = request("DELETE", fmt.Sprintf(`{"ID":%q,"DeleteDirectory":true}`, x.ID))
	if w.Code != 400 {
		t.Fatal("accepted root deletion", w.Code, w.Body.String())
	}
	if _, e := os.Stat(x.Path); e != nil {
		t.Fatal(e)
	}
	w = request("DELETE", fmt.Sprintf(`{"ID":%q}`, x.ID))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, e := os.Stat(x.Path); !os.IsNotExist(e) {
		t.Fatal("file survived")
	}
	if _, e := a.item(x.ID); e == nil {
		t.Fatal("index survived")
	}
}
