import type { ApiKeySummary, Role } from "./contract.ts";

/**
 * These are the control plane's authorization rules, restated here for one
 * purpose only: deciding which controls to draw.
 *
 * Nothing in this module makes an operation safe. The control plane checks
 * every call again — `logic.Service.CreateUser` refuses a non-owner,
 * `Actor.canManageKeys` gates minting, `RevokeAPIKey` re-checks the key's
 * creator — and a Console that skipped a check here would be answered with a
 * 403 or a 404, not obeyed. What a wrong rule here costs is a button that
 * fails when clicked, or an action a person cannot find. That is why the rules
 * live in one file with tests rather than being spelled out inside each view.
 *
 * 这些是控制面的授权规则，在此复述只为一个目的：决定画出哪些控件。
 *
 * 本模块中的任何东西都不能让一次操作变安全。控制面会重新检查每一次调用——
 * `logic.Service.CreateUser` 拒绝非 owner，`Actor.canManageKeys` 把守铸造，
 * `RevokeAPIKey` 会再次核对 key 的创建者——一个在这里跳过检查的 Console 得到的是 403
 * 或 404，而不是照办。这里的规则写错，代价是一个点下去就失败的按钮，或一个当事人找不到
 * 的操作。正因如此，这些规则集中在一个有测试的文件里，而不是散落在各个视图内部。
 */

/**
 * canCreateUser reports whether a role may add users. Only an owner may:
 * adding a user is granting access, and the control plane keeps that with the
 * owner rather than delegating it to every admin.
 *
 * canCreateUser 报告某个角色是否可以添加用户。只有 owner 可以：添加用户就是授予访问
 * 权限，控制面把这件事留给 owner，而不是下放给每个 admin。
 */
export function canCreateUser(role: string): boolean {
  return role === "owner";
}

/**
 * canCreateApiKey reports whether a role may mint keys. Owner and admin may;
 * a member may not, despite what the control plane README's summary suggests
 * — the code is `Actor.canManageKeys`, and this follows the code.
 *
 * canCreateApiKey 报告某个角色是否可以铸造 key。owner 与 admin 可以；member 不可以
 * ——尽管控制面 README 的概括另有暗示，实际代码是 `Actor.canManageKeys`，这里以代码为准。
 */
export function canCreateApiKey(role: string): boolean {
  return role === "owner" || role === "admin";
}

/**
 * canRevokeApiKey reports whether this user may revoke this key.
 *
 * A member sees the revoke control only on a key it created. The control plane
 * answers 404 rather than 403 for the rest, so that a member cannot tell "it
 * exists but is not yours" from "no such key" and enumerate the tenant's keys
 * that way — which is also why the Console must not offer the control and let
 * the failure explain itself.
 *
 * canRevokeApiKey 报告该用户是否可以吊销这个 key。
 *
 * member 只在自己创建的 key 上看到吊销控件。控制面对其余情况答 404 而不是 403，好让
 * member 无法分辨「存在但不属于你」与「没有这个 key」并据此枚举租户的 key —— 这也正是
 * Console 不该把控件摆出来、再让失败去解释自己的原因。
 */
export function canRevokeApiKey(
  role: string,
  userId: string,
  key: Pick<ApiKeySummary, "createdBy">
): boolean {
  if (role === "owner" || role === "admin") {
    return true;
  }
  return role === "member" && key.createdBy === userId;
}

/**
 * canEditTenantLimits reports whether a role may write the tenant's quota.
 * The editing view itself waits on a read endpoint — see STATUS B01.
 *
 * canEditTenantLimits 报告某个角色是否可以写租户配额。编辑视图本身还在等一个读取端点
 * —— 见 STATUS 的 B01。
 */
export function canEditTenantLimits(role: string): boolean {
  return role === "owner" || role === "admin";
}

/**
 * assignableRoles is what a creation form may offer. It is the full set: an
 * owner creating a user may pick any role, including another owner.
 *
 * assignableRoles 是创建表单可以提供的选项。它是全集：owner 创建用户时可以选择任何
 * 角色，包括另一个 owner。
 */
export const assignableRoles: readonly Role[] = ["owner", "admin", "member"];
