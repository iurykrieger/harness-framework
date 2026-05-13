package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// Scan reads a Claude Code transcript JSONL file and returns one Entry
// per parseable line. Lines that fail to decode are skipped silently —
// transcripts contain heterogeneous record types and we only care about
// user/assistant messages.
func Scan(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("transcript open %s: %w", path, err)
	}
	defer f.Close()

	var out []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("transcript scan %s: %w", path, err)
	}
	return out, nil
}
