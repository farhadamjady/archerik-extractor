package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Store keeps one baseline file per service (the raw byte-stable extractor
// JSON), under a data directory. Files are inspectable with any JSON tool —
// deliberately boring MVP storage.
type Store struct{ dir string }

// NewStore creates the data directory if needed.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Load returns the stored baseline bytes for a service, found=false when the
// service has never been scanned on the default branch.
func (s *Store) Load(serviceID string) (data []byte, found bool, err error) {
	b, err := os.ReadFile(s.path(serviceID))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// Save writes the new baseline atomically (write temp + rename), so a crashed
// write can never leave a half-baseline behind.
func (s *Store) Save(serviceID string, data []byte) error {
	p := s.path(serviceID)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// List returns every stored baseline's (service_id, service_name) pair — the
// fleet registry the name→service mapping is built from.
func (s *Store) List() ([][2]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var svc struct {
			ID   string `json:"service_id"`
			Name string `json:"service_name"`
		}
		if json.Unmarshal(b, &svc) == nil && svc.ID != "" {
			out = append(out, [2]string{svc.ID, svc.Name})
		}
	}
	return out, nil
}

// path maps a service id to a file, sanitized so an id can never escape the
// data directory (path traversal).
func (s *Store) path(serviceID string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, serviceID)
	safe = strings.Trim(safe, ".") // no ".." or hidden files
	if safe == "" {
		safe = "_"
	}
	return filepath.Join(s.dir, safe+".json")
}
