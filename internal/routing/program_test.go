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
	if decision.Policy != "Specific" || decision.Matched == nil || decision.Matched.Rule.Line != 1 || len(decision.Trace) != 3 {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

func TestOrderDiagnostics(t *testing.T) {
	dataset := routingDataset()
	for _, test := range []struct {
		name, query, text, policy                        string
		winner, matched, shadowed, conflicting, overlaps int
	}{
		{"filtered overlap", "exact.example.cn", "# first line\nGEOSITE,shared,Proxy\n\nGEOSITE,cn@local,DIRECT\nGEOIP,cn,Unused", "Proxy", 2, 2, 1, 1, 2},
		{"reordered", "exact.example.cn", "GEOSITE,cn@local,DIRECT\nGEOSITE,shared,Proxy", "DIRECT", 1, 2, 1, 1, 2},
		{"same policy", "exact.example.cn", "GEOSITE,shared,DIRECT\nGEOSITE,cn,DIRECT", "DIRECT", 1, 2, 1, 0, 2},
		{"duplicate reference", "exact.example.cn", "GEOSITE,CN,DIRECT\nRULE-SET,geosite/cn,Proxy", "DIRECT", 1, 2, 1, 1, 0},
		{"no match", "unrelated.test", "GEOSITE,shared,DIRECT\nGEOSITE,cn,Proxy\nGEOIP,cn,DIRECT", "", 0, 0, 0, 0, 0},
		{"IP overlap", "10.1.2.3", "GEOSITE,cn,Unused\nGEOIP,cn,DIRECT\nGEOIP,private,Proxy", "DIRECT", 2, 2, 1, 1, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			program, err := Parse(test.text)
			if err != nil {
				t.Fatal(err)
			}
			if err := program.Validate(dataset); err != nil {
				t.Fatal(err)
			}
			decision, err := program.Evaluate(test.query, dataset)
			if err != nil {
				t.Fatal(err)
			}
			d := decision.Diagnostics
			if decision.Policy != test.policy || len(decision.Trace) != len(program.Rules) || d.MatchedRules != test.matched || d.ShadowedRules != test.shadowed || d.ConflictingRules != test.conflicting || len(d.OverlappingSets) != test.overlaps {
				t.Fatalf("unexpected diagnosis: %+v", decision)
			}
			if test.winner == 0 {
				if decision.Matched != nil {
					t.Fatal("no-match query chose a winner")
				}
			} else if decision.Matched == nil || decision.Matched.Rule.Line != test.winner || !decision.Matched.Effective {
				t.Fatalf("wrong winner: %+v", decision.Matched)
			}
			for _, item := range decision.Trace {
				if item.Matched && !item.Effective && (item.ShadowedBy != test.winner || item.PolicyConflict != (item.Rule.Policy != test.policy)) {
					t.Fatalf("wrong shadow evidence: %+v", item)
				}
				if !item.Matched && (item.ShadowedBy != 0 || item.Effective || item.PolicyConflict) {
					t.Fatalf("non-match marked shadowed: %+v", item)
				}
			}
			for _, set := range d.OverlappingSets {
				if set.Detail == "" || len(set.Lines) == 0 {
					t.Fatalf("overlap lacks evidence: %+v", set)
				}
			}
		})
	}
}

func TestOverlappingSetsGroupRepeatedReferences(t *testing.T) {
	program, err := ParseGeo("GEOSITE,shared,Proxy\nGEOSITE,SHARED,Proxy\nGEOSITE,cn,DIRECT")
	if err != nil {
		t.Fatal(err)
	}
	decision, err := program.Evaluate("exact.example.cn", routingDataset())
	if err != nil {
		t.Fatal(err)
	}
	sets := decision.Diagnostics.OverlappingSets
	if len(sets) != 2 || sets[0].Name != "shared" || len(sets[0].Lines) != 2 || sets[0].Lines[0] != 1 || sets[0].Lines[1] != 2 || sets[1].Name != "cn" {
		t.Fatalf("duplicate reference inflated overlap: %+v", sets)
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
	data = protowire.AppendBytes(data, site)
	var sharedDomain []byte
	sharedDomain = protowire.AppendTag(sharedDomain, 1, protowire.VarintType)
	sharedDomain = protowire.AppendVarint(sharedDomain, 2)
	sharedDomain = protowire.AppendTag(sharedDomain, 2, protowire.BytesType)
	sharedDomain = protowire.AppendString(sharedDomain, "example.cn")
	var shared []byte
	shared = protowire.AppendTag(shared, 1, protowire.BytesType)
	shared = protowire.AppendString(shared, "shared")
	shared = protowire.AppendTag(shared, 2, protowire.BytesType)
	shared = protowire.AppendBytes(shared, sharedDomain)
	data = protowire.AppendTag(data, 1, protowire.BytesType)
	return protowire.AppendBytes(data, shared)
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
	data = protowire.AppendBytes(data, set)
	var privateCIDR []byte
	privateCIDR = protowire.AppendTag(privateCIDR, 1, protowire.BytesType)
	privateCIDR = protowire.AppendBytes(privateCIDR, []byte{10, 1, 0, 0})
	privateCIDR = protowire.AppendTag(privateCIDR, 2, protowire.VarintType)
	privateCIDR = protowire.AppendVarint(privateCIDR, 16)
	var private []byte
	private = protowire.AppendTag(private, 1, protowire.BytesType)
	private = protowire.AppendString(private, "private")
	private = protowire.AppendTag(private, 2, protowire.BytesType)
	private = protowire.AppendBytes(private, privateCIDR)
	data = protowire.AppendTag(data, 1, protowire.BytesType)
	return protowire.AppendBytes(data, private)
}
