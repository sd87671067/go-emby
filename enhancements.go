package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) defaultOn(key string) bool {
	var value string
	return a.db.QueryRow("SELECT v FROM settings WHERE k=?", key).Scan(&value) != nil || value != "false"
}
func (a *App) hideMissingActors() bool { return a.defaultOn("hide_missing_actor_images") }
func (a *App) enhancementValues() M {
	return M{"ProxyDebug": a.proxyDebugEnabled(), "ThirdPartyProxyPorts": a.thirdPartyProxyPorts(), "FastCDN": a.defaultOn("fast_cdn"), "WatchEnabled": a.defaultOn("watch_enabled"), "ScanConcurrency": a.jobLimit(false), "UpdateConcurrency": a.jobLimit(true), "ServerName": a.displayName(), "WatchDelaySeconds": a.watchDelay(), "SearchByInitials": a.defaultOn("search_by_initials"), "HideMissingActorImages": a.hideMissingActors(), "MergeVersionsInFolder": a.defaultOn("merge_versions_folder"), "MergeVersionsAcrossLibraries": a.defaultOn("merge_versions_libraries")}
}
func (a *App) enhancementSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		respond(w, a.enhancementValues())
		return
	}
	if r.Method != "PUT" {
		fail(w, 405, "PUT required")
		return
	}
	var b struct {
		ProxyDebug                                                                                                           *bool
		ThirdPartyProxyPorts                                                                                                 *[]int
		ScanConcurrency, UpdateConcurrency                                                                                   *int
		ServerName                                                                                                           *string
		FastCDN, WatchEnabled, SearchByInitials, HideMissingActorImages, MergeVersionsInFolder, MergeVersionsAcrossLibraries *bool
		WatchDelaySeconds                                                                                                    *int
	}
	if !body(w, r, &b) {
		return
	}
	if b.ProxyDebug == nil && b.ThirdPartyProxyPorts == nil && b.FastCDN == nil && b.WatchEnabled == nil && b.SearchByInitials == nil && b.HideMissingActorImages == nil && b.MergeVersionsInFolder == nil && b.MergeVersionsAcrossLibraries == nil && b.WatchDelaySeconds == nil && b.ScanConcurrency == nil && b.UpdateConcurrency == nil && b.ServerName == nil {
		fail(w, 400, "缺少增强功能设置")
		return
	}
	if b.WatchDelaySeconds != nil && (*b.WatchDelaySeconds < 10 || *b.WatchDelaySeconds > 86400) {
		fail(w, 400, "监听延时范围 10–86400 秒")
		return
	}
	for _, n := range []*int{b.ScanConcurrency, b.UpdateConcurrency} {
		if n != nil && (*n < 1 || *n > 64) {
			fail(w, 400, "并发数量范围 1–64")
			return
		}
	}
	if b.ServerName != nil {
		*b.ServerName = strings.TrimSpace(*b.ServerName)
		if len(*b.ServerName) == 0 || len(*b.ServerName) > 128 {
			fail(w, 400, "服务器名称需要 1–128 字节")
			return
		}
	}
	if b.ThirdPartyProxyPorts != nil {
		seen := map[int]bool{}
		if len(*b.ThirdPartyProxyPorts) > 32 {
			fail(w, 400, "最多添加32个反代端口")
			return
		}
		for _, port := range *b.ThirdPartyProxyPorts {
			if port < 1 || port > 65535 || port == 8097 || seen[port] {
				fail(w, 400, "端口必须在1–65535之间，不能重复或使用服务端口8097")
				return
			}
			seen[port] = true
		}
	}
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, "保存失败")
		return
	}
	defer tx.Rollback()
	for k, v := range map[string]*bool{"proxy_debug": b.ProxyDebug, "fast_cdn": b.FastCDN, "watch_enabled": b.WatchEnabled, "search_by_initials": b.SearchByInitials, "hide_missing_actor_images": b.HideMissingActorImages, "merge_versions_folder": b.MergeVersionsInFolder, "merge_versions_libraries": b.MergeVersionsAcrossLibraries} {
		if v == nil {
			continue
		}
		value := "false"
		if *v {
			value = "true"
		}
		if _, e = tx.Exec("INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", k, value); e != nil {
			fail(w, 500, "保存失败")
			return
		}
	}
	if b.WatchDelaySeconds != nil {
		if _, e = tx.Exec("INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", "watch_delay_seconds", strconv.Itoa(*b.WatchDelaySeconds)); e != nil {
			fail(w, 500, "保存失败")
			return
		}
	}
	for k, n := range map[string]*int{"scan_concurrency": b.ScanConcurrency, "update_concurrency": b.UpdateConcurrency} {
		if n != nil {
			if _, e = tx.Exec("INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", k, strconv.Itoa(*n)); e != nil {
				fail(w, 500, "保存失败")
				return
			}
		}
	}
	if b.ServerName != nil {
		if _, e = tx.Exec("INSERT INTO settings(k,v) VALUES('server_name',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", *b.ServerName); e != nil {
			fail(w, 500, "保存失败")
			return
		}
	}
	if b.ThirdPartyProxyPorts != nil {
		ports, _ := json.Marshal(*b.ThirdPartyProxyPorts)
		if _, e = tx.Exec("INSERT INTO settings(k,v) VALUES('third_party_proxy_ports',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v", string(ports)); e != nil {
			fail(w, 500, "保存失败")
			return
		}
	}
	if e = tx.Commit(); e != nil {
		fail(w, 500, "保存失败")
		return
	}
	a.wakeLibraryJobs()
	respond(w, a.enhancementValues())
}
