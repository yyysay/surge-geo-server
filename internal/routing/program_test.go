package routing

import (
	"testing"

	"github.com/yyysay/surge-geo-server/internal/rules"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestParsePreservesOrderAndAcceptsBothTerminalNames(t *testing.T) {
	program, err := Parse(`# comment
- DOMAIN-SUFFIX,example.com,Proxy
GEOSITE,cn,DIRECT
MATCH,Fallback`)
	if err != nil {
		t.Fatal(err)
	}
	if len(program.Rules) != 3 {
		t.Fatalf("got %d rules, want 3", len(program.Rules))
	}
	if program.Rules[0].Line != 2 || program.Rules[0].Type != "DOMAIN-SUFFIX" || program.Rules[2].Type != "MATCH" {
		t.Fatalf("unexpected normalized rules: %#v", program.Rules)
	}
	if _, err := Parse("FINAL,DIRECT\nDOMAIN,example.com,Proxy"); err == nil {
		t.Fatal("accepted an unreachable rule after FINAL")
	}
}

func TestParseGeoAcceptsOnlyMihomoGeoRules(t *testing.T) {
	program, err := ParseGeo("GEOSITE,cn,DIRECT\nGEOIP,cn,DIRECT,no-resolve")
	if err != nil || len(program.Rules) != 2 {
		t.Fatalf("program = %#v, err = %v", program, err)
	}
	for _, text := range []string{"MATCH,Proxy", "DOMAIN,example.com,Proxy"} {
		if _, err := ParseGeo(text); err == nil {
			t.Fatalf("ParseGeo(%q) accepted unsupported rule", text)
		}
	}
}

func TestEvaluateUsesFirstMatch(t *testing.T) {
	dataset := routingDataset()
	program, err := Parse(`DOMAIN-SUFFIX,example.com,Specific
GEOSITE,cn,DIRECT
FINAL,Fallback`)
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Validate(dataset); err != nil {
		t.Fatal(err)
	}
	decision, err := program.Evaluate("www.example.com", dataset)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Policy != "Specific" || decision.Matched == nil || decision.Matched.Rule.Line != 1 {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

func BenchmarkEvaluateGeoSite(b *testing.B) {
	dataset := routingDataset()
	program, err := ParseGeo("GEOSITE,cn,DIRECT\nGEOIP,cn,DIRECT,no-resolve")
	if err != nil {
		b.Fatal(err)
	}
	if err := program.Validate(dataset); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := program.Evaluate("exact.example.cn", dataset); err != nil {
			b.Fatal(err)
		}
	}
}

func TestEvaluateGeoReferencesAndIP(t *testing.T) {
	dataset := routingDataset()
	program, err := Parse(`GEOSITE,cn@local,DIRECT
RULE-SET,https://rules.example/geoip/cn,DIRECT
MATCH,Proxy`)
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Validate(dataset); err != nil {
		t.Fatal(err)
	}
	domainDecision, err := program.Evaluate("exact.example.cn", dataset)
	if err != nil {
		t.Fatal(err)
	}
	if domainDecision.Policy != "DIRECT" || domainDecision.Matched.Detail == "" {
		t.Fatalf("unexpected domain decision: %#v", domainDecision)
	}
	ipDecision, err := program.Evaluate("10.1.2.3", dataset)
	if err != nil {
		t.Fatal(err)
	}
	if ipDecision.Policy != "DIRECT" || ipDecision.Matched.Rule.Type != "RULE-SET" {
		t.Fatalf("unexpected IP decision: %#v", ipDecision)
	}
}

func TestValidateRejectsUnknownGeoSet(t *testing.T) {
	program, err := Parse("GEOSITE,missing,Proxy\nFINAL,DIRECT")
	if err != nil {
		t.Fatal(err)
	}
	if err := program.Validate(routingDataset()); err == nil {
		t.Fatal("validation accepted an unknown geosite set")
	}
}

func routingDataset() *rules.Dataset {
	dataset, err := rules.New(routingGeoSiteData(), routingGeoIPData(), rules.RegexStrict)
	if err != nil {
		panic(err)
	}
	return dataset
}

func routingGeoSiteData() []byte {
	var attribute []byte
	attribute = protowire.AppendTag(attribute, 1, protowire.BytesType)
	attribute = protowire.AppendString(attribute, "local")
	var domain []byte
	domain = protowire.AppendTag(domain, 1, protowire.VarintType)
	domain = protowire.AppendVarint(domain, 3)
	domain = protowire.AppendTag(domain, 2, protowire.BytesType)
	domain = protowire.AppendString(domain, "exact.example.cn")
	domain = protowire.AppendTag(domain, 3, protowire.BytesType)
	domain = protowire.AppendBytes(domain, attribute)
	var site []byte
	site = protowire.AppendTag(site, 1, protowire.BytesType)
	site = protowire.AppendString(site, "CN")
	site = protowire.AppendTag(site, 2, protowire.BytesType)
	site = protowire.AppendBytes(site, domain)
	var data []byte
	data = protowire.AppendTag(data, 1, protowire.BytesType)
	return protowire.AppendBytes(data, site)
}

func routingGeoIPData() []byte {
	var cidr []byte
	cidr = protowire.AppendTag(cidr, 1, protowire.BytesType)
	cidr = protowire.AppendBytes(cidr, []byte{10, 0, 0, 0})
	cidr = protowire.AppendTag(cidr, 2, protowire.VarintType)
	cidr = protowire.AppendVarint(cidr, 8)
	var set []byte
	set = protowire.AppendTag(set, 1, protowire.BytesType)
	set = protowire.AppendString(set, "CN")
	set = protowire.AppendTag(set, 2, protowire.BytesType)
	set = protowire.AppendBytes(set, cidr)
	var data []byte
	data = protowire.AppendTag(data, 1, protowire.BytesType)
	return protowire.AppendBytes(data, set)
}
