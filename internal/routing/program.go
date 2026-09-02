// Package routing parses and evaluates an ordered, Surge/Mihomo-style rule program.
package routing

import (
	"encoding/csv"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/yyysay/surge-geo-server/internal/rules"
)

const (
	maxProgramBytes = 2 << 20
	maxRules        = 10000
)

// Rule is one normalized rule in an ordered rule program.
type Rule struct {
	Line    int      `json:"line"`
	Type    string   `json:"type"`
	Value   string   `json:"value,omitempty"`
	Policy  string   `json:"policy"`
	Options []string `json:"options,omitempty"`

	prefix  netip.Prefix
	regex   *regexp.Regexp
	setType string
	setName string
}

// Program is immutable after parsing and safe for concurrent evaluation.
type Program struct {
	Rules    []Rule    `json:"rules"`
	LoadedAt time.Time `json:"loaded_at"`
}

type Evaluation struct {
	Rule       Rule   `json:"rule"`
	Applicable bool   `json:"applicable"`
	Matched    bool   `json:"matched"`
	Detail     string `json:"detail,omitempty"`
}

type Decision struct {
	Query      string       `json:"query"`
	Kind       string       `json:"kind"`
	Policy     string       `json:"policy,omitempty"`
	Matched    *Evaluation  `json:"matched,omitempty"`
	Trace      []Evaluation `json:"trace"`
	GeoMatches any          `json:"geo_matches,omitempty"`
}

func Parse(text string) (*Program, error) {
	if len(text) > maxProgramBytes {
		return nil, fmt.Errorf("rule program exceeds %d bytes", maxProgramBytes)
	}
	program := &Program{LoadedAt: time.Now().UTC()}
	terminalLine := 0
	for index, raw := range strings.Split(text, "\n") {
		lineNumber := index + 1
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "//") {
			continue
		}
		// Accept rules copied directly from a Mihomo YAML payload list.
		if strings.HasPrefix(line, "- ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
		}
		if terminalLine != 0 {
			return nil, fmt.Errorf("line %d: rule is unreachable after terminal rule on line %d", lineNumber, terminalLine)
		}
		rule, err := parseRule(lineNumber, line)
		if err != nil {
			return nil, err
		}
		program.Rules = append(program.Rules, rule)
		if len(program.Rules) > maxRules {
			return nil, fmt.Errorf("rule program exceeds %d rules", maxRules)
		}
		if rule.Type == "FINAL" || rule.Type == "MATCH" {
			terminalLine = lineNumber
		}
	}
	if len(program.Rules) == 0 {
		return nil, fmt.Errorf("rule program is empty")
	}
	return program, nil
}

// ParseGeo accepts the Mihomo GEOSITE/GEOIP subset used by the browser workspace.
func ParseGeo(text string) (*Program, error) {
	program, err := Parse(text)
	if err != nil {
		return nil, err
	}
	for _, rule := range program.Rules {
		if rule.Type != "GEOSITE" && rule.Type != "GEOIP" {
			return nil, fmt.Errorf("line %d: only GEOSITE and GEOIP rules are supported", rule.Line)
		}
	}
	return program, nil
}

func parseRule(lineNumber int, line string) (Rule, error) {
	reader := csv.NewReader(strings.NewReader(line))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1
	fields, err := reader.Read()
	if err != nil {
		return Rule{}, fmt.Errorf("line %d: %w", lineNumber, err)
	}
	for index := range fields {
		fields[index] = strings.TrimSpace(fields[index])
	}
	if len(fields) == 0 || fields[0] == "" {
		return Rule{}, fmt.Errorf("line %d: missing rule type", lineNumber)
	}

	typeName := strings.ToUpper(fields[0])
	rule := Rule{Line: lineNumber, Type: typeName}
	if typeName == "FINAL" || typeName == "MATCH" {
		if len(fields) != 2 || fields[1] == "" {
			return Rule{}, fmt.Errorf("line %d: %s requires a policy", lineNumber, typeName)
		}
		rule.Policy = fields[1]
		return rule, nil
	}
	if len(fields) < 3 || fields[1] == "" || fields[2] == "" {
		return Rule{}, fmt.Errorf("line %d: %s requires value and policy", lineNumber, typeName)
	}
	rule.Value = fields[1]
	rule.Policy = fields[2]
	if len(fields) > 3 {
		rule.Options = append([]string(nil), fields[3:]...)
	}

	switch typeName {
	case "DOMAIN", "DOMAIN-SUFFIX", "DOMAIN-KEYWORD":
		rule.Value = normalizeDomain(rule.Value)
		if rule.Value == "" {
			return Rule{}, fmt.Errorf("line %d: invalid domain value", lineNumber)
		}
	case "DOMAIN-WILDCARD":
		rule.Value = strings.ToLower(strings.TrimSpace(rule.Value))
		pattern, compileErr := wildcardRegex(rule.Value)
		if compileErr != nil {
			return Rule{}, fmt.Errorf("line %d: invalid domain wildcard: %w", lineNumber, compileErr)
		}
		rule.regex = pattern
	case "DOMAIN-REGEX":
		rule.regex, err = regexp.Compile(rule.Value)
		if err != nil {
			return Rule{}, fmt.Errorf("line %d: invalid domain regular expression: %w", lineNumber, err)
		}
	case "IP-CIDR", "IP-CIDR6":
		rule.prefix, err = netip.ParsePrefix(rule.Value)
		if err != nil {
			return Rule{}, fmt.Errorf("line %d: invalid CIDR: %w", lineNumber, err)
		}
		rule.prefix = rule.prefix.Masked()
		if typeName == "IP-CIDR" && !rule.prefix.Addr().Is4() {
			return Rule{}, fmt.Errorf("line %d: IP-CIDR requires an IPv4 prefix", lineNumber)
		}
		if typeName == "IP-CIDR6" && !rule.prefix.Addr().Is6() {
			return Rule{}, fmt.Errorf("line %d: IP-CIDR6 requires an IPv6 prefix", lineNumber)
		}
		rule.Value = rule.prefix.String()
	case "GEOSITE":
		rule.setType, rule.setName = "geosite", strings.ToLower(rule.Value)
	case "GEOIP":
		rule.setType, rule.setName = "geoip", strings.ToLower(rule.Value)
	case "RULE-SET":
		rule.setType, rule.setName = parseRuleSetReference(rule.Value)
		if rule.setType == "" {
			return Rule{}, fmt.Errorf("line %d: RULE-SET must reference geosite/<name> or geoip/<name>", lineNumber)
		}
	default:
		return Rule{}, fmt.Errorf("line %d: unsupported rule type %q", lineNumber, typeName)
	}
	return rule, nil
}

// Validate checks that every referenced Geosite and GeoIP set exists in the dataset.
func (p *Program) Validate(dataset *rules.Dataset) error {
	for _, rule := range p.Rules {
		switch rule.setType {
		case "geosite":
			if !dataset.HasGeoSite(rule.setName) {
				return fmt.Errorf("line %d: unknown geosite rule set or filter %q", rule.Line, rule.setName)
			}
		case "geoip":
			if !dataset.HasGeoIP(rule.setName) {
				return fmt.Errorf("line %d: unknown geoip rule set %q", rule.Line, rule.setName)
			}
		}
	}
	return nil
}

// Evaluate walks the program from top to bottom and stops at its first match.
func (p *Program) Evaluate(value string, dataset *rules.Dataset) (Decision, error) {
	query := strings.TrimSpace(value)
	decision := Decision{Query: query, Trace: make([]Evaluation, 0, len(p.Rules))}
	addr, ipErr := netip.ParseAddr(query)
	var domain string
	var domainMatches []rules.DomainMatch
	var ipMatches []rules.IPMatch
	if ipErr == nil {
		decision.Kind = "ip"
		matches, err := dataset.LookupIP(query)
		if err != nil {
			return Decision{}, err
		}
		ipMatches = matches
		decision.GeoMatches = matches
	} else {
		decision.Kind = "domain"
		domain = normalizeDomain(query)
		if domain == "" {
			return Decision{}, fmt.Errorf("invalid domain or IP address")
		}
		matches, err := dataset.LookupDomain(domain)
		if err != nil {
			return Decision{}, err
		}
		domainMatches = matches
		decision.GeoMatches = matches
	}

	for _, rule := range p.Rules {
		evaluation := evaluateRule(rule, decision.Kind, domain, addr, domainMatches, ipMatches)
		decision.Trace = append(decision.Trace, evaluation)
		if evaluation.Matched {
			decision.Policy = rule.Policy
			matched := evaluation
			decision.Matched = &matched
			break
		}
	}
	return decision, nil
}

func evaluateRule(rule Rule, kind, domain string, addr netip.Addr, domainMatches []rules.DomainMatch, ipMatches []rules.IPMatch) Evaluation {
	evaluation := Evaluation{Rule: rule}
	switch rule.Type {
	case "FINAL", "MATCH":
		evaluation.Applicable, evaluation.Matched = true, true
		evaluation.Detail = "terminal rule"
	case "DOMAIN":
		evaluation.Applicable = kind == "domain"
		evaluation.Matched = evaluation.Applicable && domain == rule.Value
	case "DOMAIN-SUFFIX":
		evaluation.Applicable = kind == "domain"
		evaluation.Matched = evaluation.Applicable && (domain == rule.Value || strings.HasSuffix(domain, "."+rule.Value))
	case "DOMAIN-KEYWORD":
		evaluation.Applicable = kind == "domain"
		evaluation.Matched = evaluation.Applicable && strings.Contains(domain, rule.Value)
	case "DOMAIN-WILDCARD", "DOMAIN-REGEX":
		evaluation.Applicable = kind == "domain"
		evaluation.Matched = evaluation.Applicable && rule.regex.MatchString(domain)
	case "IP-CIDR", "IP-CIDR6":
		evaluation.Applicable = kind == "ip" && addr.IsValid()
		evaluation.Matched = evaluation.Applicable && rule.prefix.Contains(addr)
	case "GEOSITE", "RULE-SET":
		if rule.setType == "geoip" {
			evaluation.Applicable = kind == "ip"
			if match, ok := matchingIPSet(rule.setName, ipMatches); ok {
				evaluation.Matched, evaluation.Detail = true, match.CIDR
			}
			break
		}
		evaluation.Applicable = kind == "domain"
		if match, ok := matchingDomainSet(rule.setName, domainMatches); ok {
			evaluation.Matched = true
			evaluation.Detail = domainMatchDetail(match)
		}
	case "GEOIP":
		evaluation.Applicable = kind == "ip"
		if match, ok := matchingIPSet(rule.setName, ipMatches); ok {
			evaluation.Matched, evaluation.Detail = true, match.CIDR
		}
	}
	return evaluation
}

func matchingDomainSet(reference string, matches []rules.DomainMatch) (rules.DomainMatch, bool) {
	setName, filter := splitFilter(reference)
	for _, match := range matches {
		if !strings.EqualFold(match.RuleSet, setName) {
			continue
		}
		if filter != "" && !contains(match.Attributes, filter) {
			continue
		}
		return match, true
	}
	return rules.DomainMatch{}, false
}

func matchingIPSet(name string, matches []rules.IPMatch) (rules.IPMatch, bool) {
	for _, match := range matches {
		if strings.EqualFold(match.RuleSet, name) {
			return match, true
		}
	}
	return rules.IPMatch{}, false
}

func domainMatchDetail(match rules.DomainMatch) string {
	detail := string(match.Kind) + ":" + match.Value
	if len(match.Attributes) > 0 {
		detail += " @" + strings.Join(match.Attributes, " @")
	}
	return detail
}

func parseRuleSetReference(value string) (string, string) {
	reference := strings.TrimSpace(value)
	if parsed, err := url.Parse(reference); err == nil && parsed.Host != "" {
		reference = parsed.Path
	}
	reference = strings.Trim(reference, "/")
	for _, separator := range []string{"/", ":"} {
		parts := strings.SplitN(reference, separator, 2)
		if len(parts) == 2 && parts[1] != "" {
			kind := strings.ToLower(parts[0])
			if kind == "geosite" || kind == "geoip" {
				return kind, strings.ToLower(parts[1])
			}
		}
	}
	return "", ""
}

func normalizeDomain(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(value, ".")
	if value == "" || strings.ContainsAny(value, "/:@ ") {
		return ""
	}
	return value
}

func wildcardRegex(pattern string) (*regexp.Regexp, error) {
	if pattern == "" || strings.ContainsAny(pattern, "/:@ ") {
		return nil, fmt.Errorf("empty or malformed pattern")
	}
	quoted := regexp.QuoteMeta(pattern)
	quoted = strings.ReplaceAll(quoted, `\*`, `.*`)
	quoted = strings.ReplaceAll(quoted, `\?`, `.`)
	return regexp.Compile("^(?:" + quoted + ")$")
}

func splitFilter(value string) (string, string) {
	parts := strings.SplitN(strings.ToLower(value), "@", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
