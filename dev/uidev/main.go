// Command uidev is a zero-dependency dev server for the key-manager UI.
//
// It serves the UI's static files from disk (so edits show up immediately,
// unlike the embedded copy baked into the key-manager image), proxies /api/*
// to the running key-manager (normally a kubectl port-forward on :8080), and
// live-reloads the browser when any static file changes. Frontend devs edit
// key-manager/internal/ui/static/* and just refresh-free reload.
//
// Run via `make run-dev`, or directly:
//
//	go run . -static ../../key-manager/internal/ui/static -api http://localhost:8080 -addr :5173
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	staticDir := flag.String("static", "../../key-manager/internal/ui/static", "path to the UI static files")
	apiURL := flag.String("api", "http://localhost:8080", "key-manager API base URL to proxy /api/* to")
	addr := flag.String("addr", ":5173", "address to listen on")
	flag.Parse()

	absStatic, err := filepath.Abs(*staticDir)
	if err != nil || !dirExists(absStatic) {
		log.Fatalf("static dir not found: %s", *staticDir)
	}
	api, err := url.Parse(*apiURL)
	if err != nil {
		log.Fatalf("invalid -api URL %q: %v", *apiURL, err)
	}

	// Proxy /api/* to the key-manager (the dev-mode instance bypasses auth).
	proxy := httputil.NewSingleHostReverseProxy(api)

	mux := http.NewServeMux()
	mux.Handle("/api/", proxy)
	mux.HandleFunc("/__livereload", liveReloadHandler(absStatic))
	mux.HandleFunc("/", staticHandler(absStatic))

	fmt.Printf("UI dev server:    http://localhost%s  (hot reload)\n", normalizeAddr(*addr))
	fmt.Printf("  serving:        %s\n", absStatic)
	fmt.Printf("  /api/* proxied: %s\n", api)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// staticHandler serves files from dir. For HTML responses it injects a small
// live-reload script before </body> so edits trigger a browser reload.
func staticHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clean := filepath.Clean(r.URL.Path)
		if clean == "/" || clean == "." {
			clean = "/index.html"
		}
		full := filepath.Join(dir, clean)
		// Keep the response inside the static dir.
		if !strings.HasPrefix(full, dir) {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(clean, ".html") {
			b, err := os.ReadFile(full)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			body := injectLiveReload(string(b))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(body))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, full)
	}
}

const liveReloadScript = `<script>
(function () {
  var es = new EventSource("/__livereload");
  es.onmessage = function (e) { if (e.data === "reload") location.reload(); };
})();
</script>`

func injectLiveReload(html string) string {
	if i := strings.LastIndex(html, "</body>"); i >= 0 {
		return html[:i] + liveReloadScript + html[i:]
	}
	return html + liveReloadScript
}

// liveReloadHandler streams a reload event whenever the static dir changes.
// It polls a cheap fingerprint (file count + sizes + max mtime) so it needs no
// third-party file-watching dependency.
func liveReloadHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		last := fingerprint(dir)
		ticker := time.NewTicker(400 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if fp := fingerprint(dir); fp != last {
					last = fp
					fmt.Fprint(w, "data: reload\n\n")
					flusher.Flush()
				}
			}
		}
	}
}

// fingerprint returns a string that changes whenever a file under dir is added,
// removed, resized, or modified.
func fingerprint(dir string) string {
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", path, info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	return b.String()
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func normalizeAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return addr
	}
	return ":" + addr
}
