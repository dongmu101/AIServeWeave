package comfyuimanagedapi

import "errors"

// errNoToken and errNoSource are construction failures, the same restraint
// modelpullapi's own errors.go documents: a listener that can start or stop
// a container must not start unauthenticated, and must not start pointed at
// nothing.
//
// errNoToken 与 errNoSource 是构造失败，与 modelpullapi 自己 errors.go 里的
// 克制相同：一个能启停容器的监听器，不能以未认证的方式启动，也不能在没有
// 指向任何东西时启动。
var (
	errNoToken  = errors.New("comfyuimanagedapi: a token is required; set AISW_GATEWAY_COMFYUI_MANAGED_TOKEN")
	errNoSource = errors.New("comfyuimanagedapi: Trigger, Status and HasActiveJob are all required")
)
