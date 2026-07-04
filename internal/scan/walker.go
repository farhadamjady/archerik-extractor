// Package scan provides a filesystem-backed FileTree: a repo-relative view of
// files driven by provider glob patterns, honoring exclusions.
package scan

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/farhadamjady/super-discovery/internal/provider"
)

// osFileTree implements provider.FileTree over the local filesystem.
type osFileTree struct {
	root    string
	exclude []*regexp.Regexp
}

// NewOSFileTree returns a FileTree rooted at root. Paths matching any exclude
// glob are hidden from Glob (Read/Exists still work if asked directly).
func NewOSFileTree(root string, exclude []string) provider.FileTree {
	t := &osFileTree{root: root}
	for _, e := range exclude {
		t.exclude = append(t.exclude, globToRegexp(e))
	}
	return t
}

func (t *osFileTree) Exists(rel string) bool {
	_, err := os.Stat(filepath.Join(t.root, filepath.FromSlash(rel)))
	return err == nil
}

func (t *osFileTree) Read(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(t.root, filepath.FromSlash(rel)))
}

func (t *osFileTree) Glob(pattern string) []string {
	re := globToRegexp(pattern)
	var out []string
	_ = filepath.WalkDir(t.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(t.root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if t.excluded(rel) {
			return nil
		}
		if re.MatchString(rel) {
			out = append(out, rel)
		}
		return nil
	})
	return out
}

func (t *osFileTree) excluded(rel string) bool {
	for _, re := range t.exclude {
		if re.MatchString(rel) {
			return true
		}
	}
	return false
}

// globToRegexp translates a glob into an anchored regexp.
//
//	**/  -> zero or more path segments
//	**   -> anything, including separators
//	*    -> anything within a single segment
//	?    -> a single non-separator char
func globToRegexp(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch c {
		case '*':
			switch {
			case i+2 < len(glob) && glob[i+1] == '*' && glob[i+2] == '/':
				b.WriteString("(?:.*/)?")
				i += 2
			case i+1 < len(glob) && glob[i+1] == '*':
				b.WriteString(".*")
				i++
			default:
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}
