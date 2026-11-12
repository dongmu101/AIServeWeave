import "server-only";

import { parseLegalEntity, type LegalEntity } from "../legal/entity.ts";

/** cached holds the resolved entity for the life of the process.
 *
 * cached 在进程生命期内保存解析后的营运方资讯。 */
let cached: LegalEntity | null = null;

/**
 * legalEntity reads the operator facts once per process. It is separate from
 * serverConfig on purpose: the public pages that need it must render without
 * the session secret, and a missing legal fact is a warning, not a failure.
 *
 * legalEntity 每个进程读取一次营运方事实。它刻意与 serverConfig 分开：需要它的公开页面
 * 必须在没有会话密钥时也能渲染，而缺少某项法律事实只是警告，不是失败。
 */
export function legalEntity(): LegalEntity {
  if (cached) return cached;
  cached = parseLegalEntity(process.env);
  if (cached.missing.length > 0) {
    console.warn(
      `[console] legal pages show placeholders; set ${cached.missing.join(", ")} before going live.`
    );
  }
  return cached;
}
