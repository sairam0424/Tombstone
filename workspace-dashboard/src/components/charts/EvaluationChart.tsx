// workspace-dashboard/src/components/charts/EvaluationChart.tsx
import ReactECharts from 'echarts-for-react';
import type { EChartsOption } from 'echarts';

export interface TimeSeriesPoint {
  timestamp: number; // Unix ms
  value: number;
}

interface Props {
  data: TimeSeriesPoint[];
  title?: string;
  color?: string;
  height?: number;
  yLabel?: string;
}

export function EvaluationChart({
  data,
  title,
  color = '#38e1ff',
  height = 200,
  yLabel,
}: Props) {
  const option: EChartsOption = {
    backgroundColor: 'transparent',
    animation: true,
    title: title ? {
      text: title,
      textStyle: { color: '#e9ecf5', fontSize: 13, fontWeight: 500 },
      top: 4, left: 8,
    } : undefined,
    tooltip: {
      trigger: 'axis',
      backgroundColor: '#141826',
      borderColor: '#2c3346',
      textStyle: { color: '#e9ecf5', fontSize: 12 },
      axisPointer: { type: 'cross', lineStyle: { color: '#38e1ff', opacity: 0.5 } },
    },
    grid: { top: title ? 40 : 16, bottom: 32, left: 48, right: 16, containLabel: false },
    xAxis: {
      type: 'time',    // ECharts v6.1 fixed critical bugs in time axis
      axisLine: { lineStyle: { color: '#1f2433' } },
      axisLabel: { color: '#747e99', fontSize: 11 },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value',
      name: yLabel,
      nameTextStyle: { color: '#747e99', fontSize: 11 },
      axisLabel: { color: '#747e99', fontSize: 11 },
      splitLine: { lineStyle: { color: '#1f2433', type: 'dashed' } },
      axisLine: { show: false },
    },
    series: [{
      type: 'line',
      data: data.map(p => [p.timestamp, p.value]),
      smooth: true,
      symbol: 'none',
      lineStyle: { color, width: 2 },
      areaStyle: {
        color: {
          type: 'linear',
          x: 0, y: 0, x2: 0, y2: 1,
          colorStops: [
            { offset: 0, color: `${color}30` },
            { offset: 1, color: `${color}00` },
          ],
        },
      },
    }],
  };

  return (
    <ReactECharts
      option={option}
      style={{ height, width: '100%' }}
      opts={{ renderer: 'canvas', devicePixelRatio: window.devicePixelRatio ?? 1 }}
      notMerge={true}
      lazyUpdate={false}
    />
  );
}
