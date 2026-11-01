package handler

import (
	"net/http"
	"strconv"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/redeemgate"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// renderInstallIssue converts an issue result to its wire form.
//
// renderInstallIssue 把签发结果转换为对外形式。
func renderInstallIssue(in logic.InstallIssue) types.InstallIssueResponse {
	return types.InstallIssueResponse{
		Credential: in.Credential, Code: in.Code, NodeID: in.NodeID, ExpiresAt: in.ExpiresAt,
		InstallBaseURL: in.InstallBaseURL, BootstrapToken: in.BootstrapToken, CASHA256: in.CASHA256,
		RegistryAddr: in.RegistryAddr, GatewayAddr: in.GatewayAddr,
	}
}

// createOperatorInstall issues an install credential for a new platform
// node. A failed issue is never retried here, and the response, which holds
// the only copy of the code or token, is never cached.
//
// createOperatorInstall 为一台新的平台节点签发安装凭据。签发失败不在这里重试；响应中含有
// 兑换码或令牌仅有的一份，绝不缓存。
func createOperatorInstall(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req types.CreateOperatorInstallRequest
		if !decode(w, r, &req) {
			return
		}
		if req.TTLMinutes < 15 || req.TTLMinutes > 1440 {
			writeError(w, http.StatusBadRequest, "ttl_minutes must be between 15 and 1440")
			return
		}
		out, err := ctx.Logic.CreateOperatorInstall(r.Context(), actor, req.RegistryAddr, req.GatewayAddr, req.Credential, time.Duration(req.TTLMinutes)*time.Minute)
		recordRegistryClientCall(ctx, err)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, renderInstallIssue(out))
	}
}

// createTenantInstall issues an install credential for a new node of the
// caller's tenant.
//
// createTenantInstall 为调用方商户的一台新节点签发安装凭据。
func createTenantInstall(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req types.CreateTenantInstallRequest
		if !decode(w, r, &req) {
			return
		}
		if req.TTLMinutes < 15 || req.TTLMinutes > 1440 {
			writeError(w, http.StatusBadRequest, "ttl_minutes must be between 15 and 1440")
			return
		}
		out, err := ctx.Logic.CreateTenantInstall(r.Context(), actor, req.Credential, time.Duration(req.TTLMinutes)*time.Minute)
		recordRegistryClientCall(ctx, err)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, renderInstallIssue(out))
	}
}

// admitInstall counts one attempt against the deployment-wide redemption
// bound. It writes the 503 or 429 itself and reports whether to continue.
//
// admitInstall 把一次尝试计入全部署范围的兑换上限。503 或 429 由它自己写出，并报告是否继续。
func admitInstall(ctx *svc.ServiceContext, w http.ResponseWriter, r *http.Request) bool {
	if ctx.RedeemGate == nil {
		return true
	}
	admitted, err := ctx.RedeemGate.Admit(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "service unavailable")
		return false
	}
	if !admitted {
		w.Header().Set("Retry-After", strconv.Itoa(int(redeemgate.Window/time.Second)))
		writeError(w, http.StatusTooManyRequests, "too many requests; try again later")
		return false
	}
	return true
}

// redeemInstallCode exchanges a one-time code for enrollment material. Every
// reason a code is refused answers the same 404, so nothing is learned by
// guessing; neither the request nor the response is logged or cached.
//
// redeemInstallCode 用一次性码换取接入材料。码被拒绝的每一种原因都回答同一个 404，因此
// 猜测得不到任何信息；请求与响应都不记录、不缓存。
func redeemInstallCode(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !admitInstall(ctx, w, r) {
			return
		}
		var req types.InstallRedeemRequest
		if !decode(w, r, &req) {
			return
		}
		out, err := ctx.Logic.RedeemInstallCode(r.Context(), req.Code)
		recordRegistryClientCall(ctx, err)
		if err != nil {
			if err == logic.ErrNotFound {
				writeError(w, http.StatusNotFound, "invalid_code")
				return
			}
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.InstallRedeemResponse{
			NodeID: out.NodeID, RegistryAddr: out.RegistryAddr, GatewayAddr: out.GatewayAddr,
			CAPEM: out.CAPEM, CASHA256: out.CASHA256, BootstrapToken: out.BootstrapToken, TokenExpiresAt: out.TokenExpiresAt,
		})
	}
}

// installCA returns the public CA for token-credential installs.
//
// installCA 返回令牌直写安装所用的公开 CA。
func installCA(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !admitInstall(ctx, w, r) {
			return
		}
		ca, err := ctx.Logic.InstallCA()
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.AgentEnrollmentCAResponse{CAPEM: ca.PEM, CASHA256: ca.SHA256})
	}
}
