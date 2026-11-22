import {TrainingProgress} from './TrainingProgress';
import {usePagination} from './Pagination';
import {Icon} from './Icon';
import {useEffect,useState} from 'react';
import {Chart} from './Chart';
import {useRequestScope} from './project-access-api';
import {legacyLabError} from './legacy-lab-api';
import type {LegacyAdmissionControl} from './useLegacyAdmission';
import {ApiError,list,request,download,statuses,type Run,type Metric,type Round,type Event as RunEvent,type Experiment} from './api';
export function RunDetails({run,runs,refresh,admission,readOnly=false}: {admission:LegacyAdmissionControl;readOnly?:boolean;run: Run;runs: Run[];refresh: (id?: string)=>void}) {
  const begin=useRequestScope(`run-action:${run.id}`);
  const [loaded,setLoaded]=useState(false),[stale,setStale]=useState(false);
  const [metrics,setMetrics]=useState<Metric[]>([]),[rounds,setRounds]=useState<Round[]>([]),[events,setEvents]=useState<RunEvent[]>([]);
  const [experiment,setExperiment]=useState<Experiment|null>(null),[comparison,setComparison]=useState(''),[other,setOther]=useState<Metric[]>([]);
  const [otherExperiment,setOtherExperiment]=useState<Experiment|null>(null),[metric,setMetric]=useState('accuracy'),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  const [unknownRepeat,setUnknownRepeat]=useState<Set<string>|null>(null);
  const [repeatCandidates,setRepeatCandidates]=useState<Run[]>([]);
  useEffect(()=>{let alive=true;let timer:ReturnType<typeof setTimeout>;setLoaded(false);setStale(false);setMetrics([]);setRounds([]);setEvents([]);setExperiment(null);setComparison('');
    async function load(){try{const [m,r,e,x]=await Promise.all([list<Metric>(`/runs/${run.id}/metrics`),list<Round>(`/runs/${run.id}/rounds`),list<RunEvent>(`/runs/${run.id}/events`),request<Experiment>(`/experiments/${run.experiment_id}`)]);if(alive){setMetrics(m);setRounds(r);setEvents(e);setExperiment(x);setLoaded(true);setStale(false);setError('')}}catch(e){if(alive){setStale(true);setError((e as Error).message)}}finally{if(alive)timer=setTimeout(load,3000)}}
    void load();return()=>{alive=false;clearTimeout(timer)};
  },[run.id,run.experiment_id]);
  useEffect(()=>{let alive=true;setOther([]);setOtherExperiment(null);const selected=runs.find(r=>r.id===comparison);if(selected)Promise.all([list<Metric>(`/runs/${comparison}/metrics`),request<Experiment>(`/experiments/${selected.experiment_id}`)]).then(([m,e])=>{if(alive){setOther(m);setOtherExperiment(e)}}).catch(e=>setError(e.message));return()=>{alive=false}},[comparison,runs]);
  async function action(kind: 'cancel'|'repeat'|'export') {
    if(busy||(kind==='repeat'&&unknownRepeat))return;const current=begin('mutation');let queueAttempted=false;setBusy(true);setError('');
    try{
      if(kind==='export')await download(run.id);
      else if(kind==='cancel'){await request(`/runs/${run.id}/cancel`,'POST');if(current())refresh()}
      else{if(!await admission.requireCurrent()||!current())return;queueAttempted=true;const created=await request<Run>(`/experiments/${run.experiment_id}/runs`,'POST');if(current())refresh(created.id)}
    }catch(cause){if(current()){
      if(kind==='repeat')admission.invalidate(cause);
      if(queueAttempted&&!(cause instanceof ApiError&&cause.code==='LEGACY_LAB_NOT_ADMITTED')){
        setUnknownRepeat(new Set(runs.map(item=>item.id)));setError('Результат повторного запуска не подтверждён. Проверьте новые запуски этого эксперимента. Повторный запрос не отправляется.');
      }else setError(legacyLabError(cause));
    }}
    finally{if(current())setBusy(false)}
  }
  async function inspectRepeat(){
    if(!unknownRepeat||busy)return;const current=begin('inspection');setBusy(true);
    try{
      const items=await list<Run>(`/projects/${run.project_id}/runs`);if(!current())return;
      const found=items.filter(item=>item.experiment_id===run.experiment_id&&!unknownRepeat.has(item.id));setRepeatCandidates(found);
      setError(found.length?'':'Нового подтверждённого запуска этого эксперимента пока нет. Можно проверить позже. Новый запрос не отправлен.');
    }catch(cause){if(current())setError(legacyLabError(cause))}finally{if(current())setBusy(false)}
  }
  const roundPage=usePagination(rounds,run.id,6);
  const eventPage=usePagination([...events].reverse(),run.id,6);
  const final=metrics.filter(m=>m.split==='test_global'); const accuracy=final.find(m=>m.name==='accuracy');const f1=final.find(m=>m.name==='macro_f1');
  const paired=experiment&&otherExperiment&&(['partition_manifest_hash','initial_weights_hash','local_epochs','batch_size','learning_rate','rounds'] as const).every(k=>experiment.config[k]===otherExperiment.config[k]);
  const label=experiment?`${experiment.config.algorithm==='fedavg'?'FedAvg':'FedProx'} · ${experiment.config.partition} · seed ${experiment.config.seed}`:run.id.slice(0,8);
  return <><section className="detail-title"><div><h2>{experiment?.name||'Загрузка…'}</h2><p className="run-id">ID {run.id.slice(0,8)}</p><p className="muted">{label}</p></div><div className="actions"><button onClick={()=>action('repeat')} disabled={busy||!admission.canCreate||!!unknownRepeat}>Повторить</button><button onClick={()=>action('export')} disabled={busy||run.status!=='completed'}>Экспорт<Icon name="download"/></button>{['queued','running'].includes(run.status)&&<button className="danger" onClick={()=>action('cancel')} disabled={busy||readOnly}>Остановить</button>}</div></section>
    {error&&<div role="alert" className="error">{error}</div>}{unknownRepeat&&<aside className="note"><p role="status">Результат повторного запуска не подтверждён. Повторная отправка отключена. Новые запуски могли быть созданы в другой вкладке.</p><button disabled={busy} onClick={()=>void inspectRepeat()}>Проверить запуски</button>{repeatCandidates.length>0&&<ul>{repeatCandidates.map(item=><li key={item.id}><button onClick={()=>refresh(item.id)}>Открыть запуск {item.id.slice(0,8)}</button> · {statuses[item.status]} · {new Date(item.requested_at).toLocaleString('ru')}</li>)}</ul>}</aside>}{run.failure_code&&<div role="alert" className="error">Причина остановки: {run.failure_code}</div>}
    <TrainingProgress run={run} rounds={rounds} metrics={metrics} loaded={loaded} stale={stale}/>
    <div className="stats"><div><span>СТАТУС</span><strong className={`status ${run.status}`}>{statuses[run.status]||run.status}</strong></div><div><span>ПРОГРЕСС</span><strong>{run.completed_rounds}<small> / {run.total_rounds}</small></strong><progress value={run.completed_rounds} max={run.total_rounds}/></div><div><span>TEST ACCURACY</span><strong>{accuracy?`${(accuracy.value*100).toFixed(2)}%`:'—'}</strong></div><div><span>TEST MACRO-F1</span><strong>{f1?f1.value.toFixed(4):'—'}</strong></div></div>
    <section className="panel chart-panel"><div className="section-heading"><div><h3>Качество глобальной модели</h3></div><select aria-label="Метрика графика" value={metric} onChange={e=>setMetric(e.target.value)}><option value="accuracy">Accuracy</option><option value="macro_f1">Macro-F1</option></select></div>
      <Chart totalRounds={Math.max(run.total_rounds,runs.find(r=>r.id===comparison)?.total_rounds||0)} metric={metric} series={[{name:label,points:metrics},...(comparison?[{name:otherExperiment?`${otherExperiment.config.algorithm} · ${comparison.slice(0,8)}`:'Сравнение',points:other}]:[])]}/>
      <div className="compare"><label>Сравнить с запуском<select value={comparison} onChange={e=>setComparison(e.target.value)}><option value="">Без сравнения</option>{runs.filter(r=>r.id!==run.id).map(r=><option key={r.id} value={r.id}>{r.id.slice(0,8)} · {statuses[r.status]} · {new Date(r.requested_at).toLocaleString('ru')}</option>)}</select></label>{comparison&&otherExperiment&&<span className="muted">{paired?'Совпадают разбиение, начальные веса и бюджет.':'Условия различаются: это не парное сравнение.'}</span>}</div></section>
    <div className="detail-grid"><section className="panel"><h3>Раунды</h3><div className="scroll-table"><table><thead><tr><th>№</th><th>Состояние</th><th>Клиенты</th></tr></thead><tbody>{roundPage.items.map(r=><tr key={r.id}><td>{r.round_no}</td><td>{statuses[r.status]||r.status}</td><td>{r.accepted_count} / {r.selected_count}</td></tr>)}</tbody></table>{!rounds.length&&<p className="empty">Раунды ещё не созданы.</p>}</div>{roundPage.controls}</section>
      <section className="panel"><h3>Журнал запуска</h3><div className="event-list">{eventPage.items.map(e=><div key={e.sequence}><time>{new Date(e.created_at).toLocaleTimeString('ru')}</time><span>{({'round.started':'Раунд начат','update.accepted':'Обновление участника принято','round.completed':'Раунд завершён','run.created':'Запуск создан','run.cancelled':'Запуск остановлен','run.completed':'Обучение завершено','run.failed':'Ошибка обучения'} as Record<string,string>)[e.type]||e.type}</span></div>)}{!events.length&&<p className="empty">Событий пока нет.</p>}</div>{eventPage.controls}</section></div>
    <details className="panel"><summary>Конфигурация и контрольные суммы</summary><pre>{JSON.stringify(experiment?.config,null,2)}</pre><p className="hash">Config SHA-256: {run.config_hash}</p></details>
  </>;
}
