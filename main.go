package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const version = "0.1.0"

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Lock before loading credentials, so a simultaneous shutdown/rotation cannot
// leave the new process serving a stale in-memory token.
func lockStateDir(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("cannot open state lock")
	}
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() {
		lock.Close()
		return nil, errors.New("state lock must be a regular file")
	}
	if err := lock.Chmod(0600); err != nil {
		lock.Close()
		return nil, errors.New("cannot protect state lock")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another instance is using this state directory")
	}
	return lock, nil
}

func appHandler(s *store, c *clubClient) http.Handler {
	web := newWebHandler(s, c.me, c.status)
	mcp := newMCPHandler(readTools(), func(ctx context.Context, name string, args json.RawMessage) (any, error) {
		token := s.Token()
		if token == "" {
			return nil, errors.New("not configured, open / to set up")
		}
		return c.call(ctx, token, name, args)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Proxy credentials are not club credentials. The preferred header is
		// stripped by exe.dev; discard either spelling if a proxy forwards it.
		r.Header.Del("Authorization")
		r.Header.Del("X-Exedev-Authorization")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case "/mcp":
			// MCP clients without Origin are permitted. Browser-originated requests
			// must be same-origin; this is CSRF protection, NOT caller authentication.
			if (r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "") && !sameOrigin(r) {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
			mcp.ServeHTTP(w, r)
		case "/":
			web.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func run() error {
	listen := flag.String("listen", envDefault("VAS3K_LISTEN", "0.0.0.0:8000"), "HTTP listen address (must be behind a private authenticating proxy)")
	stateDir := flag.String("state-dir", envDefault("VAS3K_STATE_DIR", "./state"), "private credential state directory")
	baseURL := flag.String("base-url", "https://vas3k.club", "fixed HTTPS upstream origin; change only for trusted testing")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	c, err := newClubClient(*baseURL)
	if err != nil {
		return err
	}
	// Linux advisory lock prevents two instances sharing one credential store.
	// Its persistent inode is never removed, so restarts cannot split the lock.
	lock, err := lockStateDir(*stateDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	s, err := openStore(*stateDir)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: *listen, Handler: appHandler(s, c), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("vas3k-mcp-go %s listening on %s (authentication delegated to proxy)", version, *listen)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
