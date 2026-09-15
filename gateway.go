package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// NanShare can cache redirects without checking a viewer token. The public
// gateway authenticates and reserves a device BEFORE forwarding a video request.
// Its response guard never relays an upstream video body, even on fallback.
func (a *App) gateway() http.Handler { return a.gatewayFor("http://127.0.0.1:7791") }
func (a *App) gatewayFor(targetURL string) http.Handler {
	target, _ := url.Parse(targetURL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 40 * time.Second, MaxIdleConns: 100, MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second}
	proxy.ModifyResponse = func(res *http.Response) error {
		if isVideoRequest(res.Request.URL.Path) && res.StatusCode >= 200 && res.StatusCode < 300 {
			res.Body.Close()
			return errors.New("NanShare did not return a redirect; video relaying is disabled")
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		a.recordError(r, "NanShare 播放错误", "上游连接或响应失败: "+safeProxyError(e))
		fail(w, 502, "直链解析失败；已禁止服务器中转视频，请检查 NanShare 与视频源")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if done := a.beginProxyDebug(&w, r); done != nil {
			defer done()
		}
		if isVideoRequest(r.URL.Path) {
			u, e := a.auth(r)
			if e != nil {
				fail(w, 401, "请先登录")
				return
			}
			if !u.API {
				p := strings.TrimPrefix(strings.ToLower(r.URL.Path), "/emby")
				parts := strings.Split(p, "/")
				if len(parts) < 3 {
					fail(w, 400, "invalid video path")
					return
				}
				if _, e = a.item(parts[2]); e != nil {
					fail(w, 404, "媒体不存在")
					return
				}
				if e = a.reserve(u, parts[2]); e != nil {
					fail(w, 403, e.Error())
					return
				}
			}
		}
		proxy.ServeHTTP(w, r)
	})
}
func isVideoRequest(path string) bool {
	p := strings.ToLower(path)
	p = strings.TrimPrefix(p, "/emby")
	return strings.HasPrefix(p, "/videos/")
}

// Resolve once through NanShare; only redirects may leave this endpoint.
func (a *App) resolveNanShare(w http.ResponseWriter, r *http.Request) {
	target, e := url.Parse(os.Getenv("NANSHARE_URL"))
	if e != nil || target.Host == "" {
		fail(w, 502, "NanShare 地址无效")
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	fast := a.db == nil || a.defaultOn("fast_cdn")
	key := digest(target.String() + "|" + r.URL.String() + "|" + token(r) + "|" + r.UserAgent() + "|" + r.Header.Get("X-Real-IP"))
	if fast {
		cdnLinks.Lock()
		hit, ok := cdnLinks.entries[key]
		cdnLinks.Unlock()
		if ok && time.Now().Before(hit.until) {
			w.Header().Set("Location", hit.location)
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(hit.status)
			return
		}
		proxy.Transport = nanShareTransport
	} else {
		proxy.Transport = &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 40 * time.Second, DisableKeepAlives: true}
	}
	proxy.ModifyResponse = func(res *http.Response) error {
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			res.Body.Close()
			return errors.New("redirect required")
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		a.recordError(r, "NanShare 播放错误", "上游连接或响应失败: "+safeProxyError(e))
		fail(w, 502, "NanShare 直链解析失败")
	}
	guard := proxy.ModifyResponse
	proxy.ModifyResponse = func(res *http.Response) error {
		if err := guard(res); err != nil {
			return err
		}
		location := res.Header.Get("Location")
		u, err := url.Parse(location)
		if fast && err == nil && u.Scheme == "https" && u.Host != "" && (res.StatusCode == 302 || res.StatusCode == 307) && len(res.Cookies()) == 0 {
			cdnLinks.Lock()
			for k, v := range cdnLinks.entries {
				if time.Now().After(v.until) {
					delete(cdnLinks.entries, k)
				}
			}
			if len(cdnLinks.entries) < 512 {
				cdnLinks.entries[key] = cdnLink{location, res.StatusCode, time.Now().Add(5 * time.Second)}
			}
			cdnLinks.Unlock()
		}
		res.Header.Set("Cache-Control", "no-store")
		return nil
	}
	clone := r.Clone(r.Context())
	clone.Header.Set("X-Emby-Config-Id", "goembystrm2026")
	proxy.ServeHTTP(w, clone)
}

var nanShareTransport = &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 40 * time.Second, MaxIdleConns: 64, MaxIdleConnsPerHost: 16, IdleConnTimeout: 60 * time.Second}

type cdnLink struct {
	location string
	status   int
	until    time.Time
}

var cdnLinks = struct {
	sync.Mutex
	entries map[string]cdnLink
}{entries: map[string]cdnLink{}}

func safeProxyError(err error) string {
	var u *url.Error
	if errors.As(err, &u) {
		return u.Op + ": " + u.Err.Error()
	}
	return err.Error()
}
