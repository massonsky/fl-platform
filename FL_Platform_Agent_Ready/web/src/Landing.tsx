import {NetworkMotion} from './NetworkMotion';
import {enter,motionAllowed} from './motion';
import {useEffect, useRef, useState} from 'react';
import {Brand, Icon} from './Icon';
import './landing.css';

const stages = [
  {title: 'Данные остаются на узлах', text: 'Каждый клиент обучает модель на своём разделе данных. Исходные записи не передаются координатору.', icon: 'agents'},
  {title: 'Обновления объединяются', text: 'Координатор проверяет версию модели и собирает обновления участников в каждом раунде.', icon: 'layers'},
  {title: 'Результат можно проверить', text: 'История раундов, метрики и контрольные суммы сохраняют связь между настройками и итоговой моделью.', icon: 'audit'},
];
export function Landing({logged}: {logged: boolean}) {
  const [rest, setRest] = useState(false);
  const [stage, setStage] = useState(0);
  const scene = useRef<HTMLDivElement>(null);
  const action = logged ? 'Открыть платформу' : 'Войти в платформу';
  const destination = logged ? '#experiments' : '#login';
  useEffect(() => {
    const media = matchMedia('(prefers-reduced-motion: reduce)');
    let visible = true;
    const update = () => setRest(media.matches || document.hidden || !visible);
    const observer = new IntersectionObserver(([entry]) => {visible = entry.isIntersecting; update()});
    if(scene.current) observer.observe(scene.current);
    media.addEventListener('change', update); document.addEventListener('visibilitychange', update); update();
    return () => {observer.disconnect(); media.removeEventListener('change', update); document.removeEventListener('visibilitychange', update)};
  }, []);
  function scrollTo(id: string){document.getElementById(id)?.scrollIntoView({behavior:motionAllowed()?'smooth':'instant'});document.getElementById(id)?.focus({preventScroll:true})}
  return <div className="landing">
    <a className="skip-link" href="#home-content" onClick={e=>{e.preventDefault();scrollTo('home-content')}}>К содержанию</a>
    <header className="landing-nav"><a href="#home" aria-label="FL Platform — главная"><Brand/></a><nav aria-label="О платформе"><button onClick={()=>scrollTo('workflow')}>Как это работает</button><button onClick={()=>scrollTo('capabilities')}>Возможности</button></nav><a className="button nav-entry" href={destination}>{action}<Icon name="arrow"/></a></header>
    <main id="home-content" tabIndex={-1}>
      <div className="ambient-background" aria-hidden="true"><i/><i/><i/></div>
      <section className="atlas-hero">
        <div className="hero-copy"><p className="eyebrow">FL Platform · FedAvg / FedProx</p><h1>Федеративное<br/><span>обучение.</span></h1><div className="hero-description"><p>Общая модель для вашей команды.<br/>Локальные данные остаются на узлах. Запускайте FedAvg и FedProx, следите за обучением и сохраняйте результат.</p><a className="button primary" href={destination}>{action}<Icon name="arrow"/></a></div></div>
        <div ref={scene} className={`atlas-scene ${rest ? 'is-paused' : ''}`}>
          <img src="/art/network-atlas.webp" width="1672" height="941" fetchPriority="high" alt="Пять локальных узлов связаны с общей моделью в центре сети"/>
          <NetworkMotion paused={rest}/>
          <div className="city-labels" aria-label="Участники федеративного обучения">{[1,2,3,4,5].map(n=><span key={n} className={`city-label city-${n}`}><i/>Узел 0{n}</span>)}</div>
        </div>
        <div className="hero-facts"><span><Icon name="agents"/>Локальное обучение</span><span><Icon name="layers"/>История каждого раунда</span><span><Icon name="download"/>Экспорт модели</span></div>
      </section>
      <section className="workflow-section" id="workflow" tabIndex={-1}>
        <div className="section-intro"><h2>От локальных данных<br/>к общей модели</h2><p>Один цикл обучения. У каждого участника — свой раздел данных, у команды — проверяемый результат.</p></div>
        <div className="workflow-layout"><div className="workflow-steps" role="tablist" aria-label="Этапы обучения">{stages.map((item,index)=><button key={item.title} id={`stage-${index}`} role="tab" aria-selected={stage===index} aria-controls="stage-panel" tabIndex={stage===index?0:-1} onClick={()=>{setStage(index);enter(document.getElementById('stage-panel'),220)}} onKeyDown={e=>{if(['ArrowDown','ArrowRight','ArrowUp','ArrowLeft','Home','End'].includes(e.key)){e.preventDefault();const next=e.key==='Home'?0:e.key==='End'?2:(index+(['ArrowDown','ArrowRight'].includes(e.key)?1:2))%3;setStage(next);document.getElementById(`stage-${next}`)?.focus()}}}><span className="step-number">{index+1}</span><span>{item.title}</span><Icon name="arrow"/></button>)}</div>
        <div className="workflow-detail" role="tabpanel" id="stage-panel" aria-labelledby={`stage-${stage}`} tabIndex={0}><div className="flow-visual" data-stage={stage} aria-hidden="true"><div className="flow-nodes">{[1,2,3].map(n=><span key={n}><Icon name="agents"/>Локальный узел {n}</span>)}</div><div className="flow-track"/><div className="flow-model"><Icon name={stage===2?'audit':'layers'}/>{stage===2?'Модель сохранена':'Общая модель'}</div></div><h3>{stages[stage].title}</h3><p>{stages[stage].text}</p><div className="protocol-line">Локальная модель <Icon name="arrow"/> Обновление <Icon name="arrow"/> Агрегация</div></div></div>
      </section>
      <section className="capabilities-section" id="capabilities" tabIndex={-1}>
        <div className="section-intro"><h2>Весь запуск<br/>под вашим контролем</h2><p>От выбора алгоритма до выгрузки артефактов. Настройки и результаты остаются связаны с конкретным запуском.</p></div>
        <div className="capability-list"><article><div className="feature-art" aria-hidden="true"><div><span>Алгоритм</span><strong>FedAvg / FedProx</strong></div><div><span>Разбиение</span><strong>IID / Non-IID</strong></div><div><span>Конфигурация</span><strong>Зафиксирована</strong></div></div><h3>Эксперименты</h3><p>FedAvg и FedProx, фиксированные разбиения и seed. Повторяйте запуск с той же конфигурацией.</p><span>Создание · повтор · остановка</span></article><article><div className="feature-art" aria-hidden="true"><Icon name="agents"/><div><span>Локальное обучение</span><strong>На узлах</strong></div><div><span>Координатор</span><strong>Обновления модели</strong></div></div><h3>Узлы и доступ</h3><p>Статус агентов, роли пользователей и журнал изменений. Доступ к операциям определяется ролью.</p><span>Администратор · исследователь · наблюдатель</span></article><article><div className="feature-art" aria-hidden="true"><Icon name="download"/><div><span>Веса + манифест</span><strong>Экспорт</strong></div><div><span>Целостность</span><strong>SHA-256</strong></div></div><h3>Метрики и артефакты</h3><p>Сравнивайте кривые обучения и выгружайте модель вместе с манифестом и контрольными суммами.</p><span>Accuracy · Macro-F1 · SHA-256</span></article></div>
        <div className="deployment-note"><Icon name="agents"/><p>Текущий профиль — Digits с пятью клиентами. Подключение собственных наборов данных требует отдельной интеграции.</p></div>
      </section>
      <section className="landing-close"><h2>Ваш следующий<br/>эксперимент — здесь.</h2><div><p>Войдите в рабочее пространство<br/>с учётной записью вашей команды.</p><a className="button primary" href={destination}>{action}<Icon name="arrow"/></a></div></section>
    </main>
    <footer className="landing-footer"><Brand/><p>Федеративное обучение. Проверяемые результаты.</p><button className="text-button" onClick={()=>window.scrollTo({top:0,behavior:motionAllowed()?'smooth':'instant'})}>К началу страницы<Icon name="arrow"/></button></footer>
  </div>;
}
