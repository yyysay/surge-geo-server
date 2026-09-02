package rules

import (
	"fmt"
	"regexp/syntax"
	"sort"
	"strings"
	"unicode"
)

type RegexMode string

const (
	RegexStrict   RegexMode = "strict"
	RegexBalanced RegexMode = "balanced"
)

const maxRegexExpansions = 512

func ParseRegexMode(value string) (RegexMode, error) {
	mode := RegexMode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case RegexStrict, RegexBalanced:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid regex mode %q (want strict or balanced)", value)
	}
}

type regexConversion int

const (
	regexSkipped regexConversion = iota
	regexExact
	regexDegraded
)

type globExpr struct {
	value string
	start bool
	end   bool
}

func downgradeRegex(pattern string, mode RegexMode) ([]string, regexConversion) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, regexSkipped
	}

	expressions, ok := compileGlob(re, false)
	conversion := regexExact
	if !ok && mode == RegexBalanced {
		expressions, ok = compileGlob(re, true)
		conversion = regexDegraded
	}
	if !ok {
		return nil, regexSkipped
	}
	if conversion == regexDegraded {
		expressions = constrainedExpressions(expressions)
	}

	lines := globLines(expressions)
	if len(lines) == 0 {
		return nil, regexSkipped
	}
	return lines, conversion
}

func compileGlob(re *syntax.Regexp, balanced bool) ([]globExpr, bool) {
	switch re.Op {
	case syntax.OpNoMatch:
		return nil, true
	case syntax.OpEmptyMatch:
		return []globExpr{{}}, true
	case syntax.OpBeginText:
		return []globExpr{{start: true}}, true
	case syntax.OpEndText:
		return []globExpr{{end: true}}, true
	case syntax.OpBeginLine, syntax.OpEndLine, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		if balanced {
			return []globExpr{{}}, true
		}
		return nil, false
	case syntax.OpLiteral:
		var value strings.Builder
		for _, r := range re.Rune {
			if !isDomainLiteral(r) {
				if balanced {
					value.WriteByte('?')
					continue
				}
				return nil, false
			}
			value.WriteRune(unicode.ToLower(r))
		}
		return []globExpr{{value: value.String()}}, true
	case syntax.OpCharClass:
		value, ok := globClass(re.Rune)
		if !ok {
			if balanced {
				return []globExpr{{value: "?"}}, true
			}
			return nil, false
		}
		return []globExpr{{value: value}}, true
	case syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return []globExpr{{value: "?"}}, true
	case syntax.OpCapture:
		return compileGlob(re.Sub[0], balanced)
	case syntax.OpConcat:
		result := []globExpr{{}}
		for _, sub := range re.Sub {
			part, ok := compileGlob(sub, balanced)
			if !ok {
				return nil, false
			}
			result, ok = concatGlob(result, part)
			if !ok {
				return nil, false
			}
		}
		return result, true
	case syntax.OpAlternate:
		var result []globExpr
		for _, sub := range re.Sub {
			part, ok := compileGlob(sub, balanced)
			if !ok || len(result)+len(part) > maxRegexExpansions {
				if balanced {
					return []globExpr{{value: "*"}}, true
				}
				return nil, false
			}
			result = append(result, part...)
		}
		return result, true
	case syntax.OpQuest:
		part, ok := compileGlob(re.Sub[0], balanced)
		if !ok || len(part)+1 > maxRegexExpansions {
			if balanced {
				return []globExpr{{value: "*"}}, true
			}
			return nil, false
		}
		return append([]globExpr{{}}, part...), true
	case syntax.OpRepeat:
		return compileRepeat(re.Sub[0], re.Min, re.Max, balanced)
	case syntax.OpStar, syntax.OpPlus:
		part, ok := compileGlob(re.Sub[0], balanced)
		if !ok || len(part) != 1 || part[0].start || part[0].end {
			if balanced {
				return []globExpr{{value: "*"}}, true
			}
			return nil, false
		}
		value := part[0].value
		if value == "" {
			return []globExpr{{}}, true
		}
		if value == "?" {
			if re.Op == syntax.OpPlus {
				return []globExpr{{value: "?*"}}, true
			}
			return []globExpr{{value: "*"}}, true
		}
		if balanced {
			if re.Op == syntax.OpPlus {
				return []globExpr{{value: value + "*"}}, true
			}
			return []globExpr{{value: "*"}}, true
		}
		return nil, false
	default:
		return nil, false
	}
}

func compileRepeat(sub *syntax.Regexp, min, max int, balanced bool) ([]globExpr, bool) {
	part, ok := compileGlob(sub, balanced)
	if !ok {
		return nil, false
	}
	if max < 0 || max > 16 {
		if !balanced {
			return nil, false
		}
		result := []globExpr{{}}
		required := min
		if required > 16 {
			required = 16
		}
		for range required {
			result, ok = concatGlob(result, part)
			if !ok {
				return []globExpr{{value: "*"}}, true
			}
		}
		for i := range result {
			result[i].value += "*"
		}
		return result, true
	}

	var result []globExpr
	current := []globExpr{{}}
	for count := 0; count <= max; count++ {
		if count >= min {
			if len(result)+len(current) > maxRegexExpansions {
				if balanced {
					return []globExpr{{value: "*"}}, true
				}
				return nil, false
			}
			result = append(result, current...)
		}
		if count < max {
			current, ok = concatGlob(current, part)
			if !ok {
				return nil, false
			}
		}
	}
	return result, true
}

func constrainedExpressions(expressions []globExpr) []globExpr {
	result := expressions[:0]
	for _, expression := range expressions {
		literalRunes := 0
		inClass := false
		for _, r := range expression.value {
			switch r {
			case '[':
				inClass = true
			case ']':
				inClass = false
			default:
				if !inClass && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
					literalRunes++
				}
			}
		}
		if literalRunes >= 3 {
			result = append(result, expression)
		}
	}
	return result
}

func concatGlob(left, right []globExpr) ([]globExpr, bool) {
	if len(left) == 0 || len(right) == 0 {
		return nil, true
	}
	if len(left) > maxRegexExpansions/len(right) {
		return nil, false
	}
	result := make([]globExpr, 0, len(left)*len(right))
	for _, a := range left {
		for _, b := range right {
			if (b.start && a.value != "") || (a.end && b.value != "") {
				return nil, false
			}
			result = append(result, globExpr{
				value: a.value + b.value,
				start: a.start || b.start,
				end:   a.end || b.end,
			})
		}
	}
	return result, true
}

func globLines(expressions []globExpr) []string {
	seen := make(map[string]struct{}, len(expressions))
	lines := make([]string, 0, len(expressions))
	for _, expression := range expressions {
		if expression.value == "" {
			continue
		}
		value := expression.value
		if !expression.start {
			value = "*" + value
		}
		if !expression.end {
			value += "*"
		}

		line := "DOMAIN-WILDCARD," + value
		if !strings.ContainsAny(value, "*?[") {
			line = "DOMAIN," + value
		} else if !expression.start && !expression.end && !strings.ContainsAny(expression.value, "*?[") {
			line = "DOMAIN-KEYWORD," + expression.value
		}
		if _, exists := seen[line]; exists {
			continue
		}
		seen[line] = struct{}{}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines
}

func globClass(ranges []rune) (string, bool) {
	if len(ranges)%2 != 0 || len(ranges) == 0 {
		return "", false
	}
	var value strings.Builder
	value.WriteByte('[')
	for i := 0; i < len(ranges); i += 2 {
		lo, hi := ranges[i], ranges[i+1]
		if !isDomainClassRange(lo, hi) {
			return "", false
		}
		if lo == hi {
			value.WriteRune(unicode.ToLower(lo))
			continue
		}
		value.WriteRune(unicode.ToLower(lo))
		value.WriteByte('-')
		value.WriteRune(unicode.ToLower(hi))
	}
	value.WriteByte(']')
	return value.String(), true
}

func isDomainLiteral(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_'
}

func isDomainClassRange(lo, hi rune) bool {
	if lo > hi {
		return false
	}
	return lo >= 'a' && hi <= 'z' || lo >= 'A' && hi <= 'Z' || lo >= '0' && hi <= '9' || lo == hi && (lo == '_' || lo == '-')
}
