package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// R83: «Хочу пользоваться полностью платформой в мобильной версии».
//
// The platform installs on a phone as an app (PWA): a manifest with the BS
// icons and a «Быстрая заметка» shortcut (a long press on the icon), and a
// service worker that keeps only the shell: the offline page and the icons.
// Pages are always taken from the network (they are private and per
// session), /api/ is never touched: no club data is stored by the worker.

//go:embed pwa
var pwaFS embed.FS

const pwaManifest = `{
  "name": "Business Surgery",
  "short_name": "BS",
  "id": "/?pwa",
  "start_url": "/?src=pwa",
  "scope": "/",
  "display": "standalone",
  "orientation": "portrait",
  "background_color": "#050505",
  "theme_color": "#050505",
  "lang": "ru",
  "description": "Платформа клуба Business Surgery: разборы, задачи, CRM и быстрые заметки",
  "icons": [
    {"src": "/pwa/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
    {"src": "/pwa/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
    {"src": "/pwa/maskable-192.png", "sizes": "192x192", "type": "image/png", "purpose": "maskable"},
    {"src": "/pwa/maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"}
  ],
  "shortcuts": [
    {"name": "Быстрая заметка", "short_name": "Заметка", "url": "/?note=1",
     "icons": [{"src": "/pwa/note-96.png", "sizes": "96x96", "type": "image/png"}]}
  ]
}`

// The worker: the shell only. Bump pwaShellVersion when the cached files change.
const pwaShellVersion = "bs-shell-r83-1"

const pwaWorker = `/* Business Surgery: service worker (R83). Only the shell is cached:
   the offline page and the icons. Pages always come from the network,
   /api/ is never cached: the club's data stays on the server. */
var V = '` + pwaShellVersion + `';
var SHELL = ['/pwa/offline.html', '/pwa/icon-192.png', '/pwa/icon-512.png', '/pwa/note-96.png'];
self.addEventListener('install', function(e){
  e.waitUntil(caches.open(V).then(function(c){ return c.addAll(SHELL); }).then(function(){ return self.skipWaiting(); }));
});
self.addEventListener('activate', function(e){
  e.waitUntil(caches.keys().then(function(ks){
    return Promise.all(ks.filter(function(k){ return k.indexOf('bs-shell') === 0 && k !== V; }).map(function(k){ return caches.delete(k); }));
  }).then(function(){ return self.clients.claim(); }));
});
self.addEventListener('fetch', function(e){
  var r = e.request;
  if(r.method !== 'GET') return;
  var u = new URL(r.url);
  if(u.origin !== self.location.origin || u.pathname.indexOf('/api/') === 0) return;
  if(r.mode === 'navigate'){
    e.respondWith(fetch(r).catch(function(){ return caches.match('/pwa/offline.html'); }));
    return;
  }
  if(u.pathname.indexOf('/pwa/') === 0){
    e.respondWith(caches.match(r).then(function(x){ return x || fetch(r); }));
  }
});
`

const pwaOffline = `<!doctype html>
<html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#050505"><title>Нет связи · Business Surgery</title>
<style>
html,body{margin:0;background:#050505;color:#fff;font:16px/1.5 -apple-system,'Segoe UI',Manrope,Arial,sans-serif}
main{min-height:100vh;display:flex;flex-direction:column;align-items:center;justify-content:center;padding:24px 16px;text-align:center;gap:14px}
img{width:72px;height:72px;border-radius:18px}
h1{font-size:22px;margin:6px 0 0}
p{color:#c9c9c9;margin:0;max-width:340px}
button{margin-top:10px;min-height:48px;padding:0 26px;border-radius:999px;border:0;background:#fff;color:#050505;font:700 16px/1 inherit;cursor:pointer}
</style></head>
<body><main>
<img src="/pwa/icon-192.png" alt="">
<h1>Нет связи с интернетом</h1>
<p>Платформа откроется, как только появится сеть. Данные клуба хранятся на сервере, на телефоне их нет.</p>
<button type="button" onclick="location.reload()">Обновить</button>
</main></body></html>
`

func pwaETag(b []byte) string {
	s := sha256.Sum256(b)
	return `"` + hex.EncodeToString(s[:8]) + `"`
}

func servePWAText(c *gin.Context, ctype, cache string, body []byte) {
	tag := pwaETag(body)
	h := c.Writer.Header()
	h.Set("Cache-Control", cache)
	h.Set("ETag", tag)
	h.Set("X-Content-Type-Options", "nosniff")
	if c.GetHeader("If-None-Match") == tag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, ctype, body)
}

func servePWAFile(c *gin.Context) {
	name := c.Param("file")
	if name == "offline.html" {
		servePWAText(c, "text/html; charset=utf-8", "no-cache", []byte(pwaOffline))
		return
	}
	if name == "" || strings.ContainsAny(name, "/\\") || path.Ext(name) != ".png" {
		c.Status(http.StatusNotFound)
		return
	}
	b, err := pwaFS.ReadFile("pwa/" + name)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	servePWAText(c, "image/png", "public, max-age=604800", b)
}

// RegisterPWA: /manifest.webmanifest, /sw.js, /pwa/<file>.
func RegisterPWA(r *gin.Engine) {
	r.GET("/manifest.webmanifest", func(c *gin.Context) {
		servePWAText(c, "application/manifest+json; charset=utf-8", "public, max-age=3600", []byte(pwaManifest))
	})
	r.GET("/sw.js", func(c *gin.Context) {
		c.Header("Service-Worker-Allowed", "/")
		servePWAText(c, "text/javascript; charset=utf-8", "no-cache", []byte(pwaWorker))
	})
	r.GET("/pwa/:file", servePWAFile)
	r.HEAD("/pwa/:file", servePWAFile)
}

var (
	scriptsDefOnce sync.Once
	scriptsDef     string
)

// ScriptsDefJS: the platform's default sales scripts (var SCRIPTS_DEF in
// platform.html) as a JS literal, for the manager's CRM page.
func ScriptsDefJS() string {
	scriptsDefOnce.Do(func() {
		s := string(platformHTML)
		const start, end = "var SCRIPTS_DEF = ", "\n];\nvar SCRIPTS = null;"
		i := strings.Index(s, start)
		if i < 0 {
			scriptsDef = "[]"
			return
		}
		j := strings.Index(s[i:], end)
		if j < 0 {
			scriptsDef = "[]"
			return
		}
		scriptsDef = s[i+len(start):i+j] + "\n]"
	})
	return scriptsDef
}
