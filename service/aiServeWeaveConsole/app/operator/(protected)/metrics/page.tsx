import { MetricsView } from "./metrics-view";

/** MetricsPage displays fleet-wide historical request/latency/token/capacity
 * curves for platform operators (P08, Console C27).
 *
 * MetricsPage 向平台运维展示机群级别的历史请求/延迟/Token/容量曲线(P08，
 * Console C27)。 */
export default function MetricsPage() {
  return <MetricsView />;
}
