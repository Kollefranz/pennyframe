package main

import (
	"net/http"
	"strconv"
	"strings"
)

// imageHandler serves gallery images (/img/<id>) and cached thumbnails (/thumb/<id>) straight from disk.
func imageHandler(a *App) http.Handler {
	mux := http.NewServeMux()
	serve := func(thumb bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("id")
			if i := strings.IndexByte(id, '.'); i >= 0 {
				id = id[:i]
			}
			path, ok := a.gallery().Path(id)
			if !ok {
				http.NotFound(w, r)
				return
			}
			if thumb {
				want, _ := strconv.Atoi(r.URL.Query().Get("s"))
				if tp, err := a.gallery().Thumb(id, want); err == nil {
					path = tp
				}
			}
			w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
			http.ServeFile(w, r, path)
		}
	}
	mux.HandleFunc("/preview/{id}", func(w http.ResponseWriter, r *http.Request) {
		data, ok := a.previewBytes(r.PathValue("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(data))
		w.Header().Set("Cache-Control", "private, max-age=300") // URL carries a per-render counter
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/img/{id}", serve(false))
	mux.HandleFunc("/thumb/{id}", serve(true))
	return mux
}
