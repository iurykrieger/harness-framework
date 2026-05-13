package transcript

import "regexp"

var (
	reSlashName = regexp.MustCompile(`<command-name>(/[^<]+)</command-name>`)
	reSlashArgs = regexp.MustCompile(`<command-args>([^<]*)</command-args>`)
)

// ParseSlashCommand extracts the slash command name and arguments from
// the Claude Code wrapping convention:
//
//	<command-message>run-sensor</command-message>
//	<command-name>/run-sensor</command-name>
//	<command-args>my-sensor</command-args>
//
// Returns (name, args, true) on a successful match, ("", "", false)
// otherwise. The leading slash is preserved in name. args may be
// the empty string (a slash command invoked without arguments).
func ParseSlashCommand(s string) (name string, args string, ok bool) {
	m := reSlashName.FindStringSubmatch(s)
	if m == nil {
		return "", "", false
	}
	name = m[1]
	if a := reSlashArgs.FindStringSubmatch(s); a != nil {
		args = a[1]
	}
	return name, args, true
}
