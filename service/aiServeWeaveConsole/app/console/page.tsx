import { redirect } from "next/navigation";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ROLE_LABELS, type Role } from "@/lib/console/contract";
import { readSession } from "@/lib/server/session";

/**
 * The overview states what this session is, and nothing it cannot verify.
 *
 * There is no tenant detail endpoint yet, so the tenant appears as its id and
 * not as a name; there is no metrics API, so there are no numbers. A console
 * that filled these in with plausible values would be inventing the thing its
 * users came here to check.
 *
 * 概览页陈述当前会话是什么，不陈述它无法核实的任何东西。
 *
 * 目前没有租户详情端点，因此租户以 id 而非名称呈现；没有指标 API，因此没有任何数字。
 * 一个用貌似合理的值把这些位置填满的控制台，等于在编造用户正是来这里核对的那件事。
 */
export default async function OverviewPage() {
  const session = await readSession();
  if (!session) {
    redirect("/login");
  }

  const { user } = session;
  const facts: { label: string; value: string }[] = [
    { label: "登录邮箱", value: user.email },
    { label: "姓名", value: user.name || "未填写" },
    { label: "角色", value: ROLE_LABELS[user.role as Role] ?? user.role },
    { label: "租户 ID", value: user.tenantId },
    { label: "用户 ID", value: user.id },
    {
      label: "会话到期",
      value: new Date(session.expiresAt).toLocaleString("zh-CN"),
    },
  ];

  return (
    <div className="grid gap-6">
      <div>
        <h1 className="font-heading text-lg font-semibold">概览</h1>
        <p className="text-sm text-muted-foreground">
          当前会话信息。用户、API Key、管理审计、租户配额、工作流菜单与当前运行均已
          可查；指标、请求检索与告警等待控制面补齐存储与接口后再开放。
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>当前会话</CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
            {facts.map((fact) => (
              <div key={fact.label} className="grid gap-1">
                <dt className="text-xs text-muted-foreground">{fact.label}</dt>
                <dd className="font-mono text-sm break-all">{fact.value}</dd>
              </div>
            ))}
          </dl>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>当前边界</CardTitle>
        </CardHeader>
        <CardContent>
          <ul className="grid gap-2 text-sm text-muted-foreground">
            <li>
              控制台只调用控制面的 Admin
              API，不直连数据面网关、Registry、Agent 或推理后端。
            </li>
            <li>
              退出只清理本控制台的会话；控制面签发的令牌在到期前依然有效，当前没有吊销
              接口，也没有自动续期。
            </li>
            <li>
              页面上的角色只用于决定显示哪些入口；每一次操作是否被允许，仍由控制面判定。
            </li>
            <li>
              用户、API Key 与审计三张列表均由控制面分页与筛选，游标基于创建时间倒序；
              接口不提供总数，因此页面只显示页码而不显示总页数。
            </li>
            <li>
              「运行」页是实时视图而不是历史：网关的 Job 表在进程内存里、有条数上限、
              且每个副本各自持有，持久化的 Job 历史尚未实现。
            </li>
          </ul>
        </CardContent>
      </Card>
    </div>
  );
}
