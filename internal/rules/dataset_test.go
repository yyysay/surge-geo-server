package rules

import (
	"net/netip"
	"regexp"
	"strings"
	"testing"

	"github.com/yyysay/surge-geo-server/internal/model"
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
	body, report, err := d.RenderGeoSite("example")
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{"DOMAIN-SUFFIX,example.com", "DOMAIN,exact.example.net", "DOMAIN-KEYWORD,keyword"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered rules %q do not contain %q", got, want)
		}
	}
	if report.Skipped != 1 {
		t.Fatalf("regex report = %+v, want one skipped rule", report)
	}

	filtered, _, err := d.RenderGeoSite("example@cn")
	if err != nil {
		t.Fatal(err)
	}
	if string(filtered) != "DOMAIN,exact.example.net\n" {
		t.Fatalf("filtered rules = %q", filtered)
	}
}

func TestGeoSiteReturnsRawRulesAndSupportsFilter(t *testing.T) {
	d := testDataset()
	site, err := d.GeoSite("EXAMPLE")
	if err != nil {
		t.Fatal(err)
	}
	if site.Name != "example" || len(site.Rules) != 4 {
		t.Fatalf("site = %#v, want example with four raw rules", site)
	}
	filtered, err := d.GeoSite("example@cn")
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Rules) != 1 || filtered.Rules[0].Kind != model.DomainFull || filtered.Rules[0].Value != "exact.example.net" {
		t.Fatalf("filtered site = %#v", filtered)
	}
	if _, err := d.GeoSite("missing"); err == nil {
		t.Fatal("GeoSite(missing) succeeded")
	}
	if !d.HasGeoSite("example") || !d.HasGeoSite("example@cn") || d.HasGeoSite("example@missing") || d.HasGeoSite("missing") {
		t.Fatal("HasGeoSite returned an unexpected result")
	}
}

func TestRenderGeoSiteBalancedDegradesRegex(t *testing.T) {
	d := testDataset()
	d.regexMode = RegexBalanced
	body, report, err := d.RenderGeoSite("example")
	if err != nil {
		t.Fatal(err)
	}
	if report.Degraded != 1 || report.Exact != 0 || report.Skipped != 0 {
		t.Fatalf("regex report = %+v, want one degraded rule", report)
	}
	if !strings.Contains(string(body), "DOMAIN-WILDCARD,api[0-9]*.example.org") {
		t.Fatalf("rendered rules %q do not contain balanced regex fallback", body)
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

func TestUniqueDomainMatchesMergesAttributes(t *testing.T) {
	matches := uniqueDomainMatches([]DomainMatch{
		{RuleSet: "example", Kind: model.DomainFull, Value: "example.com", Attributes: []string{"cn"}},
		{RuleSet: "example", Kind: model.DomainFull, Value: "example.com", Attributes: []string{"local"}},
	})
	if len(matches) != 1 || !contains(matches[0].Attributes, "cn") || !contains(matches[0].Attributes, "local") {
		t.Fatalf("merged matches = %#v", matches)
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

func TestRenderedRuleSetsAreCachedWithoutSharingMutableBytes(t *testing.T) {
	d := testDataset()

	geositeBody, _, err := d.RenderGeoSite("EXAMPLE")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.geosites.Load("example"); !ok {
		t.Fatal("geosite render was not cached")
	}
	geositeBody[0] = 'X'
	cachedGeoSiteBody, _, err := d.RenderGeoSite("example")
	if err != nil {
		t.Fatal(err)
	}
	if cachedGeoSiteBody[0] == 'X' {
		t.Fatal("caller mutated the cached geosite response")
	}

	geoipBody, err := d.RenderGeoIP("GOOGLE")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.geoipsOut.Load("google"); !ok {
		t.Fatal("geoip render was not cached")
	}
	geoipBody[0] = 'X'
	cachedGeoIPBody, err := d.RenderGeoIP("google")
	if err != nil {
		t.Fatal(err)
	}
	if cachedGeoIPBody[0] == 'X' {
		t.Fatal("caller mutated the cached geoip response")
	}
}

func mustRegex(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}
