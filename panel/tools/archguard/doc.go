// Package archguard 只有测试：面板的结构守卫，随 go test ./... 扫描 panel/internal 的
// 非测试源码。
//
//   - imports_test.go：依赖方向（api → domain → platform；middleware 挂在 api 之前）。
//   - readonly_tx_test.go：只读事务——InTx 的闭包里只有 SELECT（没有 FOR UPDATE/SHARE、
//     没有写），白付 BEGIN 与 COMMIT 两次往返，应改用 QueryRowScoped / QueryScoped /
//     BatchScoped（一次往返）。
//
// 两个守卫的现有违例都登记在各自的豁免表里，是棘轮：修一个删一个；豁免过期
// （那一处已经改好或删掉）同样变红，逼着把表一起改小。
package archguard
