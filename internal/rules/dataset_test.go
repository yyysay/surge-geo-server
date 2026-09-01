package rules

import (
	"net/netip"
	"regexp"
	"strings"
	"testing"

	"github.com/yangtudou/surge-geo-server/internal/model"
)

func testDataset() *Dataset {
	site := model.GeoSite{Name: "example", Rules: []model.DomainRule{
		{Kind: model.DomainSuffix, Value: "example.com"},
		{Kind: model.DomainFull, Value: "exact.example.net", Attributes: []string{"cn"}},
		{Kind: model.DomainKeyword, Value: "keyword"},
		{Kind: model.DomainRegex, Value: `^api\d+\.example\.org$`},
	}}
	prefix := netip.MustParsePrefix("8.8.8.0/24")
	d := &Dataset{
		sites:    map[string]model.GeoSite{"example": site},
		geoips:   map[string]model.GeoIP{"google": {Name: "google", CIDRs: []string{prefix.String()}}},
		exact:    map[string][]DomainMatch{},
		suffix:   map[string][]DomainMatch{},
		prefixes: map[netip.Prefix][]IPMatch{prefix: {{RuleSet: "google", CIDR: prefix.String()}}},
	}
	for _, rule := range site.Rules {
		match := DomainMatch{RuleSet: site.Name, Kind: rule.Kind, Value: rule.Value, Attributes: rule.Attributes}
		switch rule.Kind {
		case model.DomainSuffix:
			d.suffix[rule.Value] = append(d.suffix[rule.Value], match)
		case model.DomainFull:
			d.exact[rule.Value] = append(d.exact[rule.Value], match)
		case model.DomainKeyword:
			d.keywords = append(d.keywords, match)
		case model.DomainRegex:
			d.regexes = append(d.regexes, indexedRegex{match: match, re: mustRegex(rule.Value)})
		}
	}
	return d
}

func TestRenderGeoSiteSkipsRegexAndSupportsFilter(t *testing.T) {
	d := testDataset()
	body, skipped, err := d.RenderGeoSite("example")
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{"DOMAIN-SUFFIX,example.com", "DOMAIN,exact.example.net", "DOMAIN-KEYWORD,keyword"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered rules %q do not contain %q", got, want)
		}
	}
	if skipped != 1 {
		t.Fatalf("skipped regex = %d, want 1", skipped)
	}

	filtered, _, err := d.RenderGeoSite("example@cn")
	if err != nil {
		t.Fatal(err)
	}
	if string(filtered) != "DOMAIN,exact.example.net\n" {
		t.Fatalf("filtered rules = %q", filtered)
	}
}

func TestLookupDomain(t *testing.T) {
	d := testDataset()
	for _, domain := range []string{"www.example.com", "exact.example.net", "has-keyword.test", "api42.example.org"} {
		matches, err := d.LookupDomain(domain)
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 1 {
			t.Fatalf("LookupDomain(%q) returned %d matches, want 1: %#v", domain, len(matches), matches)
		}
	}
}

func TestRenderAndLookupGeoIP(t *testing.T) {
	d := testDataset()
	body, err := d.RenderGeoIP("google")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "IP-CIDR,8.8.8.0/24,no-resolve\n" {
		t.Fatalf("geoip rules = %q", body)
	}
	matches, err := d.LookupIP("8.8.8.8")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].RuleSet != "google" {
		t.Fatalf("LookupIP returned %#v", matches)
	}
}

func mustRegex(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}
