import assert from "node:assert/strict";
import test from "node:test";

import {
  canCreateApiKey,
  canCreateUser,
  canEditTenantLimits,
  canManageGatewayKey,
  canRevokeApiKey,
} from "./permissions.ts";

test("the role matrix matches what the control plane enforces", () => {
  const cases: {
    role: string;
    createUser: boolean;
    createKey: boolean;
    editLimits: boolean;
    manageGatewayKey: boolean;
  }[] = [
    { role: "owner", createUser: true, createKey: true, editLimits: true, manageGatewayKey: true },
    { role: "admin", createUser: false, createKey: true, editLimits: true, manageGatewayKey: true },
    { role: "member", createUser: false, createKey: false, editLimits: false, manageGatewayKey: false },
    // A role this build does not know grants nothing. Failing closed keeps an
    // added role from silently inheriting an owner's controls.
    //
    // 本次构建不认识的角色什么都不授予。失败时保持关闭，可以避免新增的角色悄悄继承
    // owner 的控件。
    { role: "auditor", createUser: false, createKey: false, editLimits: false, manageGatewayKey: false },
    { role: "", createUser: false, createKey: false, editLimits: false, manageGatewayKey: false },
  ];

  for (const item of cases) {
    assert.equal(canCreateUser(item.role), item.createUser, `${item.role}: create user`);
    assert.equal(canCreateApiKey(item.role), item.createKey, `${item.role}: create key`);
    assert.equal(
      canEditTenantLimits(item.role),
      item.editLimits,
      `${item.role}: edit limits`
    );
    // canManageGatewayKey is enforced server-side, not merely restated for the
    // UI — see its own doc comment — so this assertion is the one place that
    // check's logic itself is exercised, not just its rendering effect.
    //
    // canManageGatewayKey 是在服务端强制执行的，而不只是为界面复述一遍——见它自己的
    // 文档注释——因此这条断言是唯一真正检验该检查本身逻辑的地方，而不只是它渲染出的
    // 效果。
    assert.equal(
      canManageGatewayKey(item.role),
      item.manageGatewayKey,
      `${item.role}: manage gateway key`
    );
  }
});

test("a member sees revoke only on the keys it created", () => {
  const cases: {
    name: string;
    role: string;
    userId: string;
    createdBy: string;
    want: boolean;
  }[] = [
    { name: "owner on another's key", role: "owner", userId: "u-1", createdBy: "u-2", want: true },
    { name: "admin on another's key", role: "admin", userId: "u-1", createdBy: "u-2", want: true },
    { name: "member on its own key", role: "member", userId: "u-1", createdBy: "u-1", want: true },
    { name: "member on another's key", role: "member", userId: "u-1", createdBy: "u-2", want: false },
    { name: "unknown role on its own key", role: "auditor", userId: "u-1", createdBy: "u-1", want: false },
  ];

  for (const item of cases) {
    assert.equal(
      canRevokeApiKey(item.role, item.userId, { createdBy: item.createdBy }),
      item.want,
      item.name
    );
  }
});
