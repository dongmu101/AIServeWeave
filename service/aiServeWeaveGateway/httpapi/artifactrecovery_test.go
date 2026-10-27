package httpapi_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// fakeArtifactRecovery is a stub httpapi.ArtifactRecoveryClient — the
// control-plane side of STATUS.md's Gateway 故障切换收尾, exercised here
// without a real control plane the same way fakeVerifier exercises
// authentication without one.
//
// fakeArtifactRecovery 是一个 httpapi.ArtifactRecoveryClient 的桩——
// STATUS.md「Gateway 故障切换收尾」的控制面一侧，这里在没有真实控制面的情况下
// 驱动它，与 fakeVerifier 在没有真实控制面时驱动认证是同一种做法。
type fakeArtifactRecovery struct {
	jobExists   map[string]bool
	artifacts   map[string][]httpapi.PersistedArtifact
	routes      map[string]httpapi.ArtifactRoute
	listErr     error
	jobExistErr error
}

func (f *fakeArtifactRecovery) JobExists(_ context.Context, _, jobID string) (bool, error) {
	if f.jobExistErr != nil {
		return false, f.jobExistErr
	}
	return f.jobExists[jobID], nil
}

func (f *fakeArtifactRecovery) ListPersistedArtifacts(_ context.Context, _, jobID string) ([]httpapi.PersistedArtifact, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.artifacts[jobID], nil
}

func (f *fakeArtifactRecovery) ArtifactRoute(_ context.Context, _, artifactID string) (httpapi.ArtifactRoute, error) {
	route, ok := f.routes[artifactID]
	if !ok {
		return httpapi.ArtifactRoute{}, errors.New("fakeArtifactRecovery: no such artifact")
	}
	return route, nil
}

// TestDownloadArtifactRecoversAnArtifactUnknownToThisReplica is STATUS.md's
// Gateway 故障切换收尾 closed loop for a download: an artifact id this
// replica's own jobStore has never seen — because a different replica ran
// the job, or this replica restarted — is still servable as long as the
// control plane knows a node/runtime that can produce its bytes live.
//
// TestDownloadArtifactRecoversAnArtifactUnknownToThisReplica 是 STATUS.md
// 「Gateway 故障切换收尾」在下载路径上的闭环：一个本副本自己的 jobStore 从未
// 见过的产物 id——因为是另一个副本跑的这个 job，或者本副本重启过——只要控制面
// 知道一个能实时产出其字节的 node/runtime，依然可以被提供服务。
func TestDownloadArtifactRecoversAnArtifactUnknownToThisReplica(t *testing.T) {
	recovery := &fakeArtifactRecovery{
		routes: map[string]httpapi.ArtifactRoute{
			"art_foreign": {
				JobID: "job_foreign", TenantID: "", Filename: "ComfyUI_00001_.png",
				Type: "output", NodeID: "node-comfy", RuntimeID: "comfy-1",
			},
		},
	}
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t), ArtifactRecoveryClient: recovery})
	connectArtifactNode(t, h, "node-comfy", "comfy-1", artifactWorkflowHandler("ComfyUI_00001_.png", nil))

	// This replica never submitted or listed job_foreign — no
	// finishedJob/listArtifacts call precedes this — so h.jobs has nothing on
	// it. The download must still succeed through the recovery fallback
	// alone.
	//
	// 本副本从未提交或列举过 job_foreign——这里没有先调用
	// finishedJob/listArtifacts——因此 h.jobs 对它一无所知。这次下载必须仅靠
	// 恢复回退就成功。
	resp, err := http.Get(srv.URL + "/v1/artifacts/art_foreign")
	if err != nil {
		t.Fatalf("GET artifact: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(got) != artifactBytes {
		t.Errorf("body = %q, want %q", got, artifactBytes)
	}
}

// TestDownloadArtifactRecoveryMissIsReportedLikeAnyUnknownArtifact covers the
// control plane also having no record: the caller must not be able to tell
// "never existed" apart from "recovery found nothing".
//
// TestDownloadArtifactRecoveryMissIsReportedLikeAnyUnknownArtifact 覆盖控制面
// 同样没有记录的情形：调用方必须无法区分「从未存在过」与「恢复也一无所获」。
func TestDownloadArtifactRecoveryMissIsReportedLikeAnyUnknownArtifact(t *testing.T) {
	recovery := &fakeArtifactRecovery{routes: map[string]httpapi.ArtifactRoute{}}
	srv, _ := newServer(t, httpapi.Config{Workflows: templates(t), ArtifactRecoveryClient: recovery})

	resp, err := http.Get(srv.URL + "/v1/artifacts/art_nope")
	if err != nil {
		t.Fatalf("GET artifact: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestListArtifactsFallsBackToPersistedArtifactsForAJobUnknownToThisReplica
// is the listing side of the same closed loop: a job this replica's jobStore
// has never seen answers with exactly the ids and filenames a previous
// listing already minted and the control plane confirmed, never a fresh set.
//
// TestListArtifactsFallsBackToPersistedArtifactsForAJobUnknownToThisReplica
// 是同一个闭环在列举一侧的对应：一个本副本 jobStore 从未见过的 job，应答的是
// 此前某次列举已经铸造、且控制面已确认的那些 id 与文件名，绝不是一套新铸造的。
func TestListArtifactsFallsBackToPersistedArtifactsForAJobUnknownToThisReplica(t *testing.T) {
	recovery := &fakeArtifactRecovery{
		jobExists: map[string]bool{"job_foreign": true},
		artifacts: map[string][]httpapi.PersistedArtifact{
			"job_foreign": {{ArtifactID: "art_foreign", Filename: "out.png", Type: "output"}},
		},
	}
	srv, _ := newServer(t, httpapi.Config{Workflows: templates(t), ArtifactRecoveryClient: recovery})

	resp, body := listArtifacts(t, srv.URL, "job_foreign")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(body.Data) != 1 || body.Data[0].ArtifactID != "art_foreign" {
		t.Errorf("artifacts = %+v, want exactly the persisted art_foreign record", body.Data)
	}
}

// TestListArtifactsFallsBackToJobNotFoundWhenTheControlPlaneHasNoRecordEither
// confirms a job unknown everywhere still answers "job_not_found", not an
// empty list — the two must stay distinguishable.
//
// TestListArtifactsFallsBackToJobNotFoundWhenTheControlPlaneHasNoRecordEither
// 确认一个哪里都不存在的 job 仍然应答"job_not_found"，而不是一份空列表——两者
// 必须保持可区分。
func TestListArtifactsFallsBackToJobNotFoundWhenTheControlPlaneHasNoRecordEither(t *testing.T) {
	recovery := &fakeArtifactRecovery{jobExists: map[string]bool{}}
	srv, _ := newServer(t, httpapi.Config{Workflows: templates(t), ArtifactRecoveryClient: recovery})

	resp, body := listArtifacts(t, srv.URL, "job_nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if len(body.Data) != 0 {
		t.Errorf("artifacts = %+v, want none reported alongside a 404", body.Data)
	}
}
