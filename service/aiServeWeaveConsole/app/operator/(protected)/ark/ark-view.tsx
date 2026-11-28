"use client";

import { ArkCredentialPanel } from "@/components/console/ark-credential-panel";
import { ArkTaskList } from "@/components/console/ark-task-list";
import { Message } from "@/components/i18n/message";

/**
 * ArkView is the platform's Volcengine Ark key — base URL, key and the models
 * it opens to every tenant without its own — and, once a key is saved, the
 * video tasks under it. Ark does not tell tenants apart, so the list covers
 * all of them.
 *
 * ArkView 是平台的火山方舟 Key——base URL、Key，以及向所有没有自己 Key 的租户开放的模型——
 * 保存 Key 后还有其下的视频任务。方舟不区分租户，因此列表涵盖所有租户。
 */
export function ArkView() {
  return <div className="grid max-w-5xl gap-6">
    <div>
      <h1 className="font-heading text-xl font-semibold tracking-tight"><Message id="operator.arkNav" /></h1>
      <p className="mt-2 text-sm text-muted-foreground"><Message id="operator.arkIntro" /></p>
    </div>
    <section className="grid gap-3 rounded-xl border bg-card p-4">
      <ArkCredentialPanel surface="operator">
        {(state) => <div className="mt-4 grid gap-3 border-t pt-4">
          <div>
            <h2 className="font-heading text-base font-semibold"><Message id="operator.arkTasksTitle" /></h2>
            <p className="mt-1 text-sm text-muted-foreground"><Message id="operator.arkTasksIntro" /></p>
          </div>
          {state.credential ? <ArkTaskList surface="operator" /> : <p className="text-sm text-muted-foreground"><Message id="operator.arkNoKeyForTasks" /></p>}
        </div>}
      </ArkCredentialPanel>
    </section>
  </div>;
}
