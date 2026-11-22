import {Icon} from './Icon';
import {Dialog} from './Dialog';
import {LegacyLabNotice} from './LegacyLabNotice';
import {useEffect,useState,type FormEvent} from 'react';
import {ApiError,list,request,type Experiment,type Preset,type Run} from './api';
import {useRequestScope} from './project-access-api';
import type {LegacyAdmissionControl} from './useLegacyAdmission';

interface Props {project:string;admission:LegacyAdmissionControl;onClose:()=>void;onCreated:(run:Run)=>void}

export function CreateExperiment({project,admission,onClose,onCreated}:Props){
  const begin=useRequestScope(`create-experiment:${project}`);
  const [presets,setPresets]=useState<Preset[]>([]),[preset,setPreset]=useState('digits-balanced-11');
  const [name,setName]=useState('Сравнение алгоритмов'),[algorithm,setAlgorithm]=useState<'fedavg'|'fedprox'>('fedavg');
  const [rounds,setRounds]=useState(25),[mu,setMu]=useState(.1),[busy,setBusy]=useState(false),[error,setError]=useState('');
  const [saved,setSaved]=useState<Experiment|null>(null),[unknown,setUnknown]=useState(false);
  useEffect(()=>{
    const current=begin('presets');
    request<{items:Preset[]}>('/presets').then(result=>{if(current())setPresets(result.items)}).catch(cause=>{if(current())setError((cause as Error).message)});
  },[begin]);
  async function submit(event:FormEvent){
    event.preventDefault();if(busy||unknown)return;
    const selected=presets.find(item=>item.id===preset);if(!saved&&!selected)return;
    const current=begin('submit');let queueAttempted=false;
    setBusy(true);setError('');
    try{
      if(!await admission.requireCurrent()||!current())return;
      let experiment=saved;
      if(!experiment){
        experiment=await request<Experiment>(`/projects/${project}/experiments`,'POST',{name,config:{...selected!.config,algorithm,rounds,mu:algorithm==='fedavg'?0:mu}});
        if(!current())return;
        setSaved(experiment);
      }
      if(!await admission.requireCurrent()||!current()){
        if(current())setError('Эксперимент сохранён. Допуск к новому запуску не подтверждён.');
        return;
      }
      queueAttempted=true;
      const run=await request<Run>(`/experiments/${experiment.id}/runs`,'POST');
      if(current())onCreated(run);
    }catch(cause){if(current()){
      admission.invalidate(cause);
      if(queueAttempted&&!(cause instanceof ApiError&&cause.code==='LEGACY_LAB_NOT_ADMITTED')){
        setUnknown(true);setError('Эксперимент сохранён, но результат запуска не подтверждён. Проверьте список запусков; повторный запрос автоматически не отправляется.');
      }else setError((cause as Error).message);
    }}finally{if(current())setBusy(false)}
  }
  async function inspect(){
    if(!saved||busy)return;
    const current=begin('inspect');setBusy(true);
    try{
      const runs=await list<Run>(`/projects/${project}/runs`);if(!current())return;
      const found=runs.find(item=>item.experiment_id===saved.id);
      if(found)onCreated(found);
      else setError('Подтверждённого запуска этого эксперимента пока нет. Можно проверить позже или закрыть форму. Новый запрос не отправлен.');
    }catch(cause){if(current())setError((cause as Error).message)}finally{if(current())setBusy(false)}
  }
  return <Dialog label="create-title" onClose={onClose}>
    <div className="section-heading"><h2 id="create-title">Параметры обучения</h2><button className="icon-button" onClick={onClose} aria-label="Закрыть"><Icon name="close"/></button></div>
    <LegacyLabNotice admission={admission}/>
    <form onSubmit={submit}>
      <fieldset className="provider-fields" disabled={busy||!!saved}>
        <label>Название<input value={name} onChange={event=>setName(event.target.value)} required maxLength={120}/></label>
        <label>Разбиение и seed<select value={preset} onChange={event=>setPreset(event.target.value)}>{presets.map(item=><option key={item.id} value={item.id}>{item.config.partition} · seed {item.config.seed}</option>)}</select></label>
        <div className="form-grid"><label>Алгоритм<select value={algorithm} onChange={event=>setAlgorithm(event.target.value as 'fedavg'|'fedprox')}><option value="fedavg">FedAvg</option><option value="fedprox">FedProx</option></select></label><label>Раунды<input type="number" min={1} max={10000} value={rounds} onChange={event=>setRounds(+event.target.value)} required/></label></div>
        {algorithm==='fedprox'&&<label>Проксимальный коэффициент μ<input type="number" min={0} step="0.01" value={mu} onChange={event=>setMu(+event.target.value)} required/></label>}
      </fieldset>
      <div className="note">Digits · MLP 64 → 32 → 10<br/>5 клиентов × 200 объектов · 3 эпохи · batch 25 · SGD 0,12<br/>Все клиенты участвуют в каждом раунде. Тест — только после завершения.</div>
      {saved&&!unknown&&<p role="status">Конфигурация сохранена. Следующее действие создаст запуск этого эксперимента.</p>}
      {error&&<p role="alert" className="error">{error}</p>}
      {unknown?<button type="button" className="full" disabled={busy} onClick={()=>void inspect()}>Проверить запуск</button>:
        <button className="primary full" disabled={busy||!admission.canCreate||(!saved&&!presets.length)}>{busy?'Проверка и создание…':saved?'Запустить сохранённый эксперимент':<>Создать и запустить<Icon name="arrow"/></>}</button>}
    </form>
  </Dialog>;
}
