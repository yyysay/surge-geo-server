// Package dat parses the protobuf wire format used by v2fly geosite/geoip DAT files.
package dat

import (
	"fmt"
	"sort"
	"strings"

	"github.com/yangtudou/surge-geo-server/internal/model"
	"google.golang.org/protobuf/encoding/protowire"
)

func ParseGeoSite(data []byte) ([]model.GeoSite, error) {
	var sites []model.GeoSite
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, fmt.Errorf("geosite: invalid tag")
		}
		data = data[n:]
		if num != 1 || typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return nil, fmt.Errorf("geosite: invalid field")
			}
			data = data[n:]
			continue
		}

		entry, consumed := protowire.ConsumeBytes(data)
		if consumed < 0 {
			return nil, fmt.Errorf("geosite: invalid entry")
		}
		data = data[consumed:]
		site, err := parseGeoSiteEntry(entry)
		if err != nil {
			return nil, err
		}
		if site.Name != "" {
			sites = append(sites, site)
		}
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].Name < sites[j].Name })
	return sites, nil
}

func parseGeoSiteEntry(data []byte) (model.GeoSite, error) {
	var site model.GeoSite
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return site, fmt.Errorf("geosite entry: invalid tag")
		}
		data = data[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			value, consumed := protowire.ConsumeBytes(data)
			if consumed < 0 {
				return site, fmt.Errorf("geosite entry: invalid name")
			}
			data = data[consumed:]
			site.Name = strings.ToLower(string(value))
		case num == 2 && typ == protowire.BytesType:
			value, consumed := protowire.ConsumeBytes(data)
			if consumed < 0 {
				return site, fmt.Errorf("geosite entry: invalid domain")
			}
			data = data[consumed:]
			rule, err := parseDomain(value)
			if err != nil {
				return site, err
			}
			if rule.Value != "" {
				site.Rules = append(site.Rules, rule)
			}
		default:
			consumed := protowire.ConsumeFieldValue(num, typ, data)
			if consumed < 0 {
				return site, fmt.Errorf("geosite entry: invalid field")
			}
			data = data[consumed:]
		}
	}
	return site, nil
}

func parseDomain(data []byte) (model.DomainRule, error) {
	var rule model.DomainRule
	var domainType uint64
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return rule, fmt.Errorf("domain: invalid tag")
		}
		data = data[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			value, consumed := protowire.ConsumeVarint(data)
			if consumed < 0 {
				return rule, fmt.Errorf("domain: invalid type")
			}
			data = data[consumed:]
			domainType = value
		case num == 2 && typ == protowire.BytesType:
			value, consumed := protowire.ConsumeBytes(data)
			if consumed < 0 {
				return rule, fmt.Errorf("domain: invalid value")
			}
			data = data[consumed:]
			rule.Value = strings.ToLower(strings.TrimSpace(string(value)))
		case num == 3 && typ == protowire.BytesType:
			value, consumed := protowire.ConsumeBytes(data)
			if consumed < 0 {
				return rule, fmt.Errorf("domain: invalid attribute")
			}
			data = data[consumed:]
			if key := parseAttributeKey(value); key != "" {
				rule.Attributes = append(rule.Attributes, strings.ToLower(key))
			}
		default:
			consumed := protowire.ConsumeFieldValue(num, typ, data)
			if consumed < 0 {
				return rule, fmt.Errorf("domain: invalid field")
			}
			data = data[consumed:]
		}
	}
	switch domainType {
	case 0:
		rule.Kind = model.DomainKeyword
	case 1:
		rule.Kind = model.DomainRegex
	case 2:
		rule.Kind = model.DomainSuffix
	case 3:
		rule.Kind = model.DomainFull
	default:
		return rule, fmt.Errorf("domain: unknown type %d", domainType)
	}
	return rule, nil
}

func parseAttributeKey(data []byte) string {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return ""
		}
		data = data[n:]
		if num == 1 && typ == protowire.BytesType {
			value, consumed := protowire.ConsumeBytes(data)
			if consumed < 0 {
				return ""
			}
			return string(value)
		}
		consumed := protowire.ConsumeFieldValue(num, typ, data)
		if consumed < 0 {
			return ""
		}
		data = data[consumed:]
	}
	return ""
}
