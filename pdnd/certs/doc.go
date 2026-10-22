// Package certs 是节点端的入站证书管理：按配置文档里 cert_mode=acme 的标识符（域名或 IP）向 ACME CA
// 申请证书（IP 用 shortlived 档），落盘复用、按 ARI 续期、失败退避，经 core.CertSource 交给内核按握手取用。
// 私钥与账户密钥只在本机。
package certs
