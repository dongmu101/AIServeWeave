import { redirect } from "next/navigation";

import { DEFAULT_LANDING } from "@/lib/console/internal-path";
import { readSession } from "@/lib/server/session";

/**
 * The root has no content of its own: the Console is either signed in, and
 * belongs on the overview, or it is not, and belongs on sign-in.
 *
 * 根路径本身没有内容：Console 要么已登录、该去概览页，要么未登录、该去登录页。
 */
export default async function Home() {
  const session = await readSession();
  redirect(session ? DEFAULT_LANDING : "/login");
}
