package dotimport

import . "os"

// Dotted 点导入后选择子消失，Refs 必须拒绝而不是报 0 处
func Dotted() string { return Getenv("FIXTURE") }
