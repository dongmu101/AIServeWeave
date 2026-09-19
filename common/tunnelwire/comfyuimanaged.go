package tunnelwire

import (
	"sort"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/comfyuimanagedstatus"
)

// ComfyUIManagedActionToProto encodes the lifecycle action a Gateway asks an
// Agent to apply to its one locally-declared Managed ComfyUI instance
// (STATUS.md's P2 ComfyUI Managed Docker deployment, subtask 2). There is
// deliberately no equivalent for an image, mount path, or any other spec
// field: see comfyuimanagedstatus's package doc for why.
func ComfyUIManagedActionToProto(a comfyuimanagedstatus.Action) *tunnelv1.ComfyUIManagedAction {
	return &tunnelv1.ComfyUIManagedAction{Action: comfyUIManagedActionTypeToProto(a)}
}

// ComfyUIManagedActionFromProto restores the requested action.
func ComfyUIManagedActionFromProto(pb *tunnelv1.ComfyUIManagedAction) comfyuimanagedstatus.Action {
	return comfyUIManagedActionTypeFromProto(pb.GetAction())
}

// ComfyUIManagedCustomNodeInstallTriggerToProto encodes a request to install
// one allowlisted custom node by name (STATUS.md's P2 ComfyUI Managed
// Docker deployment, subtask 4). There is deliberately no field for a
// repository URL: see this file's package doc and comfyuimanagedstatus's.
func ComfyUIManagedCustomNodeInstallTriggerToProto(name string) *tunnelv1.ComfyUIManagedCustomNodeInstallTrigger {
	return &tunnelv1.ComfyUIManagedCustomNodeInstallTrigger{Name: name}
}

// ComfyUIManagedCustomNodeInstallTriggerFromProto restores the requested
// custom node's name.
func ComfyUIManagedCustomNodeInstallTriggerFromProto(pb *tunnelv1.ComfyUIManagedCustomNodeInstallTrigger) string {
	return pb.GetName()
}

// ComfyUIManagedReportToProto encodes an Agent's current known Managed
// ComfyUI status (zero or one entry — an Agent manages at most one instance
// today). The caller is expected to have sorted statuses already
// (Supervisor.Snapshot does); this function does not re-sort, so two calls
// with the same input produce byte-identical output, which is what the
// Agent's change-detection relies on.
func ComfyUIManagedReportToProto(statuses []comfyuimanagedstatus.Status) *tunnelv1.ComfyUIManagedReport {
	instances := make([]*tunnelv1.ComfyUIManagedStatus, len(statuses))
	for i, st := range statuses {
		instances[i] = comfyUIManagedStatusToProto(st)
	}
	return &tunnelv1.ComfyUIManagedReport{Instances: instances}
}

// ComfyUIManagedReportFromProto restores an Agent's reported Managed ComfyUI
// statuses, sorted by container name regardless of wire order.
func ComfyUIManagedReportFromProto(pb *tunnelv1.ComfyUIManagedReport) []comfyuimanagedstatus.Status {
	instances := pb.GetInstances()
	out := make([]comfyuimanagedstatus.Status, len(instances))
	for i, inst := range instances {
		out[i] = comfyUIManagedStatusFromProto(inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ContainerName < out[j].ContainerName })
	return out
}

func comfyUIManagedStatusToProto(st comfyuimanagedstatus.Status) *tunnelv1.ComfyUIManagedStatus {
	customNodes := make([]*tunnelv1.ComfyUIManagedCustomNode, len(st.CustomNodes))
	for i, cn := range st.CustomNodes {
		customNodes[i] = &tunnelv1.ComfyUIManagedCustomNode{Name: cn.Name, Version: cn.Version}
	}
	return &tunnelv1.ComfyUIManagedStatus{
		ContainerName: st.ContainerName,
		State:         comfyUIManagedStateToProto(st.State),
		UpdatedUnixMs: st.UpdatedAt.UnixMilli(),
		CustomNodes:   customNodes,
	}
}

func comfyUIManagedStatusFromProto(pb *tunnelv1.ComfyUIManagedStatus) comfyuimanagedstatus.Status {
	pbNodes := pb.GetCustomNodes()
	customNodes := make([]comfyuimanagedstatus.CustomNodeStatus, len(pbNodes))
	for i, cn := range pbNodes {
		customNodes[i] = comfyuimanagedstatus.CustomNodeStatus{Name: cn.GetName(), Version: cn.GetVersion()}
	}
	sort.Slice(customNodes, func(i, j int) bool { return customNodes[i].Name < customNodes[j].Name })
	return comfyuimanagedstatus.Status{
		ContainerName: pb.GetContainerName(),
		State:         comfyUIManagedStateFromProto(pb.GetState()),
		UpdatedAt:     time.UnixMilli(pb.GetUpdatedUnixMs()).UTC(),
		CustomNodes:   customNodes,
	}
}

func comfyUIManagedActionTypeToProto(a comfyuimanagedstatus.Action) tunnelv1.ComfyUIManagedActionType {
	switch a {
	case comfyuimanagedstatus.ActionStart:
		return tunnelv1.ComfyUIManagedActionType_COMFYUI_MANAGED_ACTION_START
	case comfyuimanagedstatus.ActionStop:
		return tunnelv1.ComfyUIManagedActionType_COMFYUI_MANAGED_ACTION_STOP
	case comfyuimanagedstatus.ActionRestart:
		return tunnelv1.ComfyUIManagedActionType_COMFYUI_MANAGED_ACTION_RESTART
	default:
		return tunnelv1.ComfyUIManagedActionType_COMFYUI_MANAGED_ACTION_UNSPECIFIED
	}
}

func comfyUIManagedActionTypeFromProto(pb tunnelv1.ComfyUIManagedActionType) comfyuimanagedstatus.Action {
	switch pb {
	case tunnelv1.ComfyUIManagedActionType_COMFYUI_MANAGED_ACTION_START:
		return comfyuimanagedstatus.ActionStart
	case tunnelv1.ComfyUIManagedActionType_COMFYUI_MANAGED_ACTION_STOP:
		return comfyuimanagedstatus.ActionStop
	case tunnelv1.ComfyUIManagedActionType_COMFYUI_MANAGED_ACTION_RESTART:
		return comfyuimanagedstatus.ActionRestart
	default:
		return comfyuimanagedstatus.ActionUnspecified
	}
}

func comfyUIManagedStateToProto(s comfyuimanagedstatus.State) tunnelv1.ComfyUIManagedState {
	switch s {
	case comfyuimanagedstatus.StatePending:
		return tunnelv1.ComfyUIManagedState_COMFYUI_MANAGED_STATE_PENDING
	case comfyuimanagedstatus.StateStarting:
		return tunnelv1.ComfyUIManagedState_COMFYUI_MANAGED_STATE_STARTING
	case comfyuimanagedstatus.StateRunning:
		return tunnelv1.ComfyUIManagedState_COMFYUI_MANAGED_STATE_RUNNING
	case comfyuimanagedstatus.StateStopped:
		return tunnelv1.ComfyUIManagedState_COMFYUI_MANAGED_STATE_STOPPED
	case comfyuimanagedstatus.StateFailed:
		return tunnelv1.ComfyUIManagedState_COMFYUI_MANAGED_STATE_FAILED
	default:
		return tunnelv1.ComfyUIManagedState_COMFYUI_MANAGED_STATE_UNSPECIFIED
	}
}

func comfyUIManagedStateFromProto(pb tunnelv1.ComfyUIManagedState) comfyuimanagedstatus.State {
	switch pb {
	case tunnelv1.ComfyUIManagedState_COMFYUI_MANAGED_STATE_PENDING:
		return comfyuimanagedstatus.StatePending
	case tunnelv1.ComfyUIManagedState_COMFYUI_MANAGED_STATE_STARTING:
		return comfyuimanagedstatus.StateStarting
	case tunnelv1.ComfyUIManagedState_COMFYUI_MANAGED_STATE_RUNNING:
		return comfyuimanagedstatus.StateRunning
	case tunnelv1.ComfyUIManagedState_COMFYUI_MANAGED_STATE_STOPPED:
		return comfyuimanagedstatus.StateStopped
	case tunnelv1.ComfyUIManagedState_COMFYUI_MANAGED_STATE_FAILED:
		return comfyuimanagedstatus.StateFailed
	default:
		return comfyuimanagedstatus.StateUnspecified
	}
}
