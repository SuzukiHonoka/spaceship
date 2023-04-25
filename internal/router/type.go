package router

type Type string

const (
	TypeExact   Type = "exact"
	TypeDomain  Type = "domain"
	TypeCIDR    Type = "cidr"
	TypeRegex   Type = "regex"
	TypeDefault Type = "default"
)
