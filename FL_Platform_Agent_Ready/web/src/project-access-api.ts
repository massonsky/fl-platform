import {useCallback,useEffect,useRef} from 'react';
import {ApiError,token} from './api';
import type {components} from './generated/schema';

export type OrganizationProject=components['schemas']['OrganizationProject'];
export type ProjectPage=components['schemas']['OrganizationProjectPage'];
export type AccessPage=components['schemas']['OrganizationProjectAccessPage'];
export type AccessMember=components['schemas']['OrganizationProjectAccessMember'];
export type ProjectCreateInput=components['schemas']['OrganizationProjectCreateInput'];
export type GrantInput=components['schemas']['OrganizationProjectGrantInput'];
export type VisibilityInput=components['schemas']['OrganizationProjectVisibilityInput'];
export const projectPath=(organization:string,project?:string)=>`/organizations/${encodeURIComponent(organization)}/projects${project?`/${encodeURIComponent(project)}`:''}`;
export const pagePath=(path:string,cursor:string|null=null)=>`${path}?limit=20${cursor?`&cursor=${encodeURIComponent(cursor)}`:''}`;
export const errorCode=(cause:unknown)=>cause instanceof ApiError?cause.code||'':'';
export function projectError(cause:unknown){
  const messages:Record<string,string>={
    ORG_PROJECTS_NOT_CONFIGURED:'Раздел готовится к запуску.',
    ORGS_NOT_CONFIGURED:'Организации пока не включены на сервере. Личные проекты доступны.',
    NOT_FOUND:'Проект или организация больше недоступны. Обновите список.',
    FORBIDDEN:'Права изменились. Обновите данные перед следующим действием.',
    VERSION_CONFLICT:'Данные уже изменились. Обновите их и проверьте настройки перед сохранением.',
    SLUG_CONFLICT:'В этой организации уже есть проект с таким коротким именем.',
    INHERITED_ACCESS:'Доступ владельца или администратора определяется его ролью в организации.',
    MEMBER_NOT_ACTIVE:'Участник или его учётная запись неактивны. Новый доступ сейчас выдать нельзя.',
    INVALID_CURSOR:'Страница устарела. Обновите список с начала.',
  };
  return messages[errorCode(cause)]||(cause instanceof Error?cause.message:'Не удалось выполнить действие.');
}
export const accessSources:Record<OrganizationProject['access_source'],string>={
  organization_admin:'По роли в организации',explicit_denied:'Доступ отозван',
  explicit_viewer:'Назначен просмотр',explicit_researcher:'Назначена работа с проектом',
  creator:'Автор проекта',organization_read:'Общий просмотр',none:'Доступ не предоставлен',
};
export const writeLimits:Record<string,string>={
  MEMBER_NOT_ACTIVE:'Участник или учётная запись неактивны.',
  ORGANIZATION_SUSPENDED:'Организация приостановлена: доступен только просмотр.',
  GLOBAL_VIEWER:'Учётная запись допускает только просмотр; роль проекта не расширяет это право.',
  ORGANIZATION_VIEWER:'Роль в организации допускает только просмотр; роль проекта не расширяет это право.',
};
export function accessLabel(member:{capabilities:OrganizationProject['capabilities']}){
  return member.capabilities.can_manage_access?'Управление доступом':member.capabilities.can_create_experiments?'Работа с проектом':member.capabilities.can_read?'Просмотр':'Нет доступа';
}

// Every read, dialog and mutation completion gets a scope-bound ticket.
// Separate channels allow a list and a form to load without cancelling each other.
export function useRequestScope(scope:string){
  const key=`${token}:${scope}`,state=useRef({key,epoch:0,alive:true,requests:new Map<string,number>()});
  if(state.current.key!==key){state.current.key=key;state.current.epoch++;state.current.requests.clear()}
  useEffect(()=>{state.current.alive=true;return()=>{state.current.alive=false;state.current.epoch++;state.current.requests.clear()}},[]);
  return useCallback((channel:string)=>{
    const session=token,epoch=state.current.epoch,id=(state.current.requests.get(channel)||0)+1;
    state.current.requests.set(channel,id);
    return()=>state.current.alive&&state.current.key===key&&state.current.epoch===epoch&&token===session&&state.current.requests.get(channel)===id;
  },[key]);
}
