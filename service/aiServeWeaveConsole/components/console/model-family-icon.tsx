import { Boxes } from "lucide-react";

import type { ModelFamily } from "@/lib/console/model-family";
import { cn } from "@/lib/utils";

/** ModelFamilyIcon is a small brand-coloured tile with the family's mark, or a
 * neutral tile for "other". It is decorative: the family name is beside it.
 *
 * ModelFamilyIcon 是带系列字符的品牌色小方块；「其他」用中性方块。它只起装饰作用，
 * 系列名称就在旁边。 */
export function ModelFamilyIcon({ family, className }: { family: ModelFamily | null; className?: string }) {
  const base = "inline-flex size-7 shrink-0 items-center justify-center rounded-lg text-xs font-semibold shadow-sm ring-1 ring-black/5 dark:ring-white/10";
  if (!family) {
    return (
      <span aria-hidden="true" className={cn(base, "bg-muted text-muted-foreground", className)}>
        <Boxes className="size-4" />
      </span>
    );
  }
  return (
    <span aria-hidden="true" className={cn(base, "text-white", className)} style={{ backgroundColor: family.color }}>
      {family.mark}
    </span>
  );
}
