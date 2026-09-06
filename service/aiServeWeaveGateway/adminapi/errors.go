package adminapi

import "errors"

// errNoToken and errNoSource are construction failures: both mean the
// deployment asked for this listener without giving it what it needs, and
// starting anyway would serve either an unauthenticated inventory or an empty
// one that looks like an idle fleet.
//
// errNoToken 与 errNoSource 是构造失败：两者都意味着部署要求启用这个监听器，却没有给
// 它所需的东西，而照样启动会导致要么提供一份未经认证的清单，要么提供一份看起来像
// 「机群空闲」的空清单。
var (
	errNoToken  = errors.New("adminapi: a token is required; set AISW_GATEWAY_ADMIN_TOKEN")
	errNoSource = errors.New("adminapi: a node source is required")
)
