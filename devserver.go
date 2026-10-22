//go:build devserver

// Browser harness: `go run -tags devserver .` serves the UI over HTTP so it can be driven
// in a normal browser. Methods bound to the desktop shell are exposed as POST /rpc/<Method>.
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"pennyframe/internal/store"
)

//go:embed all:frontend
var assets embed.FS

const shim = `<script>
window.go = { main: { App: new Proxy({}, { get: (_, name) => (...args) =>
  fetch('/rpc/' + name, { method: 'POST', body: JSON.stringify(args) }).then(async (r) => {
    const j = await r.json(); if (j.error) throw j.error; return j.result; }) }) } };
const es = new EventSource('/events');
window.runtime = { EventsOn: (n, cb) => es.addEventListener(n, (e) => cb(JSON.parse(e.data))), BrowserOpenURL: (u) => window.open(u) };
</script>`

func main() {
	dir := os.Getenv("PENNYFRAME_DATA_DIR")
	if dir == "" {
		log.Fatal("devserver requires PENNYFRAME_DATA_DIR (it must not touch your real profile)")
	}
	cfg, err := store.LoadConfig(dir, store.OSSecrets())
	if err != nil {
		log.Fatal(err)
	}
	if cfg.OutputDir() == store.DefaultOutputDir() {
		_ = cfg.SetOutputDir(filepath.Join(dir, "images"))
	}
	gal, err := store.OpenGallery(cfg.OutputDir(), filepath.Join(dir, "thumbs"))
	if err != nil {
		log.Fatal(err)
	}
	app := NewApp(cfg, gal)

	var mu sync.Mutex
	subs := map[chan string]bool{}
	app.emit = func(ev string, data any) {
		b, _ := json.Marshal(data)
		msg := fmt.Sprintf("event: %s\ndata: %s\n\n", ev, b)
		mu.Lock()
		defer mu.Unlock()
		for c := range subs {
			select {
			case c <- msg:
			default:
			}
		}
	}

	root, _ := fs.Sub(assets, "frontend")
	files := http.FileServer(http.FS(root))
	mux := http.NewServeMux()
	mux.Handle("/img/", imageHandler(app))
	mux.Handle("/thumb/", imageHandler(app))
	mux.Handle("/preview/", imageHandler(app))
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := fs.ReadFile(root, "index.html")
		html := strings.Replace(string(b), `<script src="app.js">`, shim+`<script src="app.js">`, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, html)
	})
	mux.Handle("/", files)
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		c := make(chan string, 64)
		mu.Lock()
		subs[c] = true
		mu.Unlock()
		defer func() { mu.Lock(); delete(subs, c); mu.Unlock() }()
		fl := w.(http.Flusher)
		fmt.Fprint(w, ": hi\n\n")
		fl.Flush()
		for {
			select {
			case m := <-c:
				fmt.Fprint(w, m)
				fl.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("POST /rpc/{name}", func(w http.ResponseWriter, r *http.Request) {
		reply := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
		m := reflect.ValueOf(app).MethodByName(r.PathValue("name"))
		if !m.IsValid() {
			reply(map[string]any{"error": "no such method"})
			return
		}
		var raw []json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&raw)
		t := m.Type()
		if len(raw) != t.NumIn() {
			reply(map[string]any{"error": fmt.Sprintf("want %d args, got %d", t.NumIn(), len(raw))})
			return
		}
		in := make([]reflect.Value, t.NumIn())
		for i := range in {
			p := reflect.New(t.In(i))
			if err := json.Unmarshal(raw[i], p.Interface()); err != nil {
				reply(map[string]any{"error": err.Error()})
				return
			}
			in[i] = p.Elem()
		}
		out := m.Call(in)
		var result any
		for _, o := range out {
			if e, ok := o.Interface().(error); ok && e != nil {
				reply(map[string]any{"error": e.Error()})
				return
			}
			if o.Type() != reflect.TypeOf((*error)(nil)).Elem() {
				result = o.Interface()
			}
		}
		reply(map[string]any{"result": result})
	})
	addr := "127.0.0.1:8800"
	log.Println("devserver on http://" + addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
