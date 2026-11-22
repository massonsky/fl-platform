import {useState, type FormEvent} from 'react';
import {request, setToken} from './api';
import {Brand, Icon} from './Icon';

export function Login({onLogin}: {onLogin: ()=>void}) {
  const [username,setUsername]=useState(''),[password,setPassword]=useState('');
  const [error,setError]=useState(''),[busy,setBusy]=useState(false),[show,setShow]=useState(false);
  async function submit(e: FormEvent){
    e.preventDefault();setBusy(true);setError('');
    try{const result=await request<{token:string}>('/session','POST',{username,password});setToken(result.token);onLogin()}
    catch{setError('Не удалось войти. Проверьте логин и пароль или повторите позже.')}
    finally{setBusy(false)}
  }
  return <main className="login">
    <div className="login-story"><a href="#home" aria-label="FL Platform — главная"><Brand/></a><div className="login-message"><h1>Общая модель.<br/>Данные у вас.</h1><p>Рабочее пространство для управления федеративным обучением.</p></div><img src="/art/network-atlas.webp" width="1672" height="941" alt=""/><p className="login-bottom">FedAvg / FedProx · воспроизводимые запуски</p></div>
    <div className="login-form"><form onSubmit={submit} aria-busy={busy}><a href="#home" className="back-link"><Icon name="arrow"/>На главную</a><h2>Вход в платформу</h2><p className="muted">Продолжите работу с экспериментами,<br/>узлами и результатами обучения.</p><label>Логин<input autoComplete="username" value={username} onChange={e=>setUsername(e.target.value)} required autoCapitalize="none" spellCheck={false}/></label><label htmlFor="password">Пароль</label><span className="password-field"><input id="password" type={show?'text':'password'} autoComplete="current-password" value={password} onChange={e=>setPassword(e.target.value)} required/><button type="button" aria-pressed={show} onClick={()=>setShow(!show)}>{show?'Скрыть':'Показать'}</button></span>{error&&<p role="alert" className="error">{error}</p>}<button className="primary full" disabled={busy}>{busy?'Проверка…':<>Войти<Icon name="arrow"/></>}</button><p className="login-help">Для получения доступа обратитесь к администратору вашей платформы.</p></form></div>
  </main>;
}
