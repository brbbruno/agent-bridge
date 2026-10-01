package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type persistedState struct {
	Away          bool                    `json:"away"`
	Queues        map[string][]string     `json:"queued_late_replies"`
	SessionTitles map[string]sessionTitle `json:"session_titles,omitempty"`
}

type sessionTitle struct {
	Title  string    `json:"title"`
	SeenAt time.Time `json:"seen_at"`
}

func loadState(home string) (persistedState, error) {
	state := persistedState{Queues: map[string][]string{}, SessionTitles: map[string]sessionTitle{}}
	data, err := os.ReadFile(filepath.Join(home, "state.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Queues == nil {
		state.Queues = map[string][]string{}
	}
	if state.SessionTitles == nil {
		state.SessionTitles = map[string]sessionTitle{}
	}
	return state, nil
}

func saveState(home string, state persistedState) error {
	if state.Queues == nil {
		state.Queues = map[string][]string{}
	}
	if state.SessionTitles == nil {
		state.SessionTitles = map[string]sessionTitle{}
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(home, ".state-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(home, "state.json"))
}
