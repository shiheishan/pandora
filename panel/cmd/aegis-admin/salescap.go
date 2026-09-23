package main

import (
	"os"
	"strings"
)

// 销售能力的注入方。
//
// adminops.SalesCapability 是一道 fail-closed 的闸门：定价（CreatePlanPrice）
// 和上架（PublishPlanVersion）在没有显式授权时一律返回 503。接口、契约测试
// 都在，唯独没有人实现它、也没有人注入 —— NewService(pool) 只传了一个参数。
//
// 后果是这两个功能从上线起就是死的：后台建得出套餐，加不了价格，
// 也发布不了，报出来只有一句「服务暂时不可用，请稍后重试」。生产上四个
// 套餐的价格全是早期数据，之后再没人能加过一档。
//
// 这里补上缺失的注入方。原设计里它应当由发布校验器在绑定运行二进制、
// 数据库身份与目录契约之后授予；那套东西仓库里并不存在，硬造一个假的
// 「已校验」比现在更糟。所以退一步：把授权做成部署时的显式决定，
// 由 .env 里的一个变量控制，默认仍然关闭。
//
//   - 它保留了原设计最要紧的那条性质：授权在进程启动时定死，
//     任何请求都改不了自己的授权状态。
//   - 它没有实现的是「绑定二进制与目录契约」那一层。等发布校验器真正
//     做出来时，换掉这里的实现即可，闸门本身和调用方都不用动。
type envSalesCapability struct{ allowed bool }

func (c envSalesCapability) AllowsP0BSales() bool { return c.allowed }

// salesCapabilityFromEnv 读取部署方对销售能力的显式授权。
//
// 只认 "1" / "true" / "yes"，其余一律视为未授权 —— 包括空值和拼错的值。
// 含糊的配置按拒绝处理，这是这道闸门的本意。
func salesCapabilityFromEnv() envSalesCapability {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("AEGIS_SALES_ENABLED")))
	return envSalesCapability{allowed: v == "1" || v == "true" || v == "yes"}
}
