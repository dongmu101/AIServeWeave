import { DocView } from "@/app/console/docs/[slug]/doc-view";

/** DocPage renders one endpoint's reference page. / DocPage 渲染一个端点的参考页面。 */
export default async function DocPage({ params }: { params: Promise<{ slug: string }> }) {
  const { slug } = await params;
  return <DocView slug={slug} />;
}
