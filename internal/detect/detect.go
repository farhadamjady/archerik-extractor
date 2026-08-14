// Package detect runs the language/framework detection phase. Given a single
// service repo, it selects exactly one provider from marker files, and fails
// loudly when none match or the match is ambiguous — a hard failure is the right
// CI signal; a silent misdetection is not.
package detect

import (
	"sort"

	"github.com/farhadamjady/archerik-extractor/internal/exitcode"
	"github.com/farhadamjady/archerik-extractor/internal/provider"
)

// ModuleInfo records what detection concluded about the repo.
type ModuleInfo struct {
	Root     string   // repo root
	Provider string   // selected provider name
	Score    int      // winning match score
	Markers  []string // reserved: files/annotations that drove the decision
}

// Detect selects the single provider for a single-service repo.
//
// Errors when zero providers match, or when the top two candidates tie (ambiguous
// stack). Ties must be resolved by making provider Match scoring more specific,
// not by guessing here.
func Detect(root string, fs provider.FileTree, providers []provider.Provider) (provider.Provider, ModuleInfo, error) {
	type candidate struct {
		p     provider.Provider
		score int
	}
	var cands []candidate
	for _, p := range providers {
		if ok, score := p.Match(root, fs); ok && score > 0 {
			cands = append(cands, candidate{p, score})
		}
	}

	if len(cands) == 0 {
		return nil, ModuleInfo{}, exitcode.Errorf(exitcode.Detect,
			"detect: no framework provider matched repo at %q", root)
	}

	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })

	if len(cands) >= 2 && cands[0].score == cands[1].score {
		return nil, ModuleInfo{}, exitcode.Errorf(exitcode.Detect,
			"detect: ambiguous stack at %q — %q and %q both matched with score %d",
			root, cands[0].p.Name(), cands[1].p.Name(), cands[0].score)
	}

	win := cands[0]
	return win.p, ModuleInfo{Root: root, Provider: win.p.Name(), Score: win.score}, nil
}
