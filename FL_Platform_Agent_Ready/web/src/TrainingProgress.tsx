import type {Run,Round,Metric} from './api';
import {useEffect,useRef,useState} from 'react';
import './training.css';

const phases=[
 {label:'Подготовка',description:'Выбор участников и выдача заданий'},
 {label:'Локальное обучение',description:'Данные остаются у участников'},
 {label:'Объединение',description:'Обновления формируют общую модель'},
 {label:'Оценка',description:'Проверка модели на валидации'},
];
const phaseIndex:Record<string,number>={planned:0,dispatching:0,collecting:1,aggregating:2,evaluating:3,completed:4};
const explanations:Record<string,string>={queued:'Запуск в очереди. Ожидаем начала первого раунда.',running:'Координатор готовит следующий раунд.',completed:'Обучение завершено. Итоговые тестовые метрики показаны ниже.',cancelled:'Запуск остановлен. Сохранённые результаты доступны для просмотра.',failed:'Обучение завершилось с ошибкой. Проверьте причину остановки.'};
export function TrainingProgress({run,rounds,metrics,loaded,stale}:{run:Run;rounds:Round[];metrics:Metric[];loaded:boolean;stale:boolean}){
 const host=useRef<HTMLElement>(null),[visible,setVisible]=useState(false),[foreground,setForeground]=useState(!document.hidden);
 useEffect(()=>{const observer=new IntersectionObserver(([entry])=>setVisible(entry.isIntersecting));if(host.current)observer.observe(host.current);const changed=()=>setForeground(!document.hidden);document.addEventListener('visibilitychange',changed);return()=>{observer.disconnect();document.removeEventListener('visibilitychange',changed)}},[]);
 const latest=rounds.reduce<Round|undefined>((a,b)=>!a||b.round_no>a.round_no?b:a,undefined);
 const terminal=['completed','failed','cancelled'].includes(run.status);
 const phase=latest?phaseIndex[latest.status]??-1:-1;
 const active=loaded&&!stale&&!terminal&&run.status==='running'&&phase>=0&&phase<4;
 const values=metrics.filter(m=>m.split==='validation_global'&&m.name==='accuracy'&&m.round_no!==null&&Number.isFinite(m.value)).sort((a,b)=>a.round_no!-b.round_no!);
 const current=values.at(-1),previous=values.at(-2),delta=current&&previous?(current.value-previous.value)*100:null;
 const title=!loaded?'Загружаем ход обучения':stale?'Обновление данных приостановлено':terminal?explanations[run.status]:active?phases[phase].label:explanations[run.status]||'Ожидаем состояние запуска';
 const selected=latest?.selected_count??0,accepted=latest?.accepted_count??0;
 return <section ref={host} className="panel training" aria-label="Ход обучения" data-active={active} data-motion={visible&&foreground}>
  <div className="training-heading"><div><h3>Ход обучения</h3><p className="training-state" role="status">{title}</p></div><div className="training-round">{loaded&&latest?<>Раунд <strong>{latest.round_no}</strong> / {run.total_rounds}</>:'Ожидаем раунд'}</div></div>
  <ol className="training-phases" aria-label="Этапы последнего раунда">{phases.map((p,i)=>{
   const done=loaded&&phase>i,current=active&&phase===i;
   return <li key={p.label} className={`${done?'is-done':''} ${current?'is-current':''}`} aria-current={current?'step':undefined}>
    <div className="phase-marker" aria-hidden="true">{done?<svg viewBox="0 0 24 24"><path d="m5 12 4 4 10-10"/></svg>:i+1}</div><strong>{p.label}</strong><p>{p.description}</p><small>{done?'Пройдено':current?'Выполняется':terminal?'Не выполняется':'Ожидание'}</small>
   </li>;
  })}</ol>
  <div className="training-readout">
   <div className="training-updates"><div><h4>Обновления участников</h4><strong>{loaded&&latest?`${accepted} / ${selected}`:'—'}</strong></div>
    <div className="training-meter" role="progressbar" aria-label="Принятые обновления" aria-valuemin={0} aria-valuemax={Math.max(1,selected)} aria-valuenow={accepted}><span style={{transform:`scaleX(${selected?Math.min(1,accepted/selected):0})`}}/></div>
    <p>{!loaded?'Загружаем данные…':!latest?'Участники ещё не выбраны.':selected===0?'Состав участников ещё формируется.':accepted===selected?'Все выбранные обновления приняты.':`${Math.max(0,selected-accepted)} ещё не принято${terminal?' на момент остановки.':'.'}`}</p>
    <small>API показывает приём обновлений, а не прогресс эпох каждого агента.</small>
   </div>
   <div><h4>Accuracy на валидации</h4><strong className="training-value">{current?`${(current.value*100).toFixed(2)}%`:'—'}</strong><p>{current?`Измерено после раунда ${current.round_no}`:'Появится после оценки первого раунда.'}</p><small>{delta===null?'Для изменения нужны два измерения.':`${delta>0?'+':''}${delta.toFixed(2)} п. п. относительно раунда ${previous!.round_no}`}</small></div>
   <div><h4>Общая модель</h4><strong className="training-value">{run.latest_model_version!==null?`v${run.latest_model_version}`:'—'}</strong><p>Завершено {run.completed_rounds} из {run.total_rounds} раундов</p><small>{stale?'Последний доступный снимок. Ожидаем восстановления связи.':'Состояние запрашивается каждые 3 секунды.'}</small></div>
  </div>
 </section>;
}
