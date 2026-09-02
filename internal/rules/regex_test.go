package rules

import (
	"slices"
	"testing"
)

func TestParseRegexMode(t *testing.T) {
	for _, value := range []string{"strict", " STRICT ", "balanced", "BALANCED"} {
		if _, err := ParseRegexMode(value); err != nil {
			t.Fatalf("ParseRegexMode(%q): %v", value, err)
		}
	}
	if _, err := ParseRegexMode("unsafe"); err == nil {
		t.Fatal("ParseRegexMode(unsafe) succeeded")
	}
}

func TestStrictRegexDowngrade(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    []string
	}{
		{name: "exact domain", pattern: `^api\.example\.com$`, want: []string{"DOMAIN,api.example.com"}},
		{name: "keyword", pattern: `google`, want: []string{"DOMAIN-KEYWORD,google"}},
		{name: "wildcard", pattern: `^cdn[0-9]\.example\.com$`, want: []string{"DOMAIN-WILDCARD,cdn[0-9].example.com"}},
		{
			name:    "suffix alternatives",
			pattern: `(^|\.)service\.(com|net)$`,
			want: []string{
				"DOMAIN,service.com",
				"DOMAIN,service.net",
				"DOMAIN-WILDCARD,*.service.com",
				"DOMAIN-WILDCARD,*.service.net",
			},
		},
		{
			name:    "bounded repeat",
			pattern: `^node[0-9]{1,2}\.example$`,
			want: []string{
				"DOMAIN-WILDCARD,node[0-9].example",
				"DOMAIN-WILDCARD,node[0-9][0-9].example",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, conversion := downgradeRegex(test.pattern, RegexStrict)
			if conversion != regexExact {
				t.Fatalf("conversion = %v, want exact", conversion)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("lines = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestBalancedRegexDowngrade(t *testing.T) {
	pattern := `^api[0-9]+\.example\.com$`
	if lines, conversion := downgradeRegex(pattern, RegexStrict); conversion != regexSkipped || len(lines) != 0 {
		t.Fatalf("strict conversion = %v, %#v; want skipped", conversion, lines)
	}

	lines, conversion := downgradeRegex(pattern, RegexBalanced)
	if conversion != regexDegraded {
		t.Fatalf("balanced conversion = %v, want degraded", conversion)
	}
	want := []string{"DOMAIN-WILDCARD,api[0-9]*.example.com"}
	if !slices.Equal(lines, want) {
		t.Fatalf("balanced lines = %#v, want %#v", lines, want)
	}
}

func TestRegexDowngradeRejectsEmptyMatch(t *testing.T) {
	for _, mode := range []RegexMode{RegexStrict, RegexBalanced} {
		if lines, conversion := downgradeRegex(`^$`, mode); conversion != regexSkipped || len(lines) != 0 {
			t.Fatalf("mode %s returned %v, %#v; want skipped", mode, conversion, lines)
		}
	}
}

func TestBalancedRegexDowngradeRejectsOverlyBroadFallback(t *testing.T) {
	lines, conversion := downgradeRegex(`^\p{Han}+$`, RegexBalanced)
	if conversion != regexSkipped || len(lines) != 0 {
		t.Fatalf("conversion = %v, lines = %#v; want skipped", conversion, lines)
	}
}
