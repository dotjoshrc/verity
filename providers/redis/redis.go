// Package redis provisions an isolated slice of a Redis server per verity suite:
// an ACL user that can only touch keys and channels under the suite's own prefix.
//
// Isolation is moderate, not strong. Values, writes and pub/sub are isolated, and
// the commands that reach across the server (FLUSHALL, CONFIG, KEYS, SELECT and
// so on) are denied. Key names are not hidden: SCAN still lists other suites'
// keys, because denying it would break services that scan their own. The server
// must be Redis 7 or later: 6.0 has no channel ACLs, and 6.2 refuses the negative
// subcommand rules (such as -script|flush) that the deny list uses.
package redis

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	goredis "github.com/redis/go-redis/v9"

	"github.com/dotjoshrc/verity/internal/core"
)

func init() {
	core.RegisterProvider("redis", &provider{})
}

type urlEnv string

type prefixEnv string

// EnvName sets the env var that carries the connection URL. The default is
// REDIS_URL.
func EnvName(name string) any { return urlEnv(name) }

// PrefixEnvName sets the env var that carries the key prefix. The default is
// REDIS_KEY_PREFIX. The service must put the prefix on every key and channel it
// uses: the suite's user is refused anything else.
func PrefixEnvName(name string) any { return prefixEnv(name) }

// Keyspace is one suite's isolated slice of a Redis server.
type Keyspace struct {
	user, url, prefix string

	mu     sync.Mutex
	client *goredis.Client
	closed bool
}

// URL returns a connection URL for the suite's user.
func (k *Keyspace) URL() string { return k.url }

// Prefix returns the prefix of every key and channel the suite may use.
func (k *Keyspace) Prefix() string { return k.prefix }

// Client returns a client for the suite's user, opened on first use. Do not use
// it after the resource is released.
func (k *Keyspace) Client() *goredis.Client {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.client == nil {
		opts, err := goredis.ParseURL(k.url)
		if err != nil { // the URL is built in this package
			panic(err)
		}
		k.client = goredis.NewClient(opts)
	}

	return k.client
}

// close shuts the client and is safe to repeat.
func (k *Keyspace) close() error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.client == nil || k.closed {
		return nil
	}
	k.closed = true

	return k.client.Close()
}

type provider struct {
	mu     sync.Mutex
	admins map[string]*goredis.Client
}

func (p *provider) Provision(ctx context.Context, req core.Request) (*core.Resource, error) {
	urlKey, prefixKey := "REDIS_URL", "REDIS_KEY_PREFIX"
	for _, o := range req.Options {
		switch o := o.(type) {
		case urlEnv:
			urlKey = string(o)
		case prefixEnv:
			prefixKey = string(o)
		default:
			return nil, fmt.Errorf("redis: unknown option type %T", o)
		}
	}

	user, prefix, err := names(req.RunID, req.SuiteID)
	if err != nil {
		return nil, err
	}

	admin := p.admin(req.Endpoint)
	password := rand.Text()
	if err := admin.Do(ctx, aclSetUser(user, password, prefix)...).Err(); err != nil {
		return nil, fmt.Errorf("redis: create user %s: %w", user, explain(err))
	}

	k := &Keyspace{user: user, prefix: prefix, url: connURL(user, password, req.Endpoint)}

	return &core.Resource{
		Env:    map[string]string{urlKey: k.url, prefixKey: prefix},
		Handle: k,
		Release: func(ctx context.Context) error {
			return errors.Join(k.close(), release(ctx, admin, k))
		},
	}, nil
}

// admin returns the cached admin client for an endpoint. It is never closed: it
// lives as long as the process, like the provider.
func (p *provider) admin(ep core.Endpoint) *goredis.Client {
	key := connURL(ep.User, ep.Password, ep)

	p.mu.Lock()
	defer p.mu.Unlock()

	if c, ok := p.admins[key]; ok {
		return c
	}

	c := goredis.NewClient(&goredis.Options{
		Addr:     net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port)),
		Username: cmp.Or(ep.User, "default"),
		Password: ep.Password,
		// Each step is one command, so a small cap cannot deadlock; it keeps many
		// concurrent suites from using up the server's connections.
		PoolSize: 4,
	})
	if p.admins == nil {
		p.admins = map[string]*goredis.Client{}
	}
	p.admins[key] = c

	return c
}

// release deletes the suite's user, which also closes its connections, and then
// its keys. The user goes first so nothing writes while the keys are removed.
// shortcut: the scan visits the whole keyspace, fine for test-sized data; switch to
// per-suite tracking if a stack holds millions of keys.
func release(ctx context.Context, admin *goredis.Client, k *Keyspace) error {
	if err := admin.Do(ctx, "ACL", "DELUSER", k.user).Err(); err != nil {
		return fmt.Errorf("redis: delete user %s: %w", k.user, err)
	}

	iter := admin.Scan(ctx, 0, k.prefix+"*", 500).Iterator()
	var batch []string
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := admin.Unlink(ctx, batch...).Err()
		batch = batch[:0]
		return err
	}
	for iter.Next(ctx) {
		if batch = append(batch, iter.Val()); len(batch) == 500 {
			if err := flush(); err != nil {
				return fmt.Errorf("redis: delete keys under %s: %w", k.prefix, err)
			}
		}
	}
	if err := errors.Join(iter.Err(), flush()); err != nil {
		return fmt.Errorf("redis: delete keys under %s: %w", k.prefix, err)
	}

	return nil
}

var unsafeChars = regexp.MustCompile(`[^a-z0-9_]`)

// names returns the ACL user name and key prefix for one suite. Both carry a hash
// of the run and suite IDs, so IDs that differ only in characters the names
// cannot hold still get distinct ones. Neither holds a glob character, so the
// prefix is safe inside an ACL pattern.
func names(runID, suiteID string) (user, prefix string, err error) {
	if runID == "" || suiteID == "" {
		return "", "", errors.New("redis: request needs a run ID and a suite ID")
	}

	sum := sha256.Sum256([]byte(runID + "\x00" + suiteID))
	run := unsafeChars.ReplaceAllString(strings.ToLower(runID), "_")
	run = run[:min(len(run), 20)]
	id := run + "_" + hex.EncodeToString(sum[:16])

	return "verity_" + id, "verity:" + id + ":", nil
}

// aclSetUser returns the ACL SETUSER command for a suite's user. The deny list is
// what the milestone 2 spike found reaches across suites or the server beyond
// what -@dangerous already blocks: SELECT, RANDOMKEY (leaks a foreign key name),
// DBSIZE, SCRIPT FLUSH and KILL, and FUNCTION and PUBSUB (global namespaces).
func aclSetUser(user, password, prefix string) []any {
	return []any{
		"ACL", "SETUSER", user, "reset", "on", ">" + password, "resetchannels", "&" + prefix + "*", "~" + prefix + "*", "+@all", "-@dangerous",
		"-select", "-randomkey", "-dbsize", "-script|flush", "-script|kill", "-function", "-pubsub",
	}
}

// connURL builds a redis:// URL. An empty password is left out, because AUTH
// with one is refused by some servers.
func connURL(user, password string, ep core.Endpoint) string {
	u := url.URL{Scheme: "redis", Host: net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port))}
	if password != "" {
		u.User = url.UserPassword(cmp.Or(user, "default"), password)
	}

	return u.String()
}

// explain adds a hint to the errors users hit most: no or wrong admin credentials,
// and a server too old for the ACL rules (Redis 5 has no ACL command; 6.x rejects a
// rule with "Error in ACL SETUSER modifier").
func explain(err error) error {
	msg := err.Error()
	switch {
	// A client with no password that sends a long command, as ACL SETUSER is, gets
	// "unauthenticated multibulk length" instead of NOAUTH.
	case strings.Contains(msg, "NOAUTH"), strings.Contains(msg, "WRONGPASS"), strings.Contains(msg, "invalid username-password"), strings.Contains(msg, "unauthenticated"):
		return fmt.Errorf("%w (set the verity.user and verity.password labels on the redis service to its admin credentials)", err)
	case strings.Contains(msg, "unknown command"), strings.Contains(msg, "Error in ACL SETUSER modifier"):
		return fmt.Errorf("%w (verity needs Redis 7 or later, for ACL subcommand rules)", err)
	}

	return err
}
