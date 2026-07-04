// Closure-test adapter for ntm (the dumb translator).
//
// Belt: a saved session surfaces in `sessions list`.
//   SOURCE     an in-memory session.SessionState (the intent to save)
//   connector  session.Save  (the REAL filesystem persist)
//   OBSERVE    session.List  (the REAL list read)
// Withholding /drive persist leaves the store empty -> List returns nothing
// though the "session" existed (red-at-seam). Pure filesystem, no tmux/CGO.
//
// Run: go run ./cmd/closure-adapter <port>
package main

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/session"
)

var (
	connectorEnabled = true
	pending          *session.SessionState
)

func writeJSON(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}
func readBody(r *http.Request) map[string]any {
	var m map[string]any
	json.NewDecoder(r.Body).Decode(&m)
	return m
}

func main() {
	port := "8993"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	http.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		tmp, _ := os.MkdirTemp("", "ntm-closure-*") // fresh isolated ~/.ntm store
		os.Setenv("HOME", tmp)
		connectorEnabled = true
		pending = nil
		writeJSON(w, map[string]any{"ok": true})
	})
	http.HandleFunc("/set_connector", func(w http.ResponseWriter, r *http.Request) {
		b := readBody(r)
		if b["edge"] == "persist" {
			connectorEnabled, _ = b["enabled"].(bool)
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	http.HandleFunc("/seed", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	})
	http.HandleFunc("/trigger", func(w http.ResponseWriter, r *http.Request) {
		pending = &session.SessionState{ // SOURCE built in memory (no tmux needed)
			Name: "belt-session", SavedAt: time.Now(), WorkDir: "/tmp",
			Layout: "tiled", Version: session.StateVersion,
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	http.HandleFunc("/drive", func(w http.ResponseWriter, r *http.Request) {
		if readBody(r)["edge"] == "persist" && connectorEnabled && pending != nil {
			session.Save(pending, session.SaveOptions{Name: "belt-session", Overwrite: true}) // REAL connector
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	http.HandleFunc("/observe", func(w http.ResponseWriter, r *http.Request) {
		list, _ := session.List() // REAL production read
		names := []string{}
		for _, s := range list {
			names = append(names, s.Name)
		}
		val, _ := json.Marshal(names)
		writeJSON(w, map[string]any{"ok": true, "value": string(val)})
	})

	os.Stderr.WriteString("ntm closure-adapter on :" + port + "\n")
	http.ListenAndServe("127.0.0.1:"+port, nil)
}
