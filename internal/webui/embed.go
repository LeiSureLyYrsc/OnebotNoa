// Package webui serves the embedded management UI.
//
// The UI is built by Vite into internal/webui/dist and embedded at compile time.
// Only a placeholder file is tracked in git, so a checkout without a frontend
// build still compiles and serves a small built-in fallback page instead of
// failing the build.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler serves the built UI with SPA fallback, or the built-in fallback page
// when the frontend has not been built.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return http.HandlerFunc(serveFallback)
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return http.HandlerFunc(serveFallback)
	}

	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/"), "./")
		if name == "" {
			serveIndex(w, r, sub)
			return
		}
		f, err := sub.Open(name)
		if err != nil {
			// Unknown path: hand it to the SPA router.
			serveIndex(w, r, sub)
			return
		}
		_ = f.Close()
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	body, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		serveFallback(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

const fallbackHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>OnebotNoa</title>
<style>
  body { margin: 0; padding: 2.5rem; background: #3a6ea5; color: #fff;
         font-family: "Segoe UI", system-ui, sans-serif; }
  .window { max-width: 720px; margin: 0 auto; background: #ece9d8; color: #000;
            border: 1px solid #0831d9; border-radius: 8px 8px 0 0; box-shadow: 0 8px 32px rgba(0,0,0,.4); }
  .title-bar { display: flex; align-items: center; justify-content: space-between;
               padding: .35rem .6rem; color: #fff; font-weight: 700;
               background: linear-gradient(180deg,#0058ee 0%,#3593ff 8%,#288eff 40%,#0864e3 88%,#0054e3 100%);
               border-radius: 6px 6px 0 0; }
  .title-bar .buttons span { display:inline-block; width:18px; height:18px; margin-left:3px; text-align:center;
               background: linear-gradient(180deg,#3f8cf3,#0d47a1); border:1px solid #fff; border-radius:3px; font-size:12px; }
  .body { padding: 1rem 1.25rem 1.5rem; }
  code { background:#fff; border:1px solid #b5b3a8; padding:0 .25rem; }
  .status { margin-top:1rem; padding:.4rem .6rem; background:#ece9d8; border-top:1px solid #b5b3a8; font-size:.85rem; }
</style>
</head>
<body>
  <div class="window">
    <div class="title-bar"><span>OnebotNoa — 管理端</span>
      <span class="buttons"><span>_</span><span>□</span><span>×</span></span></div>
    <div class="body">
      <p><strong>WebUI 尚未构建</strong>，当前显示内置兜底页；后端已正常运行。</p>
      <p>构建前端：<code>pwsh -File scripts/build-web.ps1</code>，然后重新 <code>go build</code>。</p>
      <p>开发模式：<code>pwsh -File scripts/dev.ps1 -Web</code>（Vite 开发服务器 :5173 代理到本进程）。</p>
      <p>健康检查：<code>GET /healthz</code>；管理 API 前缀：<code>/api/v1</code>。</p>
    </div>
    <div class="status">OneBot V11 中继 · 单二进制部署</div>
  </div>
</body>
</html>
`

func serveFallback(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(fallbackHTML))
}
