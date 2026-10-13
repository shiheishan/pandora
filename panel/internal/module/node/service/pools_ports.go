package service

// 本文件归 p5-pools：node 要的目录（catalog）与用户组（identity）读写面，以及注入方法。
// 骨架里先是空接口，p5-pools 补齐方法。

// Catalog 是 node 要的目录读写面（套餐版本绑池），由 catalog service 实现。
type Catalog interface{}

// UserGroups 是 node 要的用户组读取面，由 identity service 实现。
type UserGroups interface{}

// UseCatalog 只在启动装配时调用（不并发安全）。
func (s *Service) UseCatalog(c Catalog) { s.catalog = c }

// UseUserGroups 只在启动装配时调用（不并发安全）。
func (s *Service) UseUserGroups(g UserGroups) { s.groups = g }
