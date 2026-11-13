package loop

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

type State struct {
	Version      int       `json:"version"`
	ID           string    `json:"id"`
	Repository   string    `json:"repository"`
	Worktree     string    `json:"worktree"`
	Branch       string    `json:"branch"`
	Base         string    `json:"base"`
	Task         string    `json:"task"`
	Config       Config    `json:"config"`
	Stage        string    `json:"stage"`
	Round        int       `json:"round"`
	Feedback     string    `json:"feedback,omitempty"`
	AcceptedTree string    `json:"accepted_tree,omitempty"`
	Error        string    `json:"error,omitempty"`
	ActivePGID   int       `json:"active_pgid,omitempty"`
	Started      time.Time `json:"started"`
	Deadline     time.Time `json:"deadline"`
	Updated      time.Time `json:"updated"`
}

func ensureEOF(d *json.Decoder) error {
	var x any
	if e := d.Decode(&x); e != io.EOF {
		if e == nil {
			return errors.New("unexpected trailing JSON")
		}
		return e
	}
	return nil
}
func Save(dir string, s *State) error {
	s.Updated = time.Now().UTC()
	return WriteJSON(filepath.Join(dir, "state.json"), s)
}
func WriteJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return atomicWrite(path, append(b, '\n'))
}
func atomicWrite(path string, b []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".write-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func Load(dir string) (State, error) {
	var s State
	b, e := os.ReadFile(filepath.Join(dir, "state.json"))
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(b, &s); e != nil {
		return s, e
	}
	if s.Version != 1 {
		return s, errors.New("unsupported state version")
	}
	return s, s.Config.Validate()
}
