package handler

import (
	"net/http"

	"AIServeWeave/common/workflowtemplate"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// workflowTemplateStatus compares the current publication with fresh reads of
// every configured Gateway, the same shape routingStatus applies to routing.
//
// workflowTemplateStatus 将当前发布与全部已配置 Gateway 的新鲜读取结果比较，与
// routingStatus 对路由的做法同构。
func workflowTemplateStatus(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		count, digest, err := desiredWorkflowTemplateBundle(ctx, r)
		if err != nil {
			respondErr(w, err)
			return
		}
		report := ctx.Fleet.WorkflowTemplateStatus(r.Context(), count, digest)
		// A concurrent publication invalidates completion for the captured
		// bundle, the same race routingStatus closes for routing.
		//
		// 一次并发发布会使先前捕获整包的完成判断失效，与 routingStatus 对路由
		// 关闭的是同一处竞态。
		latestCount, latestDigest, err := desiredWorkflowTemplateBundle(ctx, r)
		if err != nil {
			respondErr(w, err)
			return
		}
		if latestCount != count || latestDigest != digest {
			report.DesiredTemplateCount = latestCount
			report.DesiredBundleDigest = latestDigest
			report.Complete = false
		}
		writeJSON(w, http.StatusOK, report)
	}
}

func desiredWorkflowTemplateBundle(ctx *svc.ServiceContext, r *http.Request) (int, string, error) {
	bundle, err := ctx.Logic.CurrentWorkflowTemplatesBundle(r.Context())
	if err != nil {
		return 0, "", err
	}
	infos := make([]workflowtemplate.RevisionInfo, len(bundle))
	for i, snap := range bundle {
		infos[i] = snap.RevisionInfo
	}
	digest, err := workflowtemplate.BundleDigest(infos)
	if err != nil {
		return 0, "", err
	}
	return len(bundle), digest, nil
}
