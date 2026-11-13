/**
 * CHART_COLORS is the series order every chart uses: the brand gold first, then
 * hues chosen to stay apart from it and from each other. ECharts cannot read
 * CSS variables, so the values are plain hex. The order is fixed — it passed the
 * lightness, chroma, colour-vision and contrast checks against a white card as
 * adjacent pairs, and reordering it breaks that.
 *
 * CHART_COLORS 是所有图表共用的系列顺序：品牌金色在前，其后的色相与它、也彼此拉开距离。
 * ECharts 读不了 CSS 变量，所以这里是纯十六进制值。顺序是固定的——它是按相邻配对、以白色
 * 卡片为底通过明度、彩度、色觉与对比度检查的，调换顺序会破坏这一点。
 */
export const CHART_COLORS = ["#B8860B", "#3F6FB5", "#C2532D", "#0D9488", "#8A4FA8", "#5E8C2A"];
