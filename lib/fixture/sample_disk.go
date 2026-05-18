package fixture

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Hint carries the inputs FindOnDisk and DeriveFromContract need to
// locate or synthesize a fixture sample. The library does not decide
// which directories to walk — searchPaths come from the calling skill.
type Hint struct {
	JourneyID   string
	UsecaseID   string
	Role        string
	ProjectRoot string
}

// Sample is the result of either disk discovery or contract derivation.
type Sample struct {
	Payload    []byte
	Ext        string
	Source     string
	SourcePath string
	BlindSpots []string
}

// FindOnDisk walks searchPaths in order and returns the best-matching
// fixture file for hint.Role. Tiebreaker:
//  1. exact role basename (e.g. "trigger.json" for Role=="trigger")
//     beats alias basename (e.g. "request.json").
//  2. earlier searchPath beats later.
//  3. lexicographic absolute path as final fallback.
//
// Returns (nil, nil) when no candidate is found in any searchPath.
// Returns (nil, err) only for I/O errors that prevent walking.
//
// Non-existent searchPaths are skipped silently — the library does not
// validate the inputs the caller passed. Subdirectories of searchPaths
// are NOT walked recursively; only direct children are considered.
func FindOnDisk(h Hint, searchPaths []string) (*Sample, error) {
	type candidate struct {
		path  string
		idx   int
		exact bool
	}
	var all []candidate
	exactSet, aliasSet := basenamePatternsFor(h.Role)
	for idx, dir := range searchPaths {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if isNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			base := e.Name()
			ext := filepath.Ext(base)
			stem := strings.TrimSuffix(base, ext)
			full := filepath.Join(dir, base)
			switch {
			case exactSet[stem]:
				all = append(all, candidate{path: full, idx: idx, exact: true})
			case aliasSet[stem]:
				all = append(all, candidate{path: full, idx: idx, exact: false})
			}
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].exact != all[j].exact {
			return all[i].exact
		}
		if all[i].idx != all[j].idx {
			return all[i].idx < all[j].idx
		}
		return all[i].path < all[j].path
	})
	winner := all[0]
	payload, err := os.ReadFile(winner.path)
	if err != nil {
		return nil, err
	}
	ext := strings.TrimPrefix(filepath.Ext(winner.path), ".")
	return &Sample{
		Payload:    payload,
		Ext:        ext,
		Source:     "disk",
		SourcePath: winner.path,
	}, nil
}

func basenamePatternsFor(role string) (exact, alias map[string]bool) {
	exact = map[string]bool{}
	alias = map[string]bool{}
	switch role {
	case "trigger":
		exact["trigger"] = true
		alias["request"] = true
		alias["input"] = true
		alias["args"] = true
	case "outcome":
		exact["outcome"] = true
		alias["response"] = true
		alias["expected"] = true
		alias["result"] = true
	case "body":
		exact["body"] = true
		alias["payload"] = true
	case "log-line":
		exact["log-line"] = true
		alias["log"] = true
		alias["sample.log"] = true
	case "event":
		exact["event"] = true
		alias["message"] = true
		alias["kafka"] = true
		alias["sqs"] = true
	default:
		exact[role] = true
	}
	return
}

func isNotExist(err error) bool {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return os.IsNotExist(pe.Err)
	}
	return os.IsNotExist(err)
}
