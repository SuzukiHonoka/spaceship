package dns

type Type string

const (
	TypeCommon Type = "common" // only supported now
	TypeDOT    Type = "dot"
	TypeDOH    Type = "doh"
)
