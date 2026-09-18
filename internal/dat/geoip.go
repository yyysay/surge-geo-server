package dat

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/yyysay/surge-geo-server/internal/model"
	"google.golang.org/protobuf/encoding/protowire"
)

func ParseGeoIP(data []byte) ([]model.GeoIP, error) {
	var sets []model.GeoIP
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, fmt.Errorf("geoip: invalid tag")
		}
		data = data[n:]
		if num != 1 || typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return nil, fmt.Errorf("geoip: invalid field")
			}
			data = data[n:]
			continue
		}
		entry, consumed := protowire.ConsumeBytes(data)
		if consumed < 0 {
			return nil, fmt.Errorf("geoip: invalid entry")
		}
		data = data[consumed:]
		set, err := parseGeoIPEntry(entry)
		if err != nil {
			return nil, err
		}
		if set.Name != "" {
			sets = append(sets, set)
		}
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].Name < sets[j].Name })
	return sets, nil
}

func parseGeoIPEntry(data []byte) (model.GeoIP, error) {
	var set model.GeoIP
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return set, fmt.Errorf("geoip entry: invalid tag")
		}
		data = data[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			value, consumed := protowire.ConsumeBytes(data)
			if consumed < 0 {
				return set, fmt.Errorf("geoip entry: invalid name")
			}
			data = data[consumed:]
			set.Name = strings.ToLower(string(value))
		case num == 2 && typ == protowire.BytesType:
			value, consumed := protowire.ConsumeBytes(data)
			if consumed < 0 {
				return set, fmt.Errorf("geoip entry: invalid cidr")
			}
			data = data[consumed:]
			cidr, err := parseCIDR(value)
			if err != nil {
				return set, err
			}
			set.CIDRs = append(set.CIDRs, cidr)
		default:
			consumed := protowire.ConsumeFieldValue(num, typ, data)
			if consumed < 0 {
				return set, fmt.Errorf("geoip entry: invalid field")
			}
			data = data[consumed:]
		}
	}
	return set, nil
}

func parseCIDR(data []byte) (string, error) {
	var raw []byte
	var bits uint64
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return "", fmt.Errorf("cidr: invalid tag")
		}
		data = data[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			value, consumed := protowire.ConsumeBytes(data)
			if consumed < 0 {
				return "", fmt.Errorf("cidr: invalid address")
			}
			data = data[consumed:]
			raw = value
		case num == 2 && typ == protowire.VarintType:
			value, consumed := protowire.ConsumeVarint(data)
			if consumed < 0 {
				return "", fmt.Errorf("cidr: invalid prefix")
			}
			data = data[consumed:]
			bits = value
		default:
			consumed := protowire.ConsumeFieldValue(num, typ, data)
			if consumed < 0 {
				return "", fmt.Errorf("cidr: invalid field")
			}
			data = data[consumed:]
		}
	}
	var addr netip.Addr
	switch len(raw) {
	case 4:
		addr = netip.AddrFrom4([4]byte(raw))
	case 16:
		addr = netip.AddrFrom16([16]byte(raw))
	default:
		return "", fmt.Errorf("cidr: invalid address length %d", len(raw))
	}
	prefix := netip.PrefixFrom(addr, int(bits))
	if !prefix.IsValid() {
		return "", fmt.Errorf("cidr: invalid prefix length %d", bits)
	}
	return prefix.Masked().String(), nil
}
