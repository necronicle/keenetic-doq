package geo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultStatePath — выбор сервера обхода; переживает рестарт, пишется
	// редко: при закреплении и после оценки.
	DefaultStatePath = "/opt/var/lib/doqd/geo.state"
	// DefaultSnapshotPath — снимок для CLI; /tmp — это tmpfs, флешка не страдает.
	DefaultSnapshotPath = "/tmp/doqd.state.json"
)

type State struct {
	Pinned      string    `json:"pinned"`
	Since       time.Time `json:"since"`
	EvaluatedAt time.Time `json:"evaluated_at"`
	Ranking     []Result  `json:"ranking"`
}

type Snapshot struct {
	Time        time.Time     `json:"time"`
	Pinned      string        `json:"pinned"`
	Since       time.Time     `json:"since"`
	EvaluatedAt time.Time     `json:"evaluated_at"`
	Evaluating  bool          `json:"evaluating"`
	Ranking     []Result      `json:"ranking"`
	Proxies     []ProxyStatus `json:"proxies"`
	LearnedGeo  int           `json:"learned_geo"`
	Fails       int           `json:"fails"`
}

// writeJSON пишет атомарно: во временный файл с уникальным именем в том же
// каталоге, затем rename. Уникальное имя нужно, когда несколько горутин пишут
// один путь одновременно (фоновый цикл и переключение при отказе): общий
// временный файл у них перемешался бы. Оборванная запись не портит файл.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.new")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// При любой ошибке после создания временного файла он не должен остаться.
	fail := func(err error) error {
		os.Remove(tmp)
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	// CreateTemp создаёт файл с правами 0600; для состояния нужны 0644.
	if err := os.Chmod(tmp, 0o644); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fail(err)
	}
	return nil
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func SaveState(path string, s *State) error { return writeJSON(path, s) }

func LoadState(path string) (*State, error) {
	var s State
	if err := readJSON(path, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func WriteSnapshot(path string, s Snapshot) error { return writeJSON(path, s) }

func ReadSnapshot(path string) (*Snapshot, error) {
	var s Snapshot
	if err := readJSON(path, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
