import { WaypointsIcon } from "lucide-react";
import { cn } from "cn";

/** BrandMark is the product glyph both shells lead with. It is decorative: the
 * product name beside it carries the accessible name.
 *
 * BrandMark 是两个外壳开头的产品图形。它只是装饰：无障碍名称由旁边的产品名称承担。 */
export function BrandMark({ className }: { className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={cn("flex size-7 shrink-0 items-center justify-center rounded-lg bg-brand text-brand-foreground", className)}
    >
      <WaypointsIcon className="size-4" />
    </span>
  );
}
