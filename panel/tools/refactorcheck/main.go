// Command refactorcheck 为「只挪代码、不改行为」的重构提供三道机器证据。
//
//	compare  比对两个版本里每个包的全部顶层声明：gofmt 后的原文（含文档注释）、
//	         所在文件的构建约束、用到的导入路径，三者逐一相同才算纯挪动
//	sqlset   比对两个版本里整棵目录树的 SQL 字符串字面量多重集合（规范化空白），
//	         用于 SQL 跨包下沉：声明变了，SQL 一字不变
//	shatter  把仓库复制一份，再把包里每个顶层声明拆进随机命名的独立文件，
//	         用来验证测试不依赖文件名与声明先后
//
// 在 Go module 根目录（panel/）下运行：go run ./tools/refactorcheck help
package main

import (
	"fmt"
	"os"
)

const usage = `usage: go run ./tools/refactorcheck <command> [flags] [package dirs...]

compare  [-C module] [-base rev] [-head rev] [-tests] [dirs...]
    Compare top-level declarations of each package between two versions.
    -base defaults to HEAD; -head defaults to the working tree (tracked and
    untracked files). Without dirs, every directory with a changed .go file
    is compared. Exit status 1 when any declaration differs.

    before committing:      go run ./tools/refactorcheck compare
    verify commit X:        go run ./tools/refactorcheck compare -base X^ -head X
    another module (pdnd):  go run ./tools/refactorcheck compare -C ../pdnd -base X^ -head X

sqlset   [-C module] [-base rev] [-head rev] [-root dir] [-tests]
    Compare the multiset of SQL string literals (whitespace-normalised) in
    every .go file under -root (default internal) between two versions, for
    moving SQL across packages. Exit status 1 when any literal differs.

    verify commits A..B:    go run ./tools/refactorcheck sqlset -base A -head B

shatter  -out dir [-C module] [-keep regexp] [-skip regexp] [dirs...]
    Copy the repository's tracked and untracked (not ignored) files to -out,
    then split every top-level declaration of the packages into its own
    randomly named file. Test files and files matching -keep stay intact;
    packages whose module-relative dir matches -skip are left alone. Without
    dirs, every package of the module is shattered. Run the tests in the copy.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "compare":
		var ok bool
		ok, err = runCompare(os.Args[2:])
		if err == nil && !ok {
			os.Exit(1)
		}
	case "sqlset":
		var ok bool
		ok, err = runSQLSet(os.Args[2:])
		if err == nil && !ok {
			os.Exit(1)
		}
	case "shatter":
		err = runShatter(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "refactorcheck:", err)
		os.Exit(2)
	}
}
