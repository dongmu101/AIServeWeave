import { serverConfig } from "@/lib/server/config";
import type { SessionUser } from "@/lib/console/session-payload";

/**
 * This module decides who may read the fleet inventory, and it is the weakest
 * link in this Console's authorization — which is why it says so here rather
 * than looking like an ordinary check.
 *
 * The control plane cannot make this decision. Its roles are tenant-scoped and
 * a node has no tenant, so the fleet endpoints are guarded by a shared secret
 * with no user behind it. Somebody still has to decide which humans may use
 * that secret, and with no platform-operator identity in the system, the
 * Console is where that decision lands.
 *
 * Two things are required together: this deployment must hold the operator
 * token, and the signed-in person must be named in the operator allowlist.
 * Neither alone is enough. An operations Console should be deployed separately
 * from the tenant-facing one, so that a mistake in this file cannot expose the
 * fleet to a tenant's users — the tenant-facing deployment simply holds no
 * token to forward.
 *
 * The real fix is a platform-operator identity in the control plane, recorded
 * in STATUS as the follow-up to this phase.
 *
 * 本模块决定谁可以读取机群清单，而它是本 Console 授权链上最薄弱的一环——正因如此，
 * 这一点被写在这里，而不是让它看起来像一次普通的检查。
 *
 * 控制面做不了这个决定。它的角色都是租户内的，而节点没有租户，因此机群端点由一个背后
 * 没有用户的共享密钥守卫。但仍然得有人决定哪些人可以使用那个密钥，而系统中并不存在
 * 「平台运维」这种身份，于是这个决定落到了 Console 头上。
 *
 * 两件事必须同时成立：本部署必须持有运维 token，且登录者必须出现在运维名单中。任何
 * 单独一条都不够。运维控制台应当与面向租户的控制台分开部署，这样本文件里的失误就不会
 * 把机群暴露给租户的用户——面向租户的那个部署根本没有可转发的 token。
 *
 * 真正的解法是在控制面里引入平台运维身份，这一点作为本阶段的后续记在 STATUS 里。
 */

/**
 * OperatorAccess is what this deployment can do about the fleet inventory.
 *
 * OperatorAccess 是本部署在机群清单一事上能做什么。
 */
export interface OperatorAccess {
  /** configured is true when this deployment holds an operator token and
   * therefore is an operations console at all.
   *
   * configured 在本部署持有运维 token、因而确实是一个运维控制台时为 true。 */
  configured: boolean;
  /** allowed is true when this deployment is configured and the signed-in
   * person is on its operator list.
   *
   * allowed 在本部署已配置、且登录者在其运维名单上时为 true。 */
  allowed: boolean;
}

/**
 * operatorAccess resolves what the signed-in person may do.
 *
 * A deployment that holds the token but names no operators grants nothing:
 * an empty allowlist is read as "nobody", never as "everybody". The opposite
 * reading is how a feature flag turns into an open door.
 *
 * operatorAccess 求解登录者可以做什么。
 *
 * 一个持有 token 却没有点名任何运维的部署什么都不授予：空名单被读作「没有人」，绝不
 * 读作「所有人」。反过来读，正是一个功能开关变成一扇敞开的门的方式。
 */
export function operatorAccess(user: SessionUser | null): OperatorAccess {
  const config = serverConfig();
  const configured = config.operatorToken !== "";
  if (!configured || !user) {
    return { configured, allowed: false };
  }
  return {
    configured,
    allowed: config.operatorEmails.includes(user.email.trim().toLowerCase()),
  };
}
