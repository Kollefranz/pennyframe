package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"pennyframe/internal/store"
)

// imageHandler serves gallery images (/img/<id>) and cached thumbnails (/thumb/<id>?s=<px>).
// Unencrypted images stream straight from disk. Encrypted ones are decrypted in memory. Everything is
// sent with no-store so nothing readable is cached anywhere.
func imageHandler(a *App) http.Handler {
	mux := http.NewServeMux()
	serve := func(thumb bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("id")
			if i := strings.IndexByte(id, '.'); i >= 0 {
				id = id[:i]
			}
			// Never cacheable: a copy cached while an image was unencrypted would outlive moving it into an
			// encrypted folder. The files are local, so serving them again is cheap.
			w.Header().Set("Cache-Control", "no-store")
			want, _ := strconv.Atoi(r.URL.Query().Get("s"))
			g := a.gallery()
			m, ok := g.Get(id)
			if !ok {
				http.NotFound(w, r)
				return
			}
			if m.Encrypted {
				var data []byte
				var err error
				if thumb {
					data, err = g.ThumbBytes(id, want)
				}
				if !thumb || errors.Is(err, store.ErrNoThumb) {
					data, err = g.ReadImage(id)
				}
				switch {
				case errors.Is(err, store.ErrLocked):
					http.Error(w, "locked", http.StatusForbidden)
				case err != nil:
					http.Error(w, "unreadable", http.StatusInternalServerError)
				default:
					w.Header().Set("Content-Type", store.SniffMediaType(data))
					_, _ = w.Write(data)
				}
				return
			}
			path, ok := g.Path(id)
			if !ok {
				http.NotFound(w, r)
				return
			}
			if thumb {
				if tp, err := g.Thumb(id, want); err == nil {
					path = tp
				}
			}
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
