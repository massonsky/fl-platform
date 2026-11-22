import {Icon} from './Icon';
import {useEffect,useRef,useState} from 'react';
import type {Metric} from './api';
export function Chart({series, metric,totalRounds=1}: {totalRounds?:number;series: {name: string; points: Metric[]}[]; metric: string}) {
  const host=useRef<HTMLDivElement>(null);
  const [width,setWidth]=useState(800);
  useEffect(()=>{const observer=new ResizeObserver(([entry])=>setWidth(Math.max(240,Math.min(800,entry.contentRect.width))));if(host.current)observer.observe(host.current);return()=>observer.disconnect()});
  const plotWidth=width-72;
  const lines = series.map(s => ({...s, points: s.points.filter((p): p is Metric & {round_no: number} => p.round_no !== null && p.name === metric && p.split === 'validation_global').sort((a,b) => a.round_no-b.round_no)}));
  const count = Math.max(1,totalRounds, ...lines.flatMap(s => s.points.map(p => p.round_no)));
  const colors = ['#a4d6fa', '#e6b879'];
  if (!lines.some(s => s.points.length)) return <div ref={host} className="empty chart-empty"><Icon name="experiments"/><div><strong>Для графика пока нет данных</strong><p>После первого завершённого раунда здесь появятся измерения на валидации.</p></div></div>;
  return <div ref={host} className="chart"><div className="legend">{lines.map((s,i) => <span key={s.name}><i style={{background: colors[i]}}/>{s.name}</span>)}</div>
    <svg viewBox={`0 0 ${width} 270`} role="img" aria-label={`График ${metric} по раундам`}>
      {[0, .25, .5, .75, 1].map(v => <g key={v}><line x1="48" x2={width-24} y1={230-v*195} y2={230-v*195} stroke="#293847"/><text x="36" y={234-v*195} textAnchor="end">{v*100}%</text></g>)}
      {[...new Set([1, Math.max(1,Math.round(count/2)), count])].map(v => <text key={v} x={48+(v-1)/Math.max(1,count-1)*plotWidth} y="257" textAnchor="middle">{v}</text>)}
      {lines.map((s,i) => <g key={s.name}><polyline points={s.points.map(p => `${48+(p.round_no-1)/Math.max(1,count-1)*plotWidth},${230-p.value*195}`).join(' ')} fill="none" stroke={colors[i]} strokeWidth="3" strokeLinejoin="round"/>
        {s.points.map(p => <circle key={p.id} cx={48+(p.round_no-1)/Math.max(1,count-1)*plotWidth} cy={230-p.value*195} r="3" fill={colors[i]}><title>Раунд {p.round_no}: {(p.value*100).toFixed(2)}%</title></circle>)}</g>)}
    </svg><p className="caption">Раунды · фиксированная валидация, 250 объектов</p></div>;
}
