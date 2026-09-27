package fixture

import osx "os"

// Aliased 用别名导入读环境变量，Refs 必须按导入名认出来
func Aliased() (string, func(string) (string, bool)) {
	return osx.Getenv("FIXTURE"), osx.LookupEnv
}
