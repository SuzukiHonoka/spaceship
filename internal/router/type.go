package router

type Type string

const (
	TypeExact   Type = "exact"
	TypeDomains Type = "domains"
	TypeCIDR    Type = "cidr"
	TypeRegex   Type = "regex"
	TypeDefault Type = "default"
)
