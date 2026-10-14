package main

import (
	"os"
	// 内嵌时区库：站点时区校验与按时区计算不依赖机器上装没装 tzdata。
	_ "time/tzdata"

	"github.com/aegispanel/aegis/internal/app"
)

func main() {
	os.Exit(app.Execute())
}
