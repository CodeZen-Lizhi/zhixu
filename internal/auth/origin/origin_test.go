package origin

import "testing"

func TestCanonical(t *testing.T) {
	tests := map[string]struct {
		value string
		want  string
		ok    bool
	}{
		"canonical https":       {value: "https://example.com", want: "https://example.com", ok: true},
		"canonical ipv6":        {value: "http://[::1]:8080", want: "http://[::1]:8080", ok: true},
		"canonical mapped ipv6": {value: "http://[::ffff:c000:201]:8080", want: "http://[::ffff:c000:201]:8080", ok: true},
		"canonical idn":         {value: "https://xn--bcher-kva.example", want: "https://xn--bcher-kva.example", ok: true},
		"default https port":    {value: "https://EXAMPLE.com:443", want: "https://example.com", ok: true},
		"default http port":     {value: "http://example.com:080", want: "http://example.com", ok: true},
		"non-default port":      {value: "https://EXAMPLE.com:8443", want: "https://example.com:8443", ok: true},
		"noncanonical ipv4":     {value: "http://127.000.000.001"},
		"numeric ipv4":          {value: "http://2130706433"},
		"numeric suffix dns":    {value: "https://api.example.1"},
		"expanded ipv6":         {value: "http://[0:0:0:0:0:0:0:1]:8080"},
		"ipv4 embedded ipv6":    {value: "http://[::ffff:192.0.2.1]:8080"},
		"unicode idn":           {value: "https://b\u00fccher.example"},
		"path":                  {value: "https://example.com/path"},
		"query":                 {value: "https://example.com?next=1"},
		"credentials":           {value: "https://user@example.com"},
		"unsupported scheme":    {value: "ftp://example.com"},
		"invalid port":          {value: "https://example.com:0"},
		"non-numeric port":      {value: "https://example.com:abc"},
		"leading whitespace":    {value: " https://example.com"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok := Canonical(test.value)
			if ok != test.ok || got != test.want {
				t.Fatalf("Canonical(%q) = (%q, %t), want (%q, %t)", test.value, got, ok, test.want, test.ok)
			}
			if test.ok && IsCanonical(test.value) != (test.value == test.want) {
				t.Fatalf("IsCanonical(%q) is inconsistent with canonical value %q", test.value, test.want)
			}
		})
	}
}
