import {useEffect,useLayoutEffect,useRef} from 'react';

export const motionAllowed=()=>!matchMedia('(prefers-reduced-motion: reduce)').matches && document.documentElement.dataset.input!=='keyboard';
const curve='cubic-bezier(0.23, 1, 0.32, 1)';
const workspaceRoutes=new Set(['#experiments','#agents','#users','#audit','#account','#providers','#chat','#assistants','#organizations','#projects']);
export function enter(element:Element|null,duration=240){
  if(!element || !motionAllowed())return;
  return element.animate([{opacity:0,transform:'translateY(12px)'},{opacity:1,transform:'translateY(0)'}],{duration,easing:curve});
}

// Route entry follows the React commit, never a timer racing the hash listener.
// Frequent workspace navigation has no exit delay or vertical displacement.
export function useRouteMotion(routeKey:string){
  const pending=useRef<Animation|undefined>(undefined);
  const generation=useRef(0);
  const mounted=useRef(false);
  const surface=()=>document.querySelector('.workspace .content, .login-form form, .landing');
  const cancel=()=>{generation.current++;pending.current?.cancel();pending.current=undefined};

  useLayoutEffect(()=>{
    cancel();
    if(!mounted.current){mounted.current=true;return}
    window.scrollTo(0,0);
    if(location.hash==='#home'||!location.hash||!motionAllowed())return;
    const element=surface();
    pending.current=element?.matches('.workspace .content')
      ? element.animate([{opacity:.92},{opacity:1}],{duration:140,easing:curve})
      : enter(element);
    return()=>{pending.current?.cancel();pending.current=undefined};
  },[routeKey]);

  useEffect(()=>{
    const keyboard=()=>{document.documentElement.dataset.input='keyboard';cancel()};
    const pointer=()=>{document.documentElement.dataset.input='pointer'};
    const changed=()=>{generation.current++};
    const clicked=async(event:MouseEvent)=>{
      if(event.defaultPrevented||event.button!==0||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;
      const anchor=(event.target as Element).closest<HTMLAnchorElement>('a[href^="#"]');
      if(!anchor||!/^#(home|login|projects|experiments|agents|users|audit|account|providers|chat|assistants|organizations)$/.test(anchor.hash))return;
      const target=anchor.hash;
      // Even an active-route click must invalidate an earlier delayed navigation.
      cancel();
      if(target===location.hash||!motionAllowed())return;
      if(document.querySelector('.workspace')&&workspaceRoutes.has(target))return;
      event.preventDefault();
      const current=generation.current;
      const element=surface();
      const opacity=element?getComputedStyle(element).opacity:'1';
      const animation=element?.animate([{opacity,transform:'translateY(0)'},{opacity:0,transform:'translateY(-6px)'}],{duration:120,easing:curve,fill:'forwards'});
      pending.current=animation;
      try{await animation?.finished}catch{return}
      if(current!==generation.current)return;
      location.hash=target;
    };
    document.addEventListener('keydown',keyboard,true);
    document.addEventListener('pointerdown',pointer,true);
    document.addEventListener('click',clicked);
    window.addEventListener('hashchange',changed);
    return()=>{cancel();document.removeEventListener('keydown',keyboard,true);document.removeEventListener('pointerdown',pointer,true);document.removeEventListener('click',clicked);window.removeEventListener('hashchange',changed)};
  },[]);
}
