import assert from "node:assert/strict";
import test from "node:test";

import {
  canCreateApiKey,
  canCreateUser,
  canEditTenantLimits,
  canRevokeApiKey,
} from "./permissions.ts";

test("the role matrix matches what the control plane enforces", () => {
  const cases: {
    role: string;
    createUser: boolean;
    createKey: boolean;
    editLimits: boolean;
  }[] = [
    { role: "owner", createUser: true, createKey: true, editLimits: true },
    { role: "admin", createUser: false, createKey: true, editLimits: true },
    { role: "member", createUser: false, createKey: false, editLimits: false },
    // A role this build does not know grants nothing. Failing closed keeps an
    // added role from silently inheriting an owner's controls.
    //
    // 本次构建不认识的角色什么都不授予。失败时保持关闭，可以避免新增的角色悄悄继承
    // owner 的控件。
    { role: "auditor", createUser: false, createKey: false, editLimits: false },
    { role: "", createUser: false, createKey: false, editLimits: false },
  ];

  for (const item of cases) {
    assert.equal(canCreateUser(item.role), item.createUser, `${item.role}: create user`);
    assert.equal(canCreateApiKey(item.role), item.createKey, `${item.role}: create key`);
    assert.equal(
      canEditTenantLimits(item.role),
      item.editLimits,
      `${item.role}: edit limits`
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
