import {useEffect, useRef, type ReactNode} from 'react';

// Native modal supplies focus containment, inert background and Escape handling.
export function Dialog({label, onClose, children}: {label:string;onClose:()=>void;children:ReactNode}) {
  const ref=useRef<HTMLDialogElement>(null);
  const close=useRef(onClose);
  close.current=onClose;
  useEffect(()=>{
    const dialog=ref.current;
    const previous=document.activeElement as HTMLElement|null;
    dialog?.showModal();
    return()=>{dialog?.close();previous?.focus()};
  },[]);
  return <dialog ref={ref} className="modal" aria-labelledby={label} onCancel={e=>{e.preventDefault();close.current()}}>{children}</dialog>;
}
