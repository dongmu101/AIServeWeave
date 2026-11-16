import { cn } from "cn";

/**
 * BrandMark is Studio's own mark: two woven strands, in the brand colour.
 * BrandMark 是 Studio 自己的标志：两股交织的线，使用品牌色。
 */
export function BrandMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" aria-hidden="true" className={cn("text-brand", className)}>
      <path d="M4 22c6-12 10-12 12 0s6 12 12 0" fill="none" stroke="currentColor" strokeWidth="3" strokeLinecap="round" />
      <path d="M4 10c6 12 10 12 12 0s6-12 12 0" fill="none" stroke="currentColor" strokeWidth="3" strokeLinecap="round" opacity="0.55" />
    </svg>
  );
}
