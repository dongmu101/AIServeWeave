package modelpullapi

import "errors"

// errNoToken and errNoSource are construction failures, the same restraint
// adminapi's own errors.go documents: a listener that can make a node start
// downloading must not start unauthenticated, and must not start pointed at
// nothing.
//
// errNoToken 与 errNoSource 是构造失败，与 adminapi 自己 errors.go 里的克制
// 相同：一个能让节点开始下载的监听器，不能以未认证的方式启动，也不能在没有
// 指向任何东西时启动。
var (
	errNoToken  = errors.New("modelpullapi: a token is required; set AISW_GATEWAY_MODEL_PULL_TOKEN")
	errNoSource = errors.New("modelpullapi: Trigger and Status are both required")
)
