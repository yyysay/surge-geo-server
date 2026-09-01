package model

type DomainKind string

const (
	DomainKeyword DomainKind = "keyword"
	DomainRegex   DomainKind = "regex"
	DomainSuffix  DomainKind = "suffix"
	DomainFull    DomainKind = "full"
)

type DomainRule struct {
	Kind       DomainKind `json:"kind"`
	Value      string     `json:"value"`
	Attributes []string   `json:"attributes,omitempty"`
}

type GeoSite struct {
	Name  string       `json:"name"`
	Rules []DomainRule `json:"rules"`
}

type GeoIP struct {
	Name  string   `json:"name"`
	CIDRs []string `json:"cidrs"`
}
