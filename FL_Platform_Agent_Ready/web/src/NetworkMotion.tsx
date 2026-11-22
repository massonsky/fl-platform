import {useEffect,useLayoutEffect,useRef,useState} from 'react';

const routes=[
  'M430 350 C560 350 500 420 835 450',
  'M1260 350 C1120 350 1200 430 835 450',
  'M390 590 C540 590 500 470 835 450',
  'M1300 600 C1140 600 1180 500 835 450',
  'M840 740 C740 640 900 580 835 450',
];
const phases=['Локальное обучение','Передача обновлений','Объединение модели'];
export function NetworkMotion({paused}:{paused:boolean}){
  const svg=useRef<SVGSVGElement>(null);
  const [phase,setPhase]=useState(0);
  const packets=useRef<Animation[]>([]);
  useLayoutEffect(()=>{
    const groups=svg.current?.querySelectorAll<SVGGElement>('.packet-route');
    groups?.forEach((group,index)=>{
      const path=group.querySelector('path')!;
      const length=path.getTotalLength();
      const frames=Array.from({length:61},(_,step)=>{
        const point=path.getPointAtLength(length*step/60);
        return {transform:`translate(${point.x}px,${point.y}px)`,offset:step/60};
      });
      group.querySelectorAll('circle').forEach((circle,tail)=>{
        packets.current.push(circle.animate(frames,{duration:3600,iterations:Infinity,delay:-(index*220+tail*90),easing:'linear'}));
      });
    });
    return()=>{packets.current.forEach(animation=>animation.cancel());packets.current=[]};
  },[]);
  useEffect(()=>{
    if(paused){packets.current.forEach(animation=>animation.pause());return}
    packets.current.forEach(animation=>animation.play());
    const timer=setInterval(()=>setPhase(value=>(value+1)%phases.length),2400);
    return()=>clearInterval(timer);
  },[paused]);
  return <>
    <svg ref={svg} className="network-motion" viewBox="0 0 1672 941" aria-hidden="true">
      {routes.map(path=><g key={path} className="packet-route">
        <path d={path} fill="none" stroke="#9ad9ff" strokeWidth="1.5" opacity=".2"/>
        {[0,1,2].map(tail=><circle key={tail} r={tail===0?6:3} fill={tail===0?'#effbff':'#8dd5ff'} opacity={1-tail*.25}>

        </circle>)}
      </g>)}
      <ellipse className="model-wave wave-one" cx="835" cy="470" rx="155" ry="52" fill="none" stroke="#9ad9ff" strokeWidth="3"/>
      <ellipse className="model-wave wave-two" cx="835" cy="470" rx="155" ry="52" fill="none" stroke="#9ad9ff" strokeWidth="2"/>
    </svg>
    <span className="model-label">Общая модель<small key={phase}>{paused?'Цикл федеративного обучения':phases[phase]}</small></span>
  </>;
}
