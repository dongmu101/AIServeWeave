/** nodePathSegment accepts labels representable by the control plane's single-segment node route.
 * nodePathSegment 接受控制面单路径段节点路由可以表示的标签。 */
export function nodePathSegment(value: string): boolean {
  return value !== "" && value !== "." && value !== ".." && !/[\/\\\p{Cc}]/u.test(value);
}
