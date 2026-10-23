// Package todo is a tiny HTTP service backed by Postgres, to show how a service
// is run under verity. It reads DATABASE_URL through getenv and serves on the
// listener it is given, which is what verity.WithService provides.
package todo

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// Run serves until ctx is cancelled, then shuts down and returns.
func Run(ctx context.Context, getenv func(string) string, ln net.Listener) error {
	db, err := sql.Open("pgx", getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		}
	})
	mux.HandleFunc("POST /todos", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Title string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Title == "" {
			http.Error(w, "need a JSON body with a title", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "INSERT INTO todos (title) VALUES ($1)", in.Title); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET /todos", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(r.Context(), "SELECT title FROM todos ORDER BY id")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		titles := []string{}
		for rows.Next() {
			var title string
			if err := rows.Scan(&title); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			titles = append(titles, title)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(titles)
	})

	srv := &http.Server{Handler: mux}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err := <-served: // Serve only returns early when it failed
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		srv.Close()
		return err
	}
	<-served // nothing outlives the service once Run returns
	return nil
}
