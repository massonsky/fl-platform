import {useCallback,useEffect,useRef,useState,type FormEvent} from 'react';
import {request} from './api';
import {Dialog} from './Dialog';
import {Icon} from './Icon';
import {usePagination} from './Pagination';
import {organizationError,organizationRoles,membershipStatuses,
  type Organization,type Membership,type OrganizationRole,type MembershipStatus} from './organizations-api';
import './organizations.css';

export function Organizations({readOnly}:{readOnly:boolean}){
  const [items,setItems]=useState<Organization[]>([]),[selected,setSelected]=useState('');
  const [loading,setLoading]=useState(true),[error,setError]=useState(''),[available,setAvailable]=useState(true);
  const [creating,setCreating]=useState(false),[name,setName]=useState(''),[busy,setBusy]=useState(false);
  const [formError,setFormError]=useState(''),[notice,setNotice]=useState(''),[search,setSearch]=useState('');
  const alive=useRef(true),draftID=useRef('');
  const refresh=useCallback(async()=>{
    setLoading(true);
    try{
      const data=await request<{items:Organization[]}>('/organizations');
      if(alive.current){setItems(data.items);setSelected(old=>data.items.some(item=>item.id===old)?old:data.items[0]?.id||'');setAvailable(true);setError('')}
    }catch(cause){if(alive.current){
      if(cause instanceof Error&&cause.message==='ORGS_NOT_CONFIGURED'){setAvailable(false);setItems([]);setSelected('')}
      else setError(organizationError(cause));
    }}finally{if(alive.current)setLoading(false)}
  },[]);
  useEffect(()=>{alive.current=true;void refresh();return()=>{alive.current=false}},[refresh]);
  function openCreate(){draftID.current=crypto.randomUUID();setName('');setFormError('');setNotice('');setCreating(true)}
  async function create(event:FormEvent){
    event.preventDefault();setBusy(true);setFormError('');
    try{
      const org=await request<Organization>(`/organizations/${draftID.current}`,'PUT',{name:name.trim()});
      if(alive.current){setItems(old=>[...old.filter(item=>item.id!==org.id),org]);setSelected(org.id);setCreating(false);setNotice(`Организация «${org.name}» создана.`)}
    }catch(cause){if(alive.current)setFormError(organizationError(cause))}
    finally{if(alive.current)setBusy(false)}
  }
  const visible=items.filter(item=>item.name.toLocaleLowerCase('ru').includes(search.toLocaleLowerCase('ru')));
  const pagination=usePagination(visible,search,8),org=items.find(item=>item.id===selected);
  return <>
    <div className="page-heading"><div><h1>Организации</h1><p className="muted">Состав и роли ваших организаций.</p></div>
      {available&&!readOnly&&<button className="primary" disabled={loading||!!error} onClick={openCreate}><Icon name="plus"/>Создать организацию</button>}
    </div>
    {!available?<section className="panel empty"><h2>Раздел готовится к запуску</h2><p>Организации пока не включены на сервере. Ваши эксперименты и личные подключения доступны как прежде.</p><button disabled={loading} onClick={()=>void refresh()}>Проверить доступность</button></section>:<>
      {error&&<p role="alert" className="error">{error} <button disabled={loading} onClick={()=>void refresh()}>Обновить список</button></p>}
      {notice&&<p role="status" className="notice">{notice}</p>}
      <section className="panel" aria-label="Мои организации" aria-busy={loading}>
        <div className="section-heading organization-heading"><h2>Мои организации</h2><button disabled={loading} onClick={()=>void refresh()}>Обновить</button></div>
        {items.length>0&&<label className="search-field">Найти организацию<input type="search" value={search} onChange={event=>setSearch(event.target.value)} placeholder="Название организации"/></label>}
        {loading&&!items.length?<p role="status">Загружаем организации…</p>:visible.length?<>
          <div className="scroll-table organization-list-scroll"><table className="organization-list-table"><thead><tr><th>Организация</th><th>Ваша роль</th><th>Состояние</th></tr></thead><tbody>
            {pagination.items.map(item=><tr key={item.id} className={item.id===selected?'selected':''}><td><button className="run-link" aria-pressed={item.id===selected} onClick={()=>setSelected(item.id)}>{item.name}</button><small className="block muted">{item.kind==='personal'?'Личная':'Общая'}</small></td><td data-label="Ваша роль"><span>{organizationRoles[item.role]}</span></td><td data-label="Состояние"><span className={`badge ${item.state==='active'?'completed':'cancelled'}`}>{item.state==='active'?'Активна':'Приостановлена'}</span></td></tr>)}
          </tbody></table></div>{pagination.controls}
        </>:<div className="empty"><h3>{items.length?'Ничего не найдено':'Пока нет организаций'}</h3><p>{items.length?'Попробуйте другое название.':readOnly?'Здесь появятся организации, в которых у вас есть доступ.':'Создайте организацию, чтобы управлять её составом и ролями.'}</p></div>}
      </section>
      {org&&<OrganizationMembers key={org.id} organization={org} readOnly={readOnly} onChanged={refresh}/>}
      <p className="note">Членство пока не открывает совместный доступ к экспериментам, моделям и чатам. Здесь можно изменить доступ существующих участников; приглашения появятся отдельно.</p>
    </>}
    {creating&&available&&<Dialog label="organization-create-title" onClose={()=>{if(!busy)setCreating(false)}}>
      <div className="section-heading"><h2 id="organization-create-title">Создать организацию</h2><button className="icon-button" aria-label="Закрыть" disabled={busy} onClick={()=>setCreating(false)}><Icon name="close"/></button></div>
      <form onSubmit={create}><fieldset className="provider-fields" disabled={busy}><label>Название организации<input autoFocus required maxLength={120} value={name} onChange={event=>setName(event.target.value)} placeholder="Например, исследовательская группа"/></label><p className="muted">Вы станете владельцем организации. Личные проекты и переписка останутся в прежней области доступа.</p></fieldset>
        {formError&&<p className="error" role="alert">{formError}</p>}<button className="primary full" disabled={busy||!name.trim()}>{busy?'Создание…':'Создать организацию'}</button>
      </form>
    </Dialog>}
  </>;
}

function OrganizationMembers({organization:org,readOnly,onChanged}:{organization:Organization;readOnly:boolean;onChanged:()=>Promise<void>}){
  const [members,setMembers]=useState<Membership[]>([]),[loading,setLoading]=useState(true),[error,setError]=useState('');
  const [editing,setEditing]=useState<Membership|null>(null),[role,setRole]=useState<OrganizationRole>('viewer');
  const [status,setStatus]=useState<MembershipStatus>('active'),[busy,setBusy]=useState(false),[formError,setFormError]=useState(''),[notice,setNotice]=useState('');
  const alive=useRef(true),canRead=org.role==='owner'||org.role==='admin';
  const canWrite=canRead&&!readOnly&&org.state==='active'&&org.membership_status==='active'&&org.can_manage_members;
  const reload=useCallback(async()=>{
    if(!canRead)return null;
    setLoading(true);
    try{const data=await request<{items:Membership[]}>(`/organizations/${org.id}/members`);if(alive.current){setMembers(data.items);setError('')}return data.items}
    catch(cause){if(alive.current)setError(organizationError(cause));return null}
    finally{if(alive.current)setLoading(false)}
  },[org.id,canRead]);
  useEffect(()=>{alive.current=true;setMembers([]);setEditing(null);void reload();return()=>{alive.current=false}},[reload]);
  function edit(member:Membership){setEditing(member);setRole(member.role);setStatus(member.status);setFormError('');setNotice('')}
  async function refreshDraft(){
    setBusy(true);const fresh=await reload();
    if(alive.current&&fresh){const member=fresh.find(item=>item.user_id===editing?.user_id);if(member){edit(member);setFormError('Данные обновлены. Проверьте роль и доступ перед сохранением.')}else setEditing(null)}
    if(alive.current){if(!fresh)setFormError('Не удалось обновить участника. Повторите попытку позже.');setBusy(false)};
  }
  async function save(event:FormEvent){
    event.preventDefault();if(!editing||!canWrite)return;setBusy(true);setFormError('');
    try{
      const member=await request<Membership>(`/organizations/${org.id}/members/${editing.user_id}`,'PATCH',{role,status,revision:editing.revision});
      if(alive.current){setMembers(old=>old.map(item=>item.user_id===member.user_id?member:item));setEditing(null);setNotice('Роль и доступ участника сохранены.');await onChanged()}
    }catch(cause){if(alive.current)setFormError(organizationError(cause))}
    finally{if(alive.current)setBusy(false)}
  }
  const pagination=usePagination(members,org.id,8);
  return <section className="panel" aria-label={`Участники: ${org.name}`} aria-busy={loading&&canRead}>
    <div className="section-heading organization-heading"><div><h2>{org.name}</h2><p className="muted">Участники организации</p></div>{canRead&&<button disabled={loading||busy} onClick={()=>void reload()}>Обновить участников</button>}</div>
    {org.state==='suspended'&&<p className="note">Организация приостановлена. Доступен только просмотр.</p>}
    {readOnly&&<p className="muted">Ваша учётная запись позволяет только просмотр.</p>}
    {!canRead?<p className="muted">Ваша роль — {organizationRoles[org.role].toLocaleLowerCase('ru')}. Список участников доступен владельцам и администраторам организации.</p>:<>
      {error&&<p role="alert" className="error">{error}</p>}{notice&&<p role="status" className="notice">{notice}</p>}
      {loading&&!members.length?<p role="status">Загружаем участников…</p>:members.length?<><div className="scroll-table organization-member-scroll"><table className="organization-members-table"><thead><tr><th>Участник</th><th>Роль</th><th>Доступ</th><th>Действие</th></tr></thead><tbody>
        {pagination.items.map(member=><tr key={member.user_id}><td><strong>{member.display_name}</strong></td><td data-label="Роль">{organizationRoles[member.role]}</td><td data-label="Доступ"><span className={`badge ${member.status==='active'?'completed':member.status==='revoked'?'failed':'cancelled'}`}>{membershipStatuses[member.status]}</span></td><td className="organization-member-action">{canWrite&&(org.role==='owner'||member.role!=='owner')?<button disabled={busy||loading} aria-label={`Изменить доступ: ${member.display_name}`} onClick={()=>edit(member)}>Изменить</button>:<span className="muted">Только просмотр</span>}</td></tr>)}
      </tbody></table></div>{pagination.controls}</>:!error&&<p className="empty">Участники не найдены.</p>}
    </>}
    {editing&&canWrite&&<Dialog label="organization-member-title" onClose={()=>{if(!busy)setEditing(null)}}>
      <div className="section-heading"><h2 id="organization-member-title">Доступ участника</h2><button className="icon-button" aria-label="Закрыть" disabled={busy} onClick={()=>setEditing(null)}><Icon name="close"/></button></div>
      <p>{editing.display_name}</p><form onSubmit={save}><fieldset className="provider-fields" disabled={busy}>
        <label><span id="organization-member-role-label">Роль в организации</span><select aria-labelledby="organization-member-role-label" value={role} onChange={event=>setRole(event.target.value as OrganizationRole)}>{Object.entries(organizationRoles).filter(([value])=>org.role==='owner'||value!=='owner').map(([value,label])=><option key={value} value={value}>{label}</option>)}</select></label>
        <label><span id="organization-member-access-label">Доступ</span><select aria-labelledby="organization-member-access-label" value={status} onChange={event=>setStatus(event.target.value as MembershipStatus)}>{Object.entries(membershipStatuses).map(([value,label])=><option key={value} value={value}>{label}</option>)}</select></label>
        <p className="note">В организации должен оставаться действующий владелец. Изменение относится к членству в этой организации.</p>
      </fieldset>{formError&&<p role="alert" className="error">{formError} <button type="button" disabled={busy} onClick={()=>void refreshDraft()}>Обновить данные участника</button></p>}
        <button className="primary full" disabled={busy}>{busy?'Сохранение…':'Сохранить доступ'}</button>
      </form>
    </Dialog>}
  </section>;
}
