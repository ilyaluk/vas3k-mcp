package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const tokenFilename = "token.json"

type store struct {
	mu    sync.RWMutex
	dir   string
	token string
}

func validToken(token string) bool {
	if len(token) <= 3 || len(token) > 512 || token[:3] != "st_" {
		return false
	}
	for _, c := range token[3:] {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func privateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("state directory must be a real directory")
	}
	return os.Chmod(dir, 0700)
}

// regularTokenFile rejects symlinks, directories, devices, and other special files.
func regularTokenFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("token file must be a regular file")
	}
	return true, nil
}

func openStore(dir string) (*store, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	s := &store{dir: dir}
	path := filepath.Join(dir, tokenFilename)
	exists, err := regularTokenFile(path)
	if err != nil || !exists {
		return s, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 2049))
	if err != nil {
		return nil, err
	}
	if len(data) > 2048 {
		return nil, errors.New("invalid token state")
	}
	var state struct {
		Token string `json:"token"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return nil, errors.New("invalid token state")
	}
	if decoder.Decode(new(any)) != io.EOF || !validToken(state.Token) {
		return nil, errors.New("invalid token state")
	}
	s.token = state.Token
	return s, nil
}

func (s *store) Token() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

func (s *store) syncDir() error {
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *store) Replace(token string) error {
	if !validToken(token) {
		return errors.New("invalid service token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := privateDir(s.dir); err != nil {
		return err
	}
	path := filepath.Join(s.dir, tokenFilename)
	if _, err := regularTokenFile(path); err != nil {
		return err
	}
	file, err := os.CreateTemp(s.dir, ".token-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	data, _ := json.Marshal(struct {
		Token string `json:"token"`
	}{token})
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if _, err := regularTokenFile(path); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	// Once renamed, memory must reflect the on-disk value even if directory
	// syncing reports an error. No partially written credential is ever visible.
	s.token = token
	return s.syncDir()
}

func (s *store) Forget() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := privateDir(s.dir); err != nil {
		return err
	}
	path := filepath.Join(s.dir, tokenFilename)
	exists, err := regularTokenFile(path)
	if err != nil {
		return err
	}
	if exists {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	s.token = ""
	return s.syncDir()
}
