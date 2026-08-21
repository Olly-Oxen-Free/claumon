package limits

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// State is what the watcher remembers between polls.
//
// It is persisted because the interesting events are all comparisons against
// the previous reading. Without it, every restart would be a first poll —
// silent by design — and a reset that happened while claumon was down would
// never be reported at all.
type State struct {
	// Limits is the last reading, the baseline for the next comparison.
	Limits []Snapshot `json:"limits"`
	// FiredResets maps a limit to the resets_at it was already announced for,
	// so the punctual timer and the poll that follows do not both speak.
	FiredResets map[string]time.Time `json:"fired_resets"`
	// NotifiedThresholds maps a limit to the highest band already reported in
	// the current window. Cleared when the window rolls over.
	NotifiedThresholds map[string]int `json:"notified_thresholds"`
	LastPoll           time.Time      `json:"last_poll,omitzero"`
}

func NewState() *State {
	return &State{
		FiredResets:        map[string]time.Time{},
		NotifiedThresholds: map[string]int{},
	}
}

// LoadState reads persisted state. A missing or corrupt file yields empty
// state rather than an error: the next poll rebuilds the baseline, and one
// silent cycle is a far better failure than refusing to start.
func LoadState(path string) *State {
	s := NewState()
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var loaded State
	if err := json.Unmarshal(data, &loaded); err != nil {
		return s
	}
	if loaded.FiredResets == nil {
		loaded.FiredResets = map[string]time.Time{}
	}
	if loaded.NotifiedThresholds == nil {
		loaded.NotifiedThresholds = map[string]int{}
	}
	return &loaded
}

// Save writes the state atomically, so a crash mid-write cannot leave a
// truncated file that reads as "no history" on the next start.
func (s *State) Save(path string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
