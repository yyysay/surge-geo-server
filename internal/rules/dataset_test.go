package rules

import (
	"net/netip"
	"strings"
	"sync"
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
		sites:  map[string]model.GeoSite{"example": site},
		geoips: map[string]model.GeoIP{"google": {Name: "google", CIDRs: []string{prefix.String()}}},
	}

	return d
}

func TestRenderGeoSiteSkipsRegexAndSupportsFilter(t *testing.T) {
	d := testDataset()
	body, err := d.RenderGeoSite("example")
	if err != nil {
		t.Fatal(err)
	}
	report := body.Report
	got := body.Body
	for _, want := range []string{"DOMAIN-SUFFIX,example.com", "DOMAIN,exact.example.net", "DOMAIN-KEYWORD,keyword"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered rules %q do not contain %q", got, want)
		}
	}
	if report.Skipped != 1 {
		t.Fatalf("regex report = %+v, want one skipped rule", report)
	}

	filtered, err := d.RenderGeoSite("example@cn")
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Body != "DOMAIN,exact.example.net\n" {
		t.Fatalf("filtered rules = %q", filtered.Body)
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
	body, err := d.RenderGeoSite("example")
	if err != nil {
		t.Fatal(err)
	}
	report := body.Report
	if report.Degraded != 1 || report.Exact != 0 || report.Skipped != 0 {
		t.Fatalf("regex report = %+v, want one degraded rule", report)
	}
	if !strings.Contains(body.Body, "DOMAIN-WILDCARD,api[0-9]*.example.org") {
		t.Fatalf("rendered rules %q do not contain balanced regex fallback", body.Body)
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
	if body.Body != "IP-CIDR,8.8.8.0/24,no-resolve\n" {
		t.Fatalf("geoip rules = %q", body.Body)
	}
	matches, err := d.LookupIP("8.8.8.8")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].RuleSet != "google" {
		t.Fatalf("LookupIP returned %#v", matches)
	}
}

func TestConcurrentRenderAndLookup(t *testing.T) {
	d := testDataset()
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			site, err := d.RenderGeoSite("EXAMPLE")
			if err != nil {
				t.Error(err)
				return
			}
			cached, err := d.RenderGeoSite("example")
			if err != nil || cached != site || cached.ETag == "" {
				t.Errorf("cached geosite = %+v, err = %v", cached, err)
			}
			ip, err := d.RenderGeoIP("GOOGLE")
			if err != nil {
				t.Error(err)
				return
			}
			cachedIP, err := d.RenderGeoIP("google")
			if err != nil || cachedIP != ip || cachedIP.ETag == "" {
				t.Errorf("cached geoip = %+v, err = %v", cachedIP, err)
			}
			matches, err := d.LookupDomain("api42.example.org")
			if err != nil || len(matches) != 1 {
				t.Errorf("domain matches = %v, err = %v", matches, err)
			}
			ips, err := d.LookupIP("8.8.8.8")
			if err != nil || len(ips) != 1 {
				t.Errorf("IP matches = %v, err = %v", ips, err)
			}
		})
	}
	wg.Wait()
}

func TestSubscriptionsDoNotBuildLookupIndexes(t *testing.T) {
	d := testDataset()
	if _, err := d.RenderGeoSite("example"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RenderGeoIP("google"); err != nil {
		t.Fatal(err)
	}
	if d.exact != nil || d.suffix != nil || d.prefixes != nil || len(d.regexes) != 0 {
		t.Fatal("subscription rendering built lookup indexes")
	}
	if _, err := d.RenderGeoSite("example@typo"); err == nil {
		t.Fatal("unknown filter was silently rendered as an empty subscription")
	}
	if _, err := d.LookupDomain("example.com"); err != nil {
		t.Fatal(err)
	}
	if d.exact == nil || d.prefixes != nil {
		t.Fatal("domain lookup must only build domain indexes")
	}
	if _, err := d.LookupIP("8.8.8.8"); err != nil {
		t.Fatal(err)
	}
	if d.prefixes == nil {
		t.Fatal("IP lookup did not build its index")
	}
}
