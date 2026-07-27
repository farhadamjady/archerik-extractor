package deployrepo

import "fmt"

// RenderError is a non-fatal failure rendering or parsing one unit (a Helm
// chart + values overlay, a Kustomize overlay, or a single raw manifest
// file). A failure on one unit must never abort the rest of the scan — same
// "broken input never fails the scan" philosophy as adapterIndexer's handling
// of a malformed .ekg-adapters.json.
type RenderError struct {
	Unit string // chart dir / kustomization dir / raw file path
	Env  string // overlay/values file this render attempt used, "" if n/a
	Err  error
}

func (e RenderError) Error() string {
	if e.Env == "" {
		return fmt.Sprintf("%s: %v", e.Unit, e.Err)
	}
	return fmt.Sprintf("%s (%s): %v", e.Unit, e.Env, e.Err)
}
