package fixture

import "testing"

func TestFormatPayload_JSON(t *testing.T) {
	t.Run("compact becomes indented", func(t *testing.T) {
		got := string(formatPayload([]byte(`{"b":2,"a":1}`), ".json"))
		want := "{\n  \"b\": 2,\n  \"a\": 1\n}\n"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})

	t.Run("already indented stays stable", func(t *testing.T) {
		in := "{\n  \"a\": 1\n}\n"
		if got := string(formatPayload([]byte(in), ".json")); got != in {
			t.Fatalf("got %q want %q", got, in)
		}
	})

	t.Run("invalid JSON passes through", func(t *testing.T) {
		raw := []byte(`not json {{{`)
		if got := string(formatPayload(raw, ".json")); got != string(raw) {
			t.Fatalf("got %q want %q", got, raw)
		}
	})
}

func TestFormatPayload_YAML(t *testing.T) {
	t.Run("flow style becomes block style", func(t *testing.T) {
		in := []byte(`{a: 1, b: [1, 2, 3]}`)
		got := string(formatPayload(in, ".yaml"))
		// sigs.k8s.io/yaml canonicalizes to sorted keys + block style.
		want := "a: 1\nb:\n- 1\n- 2\n- 3\n"
		if got != want {
			t.Fatalf("got %q\nwant %q", got, want)
		}
	})

	t.Run(".yml extension handled identically", func(t *testing.T) {
		in := []byte(`{name: tail-sensor, kind: observation}`)
		ya := string(formatPayload(in, ".yaml"))
		yml := string(formatPayload(in, ".yml"))
		if ya != yml {
			t.Fatalf(".yaml and .yml diverged:\nyaml=%q\nyml =%q", ya, yml)
		}
	})

	t.Run("invalid YAML passes through", func(t *testing.T) {
		raw := []byte("name: foo\n\tbad: indentation")
		if got := string(formatPayload(raw, ".yaml")); got != string(raw) {
			t.Fatalf("got %q want %q", got, raw)
		}
	})

	t.Run("empty payload passes through", func(t *testing.T) {
		if got := string(formatPayload([]byte(""), ".yaml")); got != "" {
			t.Fatalf("got %q want empty", got)
		}
	})
}

func TestFormatPayload_XML(t *testing.T) {
	t.Run("collapsed XML becomes indented", func(t *testing.T) {
		in := []byte(`<root><item id="1">a</item><item id="2">b</item></root>`)
		got := string(formatPayload(in, ".xml"))
		want := "<root>\n  <item id=\"1\">a</item>\n  <item id=\"2\">b</item>\n</root>\n"
		if got != want {
			t.Fatalf("got %q\nwant %q", got, want)
		}
	})

	t.Run("already indented stays well-formed", func(t *testing.T) {
		in := []byte("<root>\n  <a>1</a>\n  <b>2</b>\n</root>")
		got := string(formatPayload(in, ".xml"))
		want := "<root>\n  <a>1</a>\n  <b>2</b>\n</root>\n"
		if got != want {
			t.Fatalf("got %q\nwant %q", got, want)
		}
	})

	t.Run("invalid XML passes through", func(t *testing.T) {
		raw := []byte("<root><unclosed>")
		if got := string(formatPayload(raw, ".xml")); got != string(raw) {
			t.Fatalf("got %q want %q", got, raw)
		}
	})
}

func TestFormatPayload_UnknownExtension(t *testing.T) {
	cases := map[string]string{
		".txt":   "plain text\nwith newlines",
		".jsonl": `{"a":1}` + "\n" + `{"a":2}`,
		".log":   "2026-05-19T12:00:00Z [INFO] hello",
		".csv":   "a,b,c\n1,2,3",
		"":       "no extension at all",
	}
	for ext, body := range cases {
		t.Run("ext="+ext, func(t *testing.T) {
			if got := string(formatPayload([]byte(body), ext)); got != body {
				t.Fatalf("got %q want %q", got, body)
			}
		})
	}
}

func TestFormatPayload_ExtensionCaseInsensitive(t *testing.T) {
	in := []byte(`{"a":1}`)
	want := "{\n  \"a\": 1\n}\n"
	for _, ext := range []string{".JSON", ".Json", ".jSoN"} {
		if got := string(formatPayload(in, ext)); got != want {
			t.Fatalf("ext=%q got %q want %q", ext, got, want)
		}
	}
}
