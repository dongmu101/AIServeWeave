"use client";

import * as React from "react";

import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { ErrorState, FormError } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  comfyUIManagedCustomNodeInstallRequest,
  comfyUIManagedStatusRequest,
  comfyUIManagedTriggerRequest,
  type ComfyUIManagedAction,
  type ComfyUIManagedStatus,
} from "@/lib/console/comfyui-managed";
import { describe } from "@/lib/console/errors";
import { formatDateTime } from "@/lib/console/format";
import { nodePathSegment } from "@/lib/console/node-path";

const ACTION_LABELS: Record<ComfyUIManagedAction, string> = {
  start: "启动", stop: "停止", restart: "重启",
};

const ACTION_EFFECTS: Record<ComfyUIManagedAction, string> = {
  start: "让容器存在并运行——已在运行的同名容器会被原样接管，不会重建。",
  stop: "先取消运行时注册再停止并移除容器；调度器停止向它派发新请求后再拆容器。",
  restart: "先停止再启动，让容器按节点当前的本地配置重建。本副本若仍追踪着路由到该节点的非终态 job 会被拒绝（409）；那种情况下需要等 job 结束后再重试。",
};

const STATE_LABELS: Record<string, string> = {
  pending: "尚未创建", starting: "启动中", running: "运行中", stopped: "已停止", failed: "失败", unspecified: "未知",
};

/** ComfyUIManagedView is the per-node lookup + control panel described in
 * page.tsx's doc comment.
 *
 * ComfyUIManagedView 是 page.tsx 文档注释所述的按节点查询与操作面板。 */
export function ComfyUIManagedView() {
  const run = useConsoleRequest();
  const [nodeIdInput, setNodeIdInput] = React.useState("");
  const [nodeId, setNodeId] = React.useState<string | null>(null);
  const [status, setStatus] = React.useState<ComfyUIManagedStatus | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [queryError, setQueryError] = React.useState<string | null>(null);
  const [notice, setNotice] = React.useState<string | null>(null);
  const [confirmAction, setConfirmAction] = React.useState<ComfyUIManagedAction | null>(null);
  const [customNodeName, setCustomNodeName] = React.useState("");
  const [installPending, setInstallPending] = React.useState(false);
  const [installError, setInstallError] = React.useState<string | null>(null);

  async function query(event: React.FormEvent) {
    event.preventDefault();
    const id = nodeIdInput.trim();
    if (!nodePathSegment(id)) {
      setQueryError("节点 ID 不能为空，且不能包含路径分隔符或控制字符。");
      return;
    }
    setLoading(true);
    setQueryError(null);
    setNotice(null);
    try {
      const result = await run(comfyUIManagedStatusRequest(id));
      setStatus(result);
      setNodeId(id);
    } catch (failure) {
      setQueryError(describe(failure));
      setStatus(null);
      setNodeId(null);
    } finally {
      setLoading(false);
    }
  }

  async function reload() {
    if (!nodeId) {
      return;
    }
    setLoading(true);
    setQueryError(null);
    try {
      setStatus(await run(comfyUIManagedStatusRequest(nodeId)));
    } catch (failure) {
      setQueryError(describe(failure));
    } finally {
      setLoading(false);
    }
  }

  async function installCustomNode(event: React.FormEvent) {
    event.preventDefault();
    if (!nodeId) {
      return;
    }
    const name = customNodeName.trim();
    if (!name) {
      setInstallError("节点名不能为空。");
      return;
    }
    setInstallPending(true);
    setInstallError(null);
    setNotice(null);
    try {
      const outcome = await run(comfyUIManagedCustomNodeInstallRequest(nodeId, name));
      const connected = outcome.replicas.filter((replica) => replica.connected).length;
      setNotice(`已下发安装 ${name}：${connected}/${outcome.replicas.length} 个已配置副本报告该节点已连接。安装成功后节点尚不会自动重启——需要再触发一次「重启」才能让 ComfyUI 加载新节点，请之后重新查询状态确认已安装。`);
      setCustomNodeName("");
    } catch (failure) {
      setInstallError(describe(failure));
    } finally {
      setInstallPending(false);
    }
  }

  const instance = status?.instances[0] ?? null;

  return <div className="grid gap-8">
    <section className="grid gap-4" aria-labelledby="comfyui-managed-title">
      <div>
        <h1 id="comfyui-managed-title" className="font-heading text-lg font-semibold">ComfyUI Managed</h1>
        <p className="text-sm text-muted-foreground">
          查询并操作某个节点本地已声明的那一个 Managed ComfyUI 容器（STATUS.md 的 P2 ComfyUI Managed Docker 部署）。触发只在该节点连到本次询问所到达的某个已配置 Gateway 副本时才生效；一次触发的 202 只确认已下发到隧道，不确认容器已经真的执行——请之后重新查询状态确认结果。镜像、GPU、挂载路径与自定义节点的安装源永远只存在于该节点自己的本地配置，这个页面从不能指定它们。
        </p>
      </div>
      <form onSubmit={query} className="flex flex-wrap items-end gap-3">
        <div className="grid gap-1.5">
          <label htmlFor="node-id" className="text-sm font-medium">节点 ID</label>
          <Input id="node-id" className="max-w-xs" value={nodeIdInput} onChange={(event) => setNodeIdInput(event.target.value)} placeholder="例如 mac-mini-01" aria-label="节点 ID" />
        </div>
        <SubmitButton pending={loading}>查询状态</SubmitButton>
        {nodeId ? <Button type="button" variant="outline" disabled={loading} onClick={reload}>重新查询</Button> : null}
      </form>
      <FormError message={queryError} />
      {notice ? <p role="status" className="text-sm">{notice}</p> : null}
    </section>

    {nodeId && !queryError ? <section className="grid gap-4" aria-labelledby="comfyui-managed-instance-title">
      <h2 id="comfyui-managed-instance-title" className="font-heading text-base font-semibold">
        <code className="break-all">{nodeId}</code>
      </h2>
      {instance ? <div className="grid gap-3 rounded-xl border p-4">
        <div className="flex flex-wrap items-center gap-2">
          <code className="text-sm">{instance.containerName}</code>
          <Badge variant={instance.state === "running" ? "default" : instance.state === "failed" ? "destructive" : "outline"}>
            {STATE_LABELS[instance.state] ?? instance.state}
          </Badge>
          <span className="text-xs text-muted-foreground">更新于 {formatDateTime(instance.updatedAt)}</span>
        </div>
        <div className="flex flex-wrap gap-2">
          {(["start", "stop", "restart"] as const).map((action) => (
            <Button key={action} variant="outline" disabled={confirmAction !== null} onClick={() => setConfirmAction(action)}>
              {ACTION_LABELS[action]}
            </Button>
          ))}
        </div>
        <div>
          <h3 className="text-sm font-medium">已安装自定义节点</h3>
          {instance.customNodes.length === 0 ? (
            <p className="text-sm text-muted-foreground">该实例上报未安装任何自定义节点，或从未使用本节点的安装机制装过节点。</p>
          ) : (
            <ul className="grid gap-1 text-sm">
              {instance.customNodes.map((node) => (
                <li key={node.name}>
                  <code>{node.name}</code> — <span className="text-muted-foreground">{node.version}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div> : <p className="text-sm text-muted-foreground">该节点未本地配置 Managed 实例，或尚未上报过任何容器状态。</p>}

      <form onSubmit={installCustomNode} className="grid gap-3 rounded-xl border p-4">
        <h3 className="text-sm font-medium">安装自定义节点</h3>
        <p className="text-xs text-muted-foreground">只能按名字触发，安装源（仓库地址与固定版本）必须已经写在该节点自己的 <code>-comfyui-managed-custom-nodes</code> 本地允许列表里；这里从不能指定仓库 URL。安装成功不会自动重启容器。</p>
        <div className="flex flex-wrap items-end gap-3">
          <div className="grid gap-1.5">
            <label htmlFor="custom-node-name" className="text-sm font-medium">节点名</label>
            <Input id="custom-node-name" className="max-w-xs" value={customNodeName} onChange={(event) => setCustomNodeName(event.target.value)} placeholder="例如 my-node" aria-label="自定义节点名" />
          </div>
          <SubmitButton pending={installPending}>安装</SubmitButton>
        </div>
        <FormError message={installError} />
      </form>

      <div className="grid gap-2">
        <h3 className="text-sm font-medium">已配置副本</h3>
        {status && status.replicas.length > 0 ? <ul className="grid gap-1 text-sm">
          {status.replicas.map((replica) => <li key={replica.endpoint}>
            <code>{replica.endpoint}</code> — <Badge variant={replica.connected ? "default" : "outline"}>{replica.connected ? "已连接" : "未连接"}</Badge>
            {replica.error ? <span className="text-muted-foreground"> {replica.error}</span> : null}
          </li>)}
        </ul> : <p className="text-sm text-muted-foreground">没有副本报告过该节点。</p>}
      </div>
    </section> : null}

    {queryError ? <ErrorState message={queryError} onRetry={nodeId ? reload : undefined} /> : null}

    <ConfirmDialog
      open={confirmAction !== null}
      onOpenChange={(open) => { if (!open) setConfirmAction(null); }}
      title={confirmAction ? ACTION_LABELS[confirmAction] : "生命周期动作"}
      confirmLabel="确认执行"
      destructive={confirmAction === "stop" || confirmAction === "restart"}
      description={confirmAction && nodeId ? <><span className="break-all font-mono">{nodeId}</span>：{ACTION_EFFECTS[confirmAction]}</> : ""}
      onConfirm={async () => {
        if (!confirmAction || !nodeId) return;
        setNotice(null);
        try {
          const outcome = await run(comfyUIManagedTriggerRequest(nodeId, confirmAction));
          const connected = outcome.replicas.filter((replica) => replica.connected).length;
          setNotice(`已下发${ACTION_LABELS[confirmAction]}：${connected}/${outcome.replicas.length} 个已配置副本报告该节点已连接。容器是否真的执行了这个动作，需要重新查询状态确认。`);
        } catch (failure) {
          throw new Error(`${describe(failure)} 写入结果未确认；请重新查询状态后再决定是否重试。`);
        }
      }}
    />
  </div>;
}
