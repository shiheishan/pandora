package fixture

// Plain 的文档注释不进 Decl 的结果
func Plain() string { return "plain" }

type Box[T any] struct{ v T }

func (b *Box[T]) Get() T { return b.v }

const (
	First  = "first"
	Second = "second"
)

var Single = "single"
