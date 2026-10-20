// Package postgres provisions an isolated Postgres database per verity suite.
// Each is a clone of the endpoint's database, which the compose stack has already
// populated with its schema: verity runs no migrations itself.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"sync"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/dotjoshrc/verity/internal/core"
)

func init() {
	core.RegisterProvider("postgres", &provider{})
}

type envName string

// EnvName sets the env var that carries the DSN. The default is DATABASE_URL.
func EnvName(name string) any { return envName(name) }

// Database is one suite's isolated database.
type Database struct {
	name, dsn string

	mu sync.Mutex
	db *sql.DB
}

// DSN returns a connection string for the database.
func (s *Database) DSN() string { return s.dsn }

// Name returns the database's name.
func (s *Database) Name() string { return s.name }

// DB returns a connection pool for the database, opened on first use. Do not use it
// after the resource is released.
func (s *Database) DB() *sql.DB {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		db, err := sql.Open("pgx", s.dsn)
		if err != nil { // only possible if the driver is not registered
			panic(err)
		}
		s.db = db
	}

	return s.db
}

func (s *Database) release(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return nil
	}

	return s.db.Close()
}

type provider struct {
	mu     sync.Mutex
	admins map[string]*sql.DB
}

func (p *provider) Provision(ctx context.Context, req core.Request) (*core.Resource, error) {
	envKey := "DATABASE_URL"
	for _, o := range req.Options {
		switch o := o.(type) {
		case envName:
			envKey = string(o)
		default:
			return nil, fmt.Errorf("postgres: unknown option type %T", o)
		}
	}

	source := req.Endpoint.Database
	switch source {
	case "":
		return nil, errors.New("postgres: no database to clone; set the verity.database label or POSTGRES_DB")
	case adminDatabase:
		// verity's admin connection is on this database, and Postgres refuses to
		// clone a database with sessions. Healthchecks and tools also connect to it.
		return nil, fmt.Errorf("postgres: the schema database is %q, which verity uses as its admin database; put the schema in its own database (POSTGRES_DB or the verity.database label)", source)
	}

	name, err := databaseName(req.RunID, req.SuiteID)
	if err != nil {
		return nil, err
	}

	admin, err := p.admin(req.Endpoint)
	if err != nil {
		return nil, err
	}
	if err := clone(ctx, admin, source, name); err != nil {
		return nil, fmt.Errorf("postgres: clone %s as %s: %w", source, name, err)
	}

	database := &Database{name: name, dsn: dsn(req.Endpoint, name)}

	return &core.Resource{
		Env:     map[string]string{envKey: database.dsn},
		Handle:  database,
		Release: database.release,
	}, nil
}

// admin returns the cached admin pool for an endpoint. It is never closed: it
// lives as long as the process, like the provider.
func (p *provider) admin(ep core.Endpoint) (*sql.DB, error) {
	key := dsn(ep, adminDatabase)

	p.mu.Lock()
	defer p.mu.Unlock()

	if db, ok := p.admins[key]; ok {
		return db, nil
	}

	db, err := sql.Open("pgx", key)
	if err != nil {
		return nil, err
	}
	// Each clone is one statement, so a small cap cannot deadlock; it keeps many
	// concurrent suites from using up the server's connections.
	db.SetMaxOpenConns(4)

	if p.admins == nil {
		p.admins = map[string]*sql.DB{}
	}
	p.admins[key] = db

	return db, nil
}

// dsn builds a connection string for database name on the endpoint.
func dsn(ep core.Endpoint, name string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(ep.User, ep.Password),
		Host:     net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port)),
		Path:     "/" + name,
		RawQuery: "sslmode=disable",
	}

	return u.String()
}
