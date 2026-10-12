// Package module 下每个子目录是一个业务模块：model、repo、service、handler、task 五层，
// 每张表只有一个属主模块；分层与模块边界由 .golangci.yml 的 depguard 规则钉住（tools/depguardgen 生成）。
package module
