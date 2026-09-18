package rules

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yyysay/surge-geo-server/internal/dat"
	"github.com/yyysay/surge-geo-server/internal/model"
)

type DomainMatch struct {
	RuleSet    string           `json:"rule_set"`
	Kind       model.DomainKind `json:"kind"`
	Value      string           `json:"value"`
	Attributes []string         `json:"attributes,omitempty"`
}

type IPMatch struct {
	RuleSet string `json:"rule_set"`
	CIDR    string `json:"cidr"`
}

type Status struct {
	LoadedAt    time.Time `json:"loaded_at"`
	GeoSiteSets int       `json:"geosite_sets"`
	GeoIPSets   int       `json:"geoip_sets"`
	GeoSiteHash string    `json:"geosite_hash"`
	GeoIPHash   string    `json:"geoip_hash"`
}

type RegexReport struct {
	Exact    int
	Degraded int
	Skipped  int
}

type indexedRegex struct {
	match DomainMatch
	re    *regexp.Regexp
}

// RuleSet holds immutable output and its precomputed HTTP validator.
// Returning strings avoids copying the entire cached response on every request.
type RuleSet struct {
	Body   string
	ETag   string
	Report RegexReport
}

func newRuleSet(body string, report RegexReport) RuleSet {
	hash := sha256.Sum256([]byte(body))
	return RuleSet{Body: body, ETag: fmt.Sprintf("\"%x\"", hash[:12]), Report: report}
}

type Dataset struct {
	domainOnce sync.Once
	ipOnce     sync.Once
	sites      map[string]model.GeoSite
	geoips     map[string]model.GeoIP
	exact      map[string][]DomainMatch
	suffix     map[string][]DomainMatch
	keywords   []DomainMatch
	regexes    []indexedRegex
	prefixes   map[netip.Prefix][]IPMatch
	status     Status
	regexMode  RegexMode
	geosites   sync.Map
	geoipsOut  sync.Map
}

func New(geositeData, geoipData []byte, regexMode RegexMode) (*Dataset, error) {
	if _, err := ParseRegexMode(string(regexMode)); err != nil {
		return nil, err
	}
	sites, err := dat.ParseGeoSite(geositeData)
	if err != nil {
		return nil, err
	}
	geoips, err := dat.ParseGeoIP(geoipData)
	if err != nil {
		return nil, err
	}

	d := &Dataset{
		sites:     make(map[string]model.GeoSite, len(sites)),
		geoips:    make(map[string]model.GeoIP, len(geoips)),
		regexMode: regexMode,
	}
	for _, site := range sites {
		d.sites[site.Name] = site
	}
	for _, set := range geoips {
		d.geoips[set.Name] = set
	}
	geositeHash := sha256.Sum256(geositeData)
	geoipHash := sha256.Sum256(geoipData)
	d.status = Status{
		LoadedAt:    time.Now().UTC(),
		GeoSiteSets: len(sites),
		GeoIPSets:   len(geoips),
		GeoSiteHash: fmt.Sprintf("%x", geositeHash[:8]),
		GeoIPHash:   fmt.Sprintf("%x", geoipHash[:8]),
	}
	return d, nil
}

// Subscription rendering needs only the raw sets. Lookup indexes are independent
// and initialized once, only when the corresponding query API is used.
func (d *Dataset) indexDomains() {
	started := time.Now()
	defer func() {
		slog.Debug("domain lookup index built", "exact", len(d.exact), "suffix", len(d.suffix), "regex", len(d.regexes), "duration", time.Since(started))
	}()
	d.exact = make(map[string][]DomainMatch)
	d.suffix = make(map[string][]DomainMatch)
	compiled := make(map[string]*regexp.Regexp)
	for _, site := range d.sites {
		for _, rule := range site.Rules {
			match := DomainMatch{RuleSet: site.Name, Kind: rule.Kind, Value: rule.Value, Attributes: rule.Attributes}
			switch rule.Kind {
			case model.DomainFull:
				d.exact[rule.Value] = append(d.exact[rule.Value], match)
			case model.DomainSuffix:
				d.suffix[rule.Value] = append(d.suffix[rule.Value], match)
			case model.DomainKeyword:
				d.keywords = append(d.keywords, match)
			case model.DomainRegex:
				re, exists := compiled[rule.Value]
				if !exists {
					re, _ = regexp.Compile(rule.Value)
					compiled[rule.Value] = re
				}
				if re != nil {
					d.regexes = append(d.regexes, indexedRegex{match: match, re: re})
				}
			}
		}
	}
}

func (d *Dataset) indexIPs() {
	started := time.Now()
	defer func() {
		slog.Debug("IP lookup index built", "prefixes", len(d.prefixes), "duration", time.Since(started))
	}()
	d.prefixes = make(map[netip.Prefix][]IPMatch)
	for _, set := range d.geoips {
		for _, cidr := range set.CIDRs {
			prefix, err := netip.ParsePrefix(cidr)
			if err == nil {
				prefix = prefix.Masked()
				d.prefixes[prefix] = append(d.prefixes[prefix], IPMatch{RuleSet: set.Name, CIDR: cidr})
			}
		}
	}
}

func (d *Dataset) Status() Status { return d.status }

func (d *Dataset) GeoSiteIndex() map[string][]string {
	result := make(map[string][]string, len(d.sites))
	for name, site := range d.sites {
		seen := make(map[string]struct{})
		for _, rule := range site.Rules {
			for _, attr := range rule.Attributes {
				seen[attr] = struct{}{}
			}
		}
		filters := make([]string, 0, len(seen))
		for filter := range seen {
			filters = append(filters, filter)
		}
		sort.Strings(filters)
		result[name] = filters
	}
	return result
}

func (d *Dataset) GeoSite(name string) (model.GeoSite, error) {
	setName, filter := splitFilter(strings.ToLower(name))
	site, ok := d.sites[setName]
	if !ok {
		return model.GeoSite{}, fmt.Errorf("unknown geosite rule set %q", setName)
	}
	rules := make([]model.DomainRule, 0, len(site.Rules))
	for _, rule := range site.Rules {
		if filter != "" && !contains(rule.Attributes, filter) {
			continue
		}
		rule.Attributes = append([]string(nil), rule.Attributes...)
		rules = append(rules, rule)
	}
	return model.GeoSite{Name: setName, Rules: rules}, nil
}

func (d *Dataset) HasGeoSite(name string) bool {
	setName, filter := splitFilter(strings.ToLower(name))
	site, ok := d.sites[setName]
	if !ok {
		return false
	}
	if filter == "" {
		return true
	}
	for _, rule := range site.Rules {
		if contains(rule.Attributes, filter) {
			return true
		}
	}
	return false
}

func (d *Dataset) GeoIPIndex() []string {
	result := make([]string, 0, len(d.geoips))
	for name := range d.geoips {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func (d *Dataset) HasGeoIP(name string) bool {
	_, ok := d.geoips[strings.ToLower(name)]
	return ok
}

func (d *Dataset) RenderGeoSite(name string) (RuleSet, error) {
	setName, filter := splitFilter(strings.ToLower(name))
	cacheKey := setName
	if filter != "" {
		cacheKey += "@" + filter
	}
	if cached, ok := d.geosites.Load(cacheKey); ok {
		return cached.(RuleSet), nil
	}
	site, ok := d.sites[setName]
	if !ok {
		return RuleSet{}, fmt.Errorf("unknown geosite rule set %q", setName)
	}
	if filter != "" && !d.HasGeoSite(cacheKey) {
		return RuleSet{}, fmt.Errorf("unknown geosite filter %q", filter)
	}
	lines := make([]string, 0, len(site.Rules))
	seen := make(map[string]struct{}, len(site.Rules))
	var report RegexReport
	for _, rule := range site.Rules {
		if filter != "" && !contains(rule.Attributes, filter) {
			continue
		}
		var line string
		switch rule.Kind {
		case model.DomainFull:
			line = "DOMAIN," + rule.Value
		case model.DomainSuffix:
			line = "DOMAIN-SUFFIX," + rule.Value
		case model.DomainKeyword:
			line = "DOMAIN-KEYWORD," + rule.Value
		case model.DomainRegex:
			regexLines, conversion := downgradeRegex(rule.Value, d.regexMode)
			switch conversion {
			case regexExact:
				report.Exact++
			case regexDegraded:
				report.Degraded++
			case regexSkipped:
				report.Skipped++
			}
			for _, regexLine := range regexLines {
				if _, exists := seen[regexLine]; !exists {
					seen[regexLine] = struct{}{}
					lines = append(lines, regexLine)
				}
			}
			continue
		}
		if _, exists := seen[line]; !exists {
			seen[line] = struct{}{}
			lines = append(lines, line)
		}
	}
	body := strings.Join(lines, "\n") + "\n"
	rendered := newRuleSet(body, report)
	cached, _ := d.geosites.LoadOrStore(cacheKey, rendered)
	return cached.(RuleSet), nil
}

func (d *Dataset) RenderGeoIP(name string) (RuleSet, error) {
	cacheKey := strings.ToLower(name)
	if cached, ok := d.geoipsOut.Load(cacheKey); ok {
		return cached.(RuleSet), nil
	}
	set, ok := d.geoips[cacheKey]
	if !ok {
		return RuleSet{}, fmt.Errorf("unknown geoip rule set %q", name)
	}
	lines := make([]string, 0, len(set.CIDRs))
	for _, cidr := range set.CIDRs {
		kind := "IP-CIDR"
		if strings.ContainsRune(cidr, ':') {
			kind = "IP-CIDR6"
		}
		lines = append(lines, kind+","+cidr+",no-resolve")
	}
	body := strings.Join(lines, "\n") + "\n"
	cached, _ := d.geoipsOut.LoadOrStore(cacheKey, newRuleSet(body, RegexReport{}))
	return cached.(RuleSet), nil
}

func (d *Dataset) LookupDomain(value string) ([]DomainMatch, error) {
	domain := normalizeDomain(value)
	if domain == "" {
		return nil, fmt.Errorf("invalid domain")
	}
	d.domainOnce.Do(d.indexDomains)
	matches := append([]DomainMatch(nil), d.exact[domain]...)
	for suffix := domain; suffix != ""; {
		matches = append(matches, d.suffix[suffix]...)
		_, suffix, _ = strings.Cut(suffix, ".")
	}
	for _, match := range d.keywords {
		if strings.Contains(domain, match.Value) {
			matches = append(matches, match)
		}
	}
	for _, indexed := range d.regexes {
		if indexed.re.MatchString(domain) {
			matches = append(matches, indexed.match)
		}
	}
	return uniqueDomainMatches(matches), nil
}

func (d *Dataset) LookupIP(value string) ([]IPMatch, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("invalid IP address")
	}
	d.ipOnce.Do(d.indexIPs)
	maxBits := 128
	if addr.Is4() {
		maxBits = 32
	}
	var matches []IPMatch
	for bits := 0; bits <= maxBits; bits++ {
		prefix := netip.PrefixFrom(addr, bits).Masked()
		matches = append(matches, d.prefixes[prefix]...)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].RuleSet == matches[j].RuleSet {
			pi, _ := netip.ParsePrefix(matches[i].CIDR)
			pj, _ := netip.ParsePrefix(matches[j].CIDR)
			return pi.Bits() > pj.Bits()
		}
		return matches[i].RuleSet < matches[j].RuleSet
	})
	return matches, nil
}

func splitFilter(name string) (string, string) {
	set, filter, _ := strings.Cut(name, "@")
	return set, filter
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func normalizeDomain(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(value, ".")
	if value == "" || strings.ContainsAny(value, "/:@ ") {
		return ""
	}
	return value
}

func uniqueDomainMatches(matches []DomainMatch) []DomainMatch {
	positions := make(map[string]int, len(matches))
	result := make([]DomainMatch, 0, len(matches))
	for _, match := range matches {
		key := match.RuleSet + "\x00" + string(match.Kind) + "\x00" + match.Value
		if position, exists := positions[key]; exists {
			for _, attribute := range match.Attributes {
				if !contains(result[position].Attributes, attribute) {
					result[position].Attributes = append(result[position].Attributes, attribute)
				}
			}
			continue
		}
		match.Attributes = append([]string(nil), match.Attributes...)
		positions[key] = len(result)
		result = append(result, match)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].RuleSet == result[j].RuleSet {
			if result[i].Kind == result[j].Kind {
				return result[i].Value < result[j].Value
			}
			return result[i].Kind < result[j].Kind
		}
		return result[i].RuleSet < result[j].RuleSet
	})
	return result
}
