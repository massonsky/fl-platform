import type {LegacyAdmissionControl} from './useLegacyAdmission';

export function LegacyLabNotice({admission}:{admission:LegacyAdmissionControl}){
  if(admission.canCreate||admission.state==='idle')return null;
  return <aside className="note" aria-label="Допуск к лаборатории" aria-busy={admission.state==='loading'}>
    <p role="status">{admission.message}</p>
    <p>Сохранённые результаты доступны для просмотра и экспорта. Уже принятые задачи продолжают работу; их можно остановить в пределах ваших прав.</p>
    {admission.state!=='loading'&&<button onClick={()=>void admission.refresh()}>Проверить допуск</button>}
  </aside>;
}
