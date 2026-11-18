import { NewConversation } from "@/components/studio/new-conversation";
import { BrandMark } from "@/components/studio/brand";
import { errorMessage, greetingName } from "@/lib/studio/contract";
import { loadModels, requireSession } from "@/lib/server/studio";

/**
 * The Studio home page: a greeting, the composer, and the model picker.
 * Studio 首页：问候语、输入框与模型选择器。
 */
export default async function HomePage() {
  const session = await requireSession("/");
  const models = await loadModels(session, "/");

  return (
    <div className="flex flex-1 flex-col items-center justify-center px-4 pb-[12vh] sm:px-6">
      <div className="flex w-full max-w-3xl flex-col items-center gap-8">
        <h1 className="flex items-center gap-3 text-center font-heading text-3xl font-medium tracking-tight sm:text-4xl">
          <BrandMark className="size-9 shrink-0 sm:size-10" />
          <span>嗨，{greetingName(session.user)}</span>
        </h1>
        <NewConversation
          models={models.kind === "ok" ? models.models : []}
          modelsError={models.kind === "error" ? errorMessage(models.code) : ""}
        />
      </div>
    </div>
  );
}
