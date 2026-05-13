package transcript

import "testing"

func TestParseSlashCommand(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantArgs string
		wantOK   bool
	}{
		{
			in:       "<command-message>run-sensor</command-message>\n<command-name>/run-sensor</command-name>\n<command-args>watch-logs</command-args>",
			wantName: "/run-sensor",
			wantArgs: "watch-logs",
			wantOK:   true,
		},
		{
			in:       "<command-name>/heal-sensor</command-name>\n<command-args>--signal-from=transcript --sensor=watch-logs</command-args>",
			wantName: "/heal-sensor",
			wantArgs: "--signal-from=transcript --sensor=watch-logs",
			wantOK:   true,
		},
		{
			in:       "<command-name>/list-sensors</command-name>\n<command-args></command-args>",
			wantName: "/list-sensors",
			wantArgs: "",
			wantOK:   true,
		},
		{"plain text, no command", "", "", false},
		{"<command-name>foo</command-name>", "", "", false}, // missing leading slash
		{"", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.wantName+"::"+tc.wantArgs, func(t *testing.T) {
			name, args, ok := ParseSlashCommand(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v", ok, tc.wantOK)
			}
			if name != tc.wantName {
				t.Fatalf("name=%q want %q", name, tc.wantName)
			}
			if args != tc.wantArgs {
				t.Fatalf("args=%q want %q", args, tc.wantArgs)
			}
		})
	}
}
