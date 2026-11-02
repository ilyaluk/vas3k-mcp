package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestValidToken(t *testing.T) {
	for _, tt := range []struct {
		token string
		want  bool
	}{
		{"st_a", true}, {"st_ABC-xyz_012", true},
		{"st_" + strings.Repeat("a", 509), true},
		{"", false}, {"st_", false}, {"st_" + strings.Repeat("a", 510), false},
		{"at_test", false}, {"st_hello\n", false}, {"st_é", false},
		{" st_abc", false}, {"st_a b", false}, {"st_a/b", false},
	} {
		if got := validToken(tt.token); got != tt.want {
			t.Errorf("validToken(%q) = %v, want %v", tt.token, got, tt.want)
		}
	}
}

func TestStoreLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Token() != "" {
		t.Fatal("new store has a token")
	}
	if _, err := os.Stat(filepath.Join(dir, tokenFilename)); !os.IsNotExist(err) {
		t.Fatalf("empty store created a credential: %v", err)
	}
	for _, token := range []string{"st_first", "st_replacement"} {
		if err := s.Replace(token); err != nil {
			t.Fatal(err)
		}
		if got := s.Token(); got != token {
			t.Fatalf("Token() = %q, want %q", got, token)
		}
		reopened, err := openStore(dir)
		if err != nil || reopened.Token() != token {
			t.Fatalf("reopen failed: %v", err)
		}
		for path, mode := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, tokenFilename): 0600} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("permissions %s: %v, info=%v", path, err, info)
			}
		}
		files, err := os.ReadDir(dir)
		if err != nil || len(files) != 1 || files[0].Name() != tokenFilename {
			t.Fatalf("temporary files not cleaned up: %v, %v", files, err)
		}
	}
	if err := s.Replace("invalid"); err == nil || s.Token() != "st_replacement" {
		t.Fatal("invalid replacement changed state")
	}
	for range 2 {
		if err := s.Forget(); err != nil {
			t.Fatal(err)
		}
	}
	if s.Token() != "" {
		t.Fatal("forget retained token")
	}
	reopened, err := openStore(dir)
	if err != nil || reopened.Token() != "" {
		t.Fatalf("forgotten token reappeared: %v", err)
	}
}

func TestStoreRejectsMalformedState(t *testing.T) {
	for i, data := range []string{
		"", "st_plaintext", "{}", "null", `{"token":"wrong"}`,
		`{"token":"st_good","extra":true}`, `{"token":"st_good"} {}`,
		`{"token":1}`, `{"token":"st_a`, strings.Repeat("x", 2049),
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tokenFilename), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := openStore(dir); err == nil {
				t.Fatal("accepted malformed state")
			} else if strings.Contains(err.Error(), "st_") {
				t.Fatal("error exposed stored credential")
			}
		})
	}
}

func TestStoreRejectsSymlinksAndSpecialFiles(t *testing.T) {
	base := t.TempDir()
	actual := filepath.Join(base, "actual")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(link); err == nil {
		t.Fatal("accepted symlink directory")
	}
	outside := filepath.Join(base, "outside")
	original := []byte(`{"token":"st_original"}`)
	if err := os.WriteFile(outside, original, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(actual, tokenFilename)
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(actual); err == nil {
		t.Fatal("accepted symlink token")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(actual)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace("st_new"); err == nil {
		t.Fatal("replaced a symlink")
	}
	if err := s.Forget(); err == nil {
		t.Fatal("forgot a symlink")
	}
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != string(original) {
		t.Fatal("symlink target changed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(actual); err == nil {
		t.Fatal("accepted directory token")
	}
}

func TestStoreFixesPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, tokenFilename)
	if err := os.WriteFile(path, []byte(`{"token":"st_test"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(dir); err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{dir: 0700, path: 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("permissions not fixed: %s: %v", path, err)
		}
	}
}

func TestStoreConcurrent(t *testing.T) {
	dir := t.TempDir()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 8 {
				token := fmt.Sprintf("st_%d_%d", i, j)
				if err := s.Replace(token); err != nil {
					t.Error(err)
				}
				got := s.Token()
				if got != "" && !validToken(got) {
					t.Error("partial token observed")
				}
				if j%3 == 0 {
					if err := s.Forget(); err != nil {
						t.Error(err)
					}
				}
			}
		}(i)
	}
	wg.Wait()
	reopened, err := openStore(dir)
	if err != nil || reopened.Token() != s.Token() {
		t.Fatalf("disk and memory disagree: %v", err)
	}
}

func TestResolveStateDir(t *testing.T) {
	base := t.TempDir()
	actual := filepath.Join(base, "private", "state")
	if err := os.MkdirAll(actual, 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "state")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	dir, err := resolveStateDir(alias)
	if err != nil || dir != actual {
		t.Fatalf("resolve systemd-style alias: %q, %v", dir, err)
	}
	lock, err := lockStateDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Replace("st_first"); err != nil {
		t.Fatal(err)
	}
	// A runtime alias change must not move the store away from the locked
	// directory or redirect replacement/forget operations.
	other := filepath.Join(base, "other")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, alias); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace("st_replaced"); err != nil {
		t.Fatal(err)
	}
	reopened, err := openStore(dir)
	if err != nil || reopened.Token() != "st_replaced" {
		t.Fatalf("canonical state lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, tokenFilename)); !os.IsNotExist(err) {
		t.Fatal("replacement followed changed alias")
	}
	if err := s.Forget(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(actual, tokenFilename)); !os.IsNotExist(err) {
		t.Fatal("forget did not use canonical directory")
	}
	info, err := os.Stat(actual)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("canonical directory not private: %v", err)
	}
	// A fresh startup follows the current operator-configured alias.
	if dir, err := resolveStateDir(alias); err != nil || dir != other {
		t.Fatalf("new startup: %q, %v", dir, err)
	}
}

func TestResolveStateDirInvalid(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(base, "dangling")
	if err := os.Symlink(filepath.Join(base, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, dangling} {
		if _, err := resolveStateDir(path); err == nil {
			t.Fatalf("accepted invalid state path: %s", path)
		}
	}
	created := filepath.Join(base, "new", "state")
	if dir, err := resolveStateDir(created); err != nil || dir != created {
		t.Fatalf("create regular state directory: %q, %v", dir, err)
	}
}
