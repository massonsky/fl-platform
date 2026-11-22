import {ApiError,request} from './api';
import type {components} from './generated/schema';

export type LegacyLabAdmission=components['schemas']['LegacyLabAdmission'];
export type LegacyLabAdmissionChange=components['schemas']['LegacyLabAdmissionChange'];
export const legacyLabPath=(userID?:string)=>userID?`/admin/users/${encodeURIComponent(userID)}/legacy-lab`:'/me/legacy-lab';

function checked(value:LegacyLabAdmission,userID?:string){
  if(!value||!value.user_id||(userID&&value.user_id!==userID))throw new Error('Не удалось проверить допуск. Обновите данные.');
  return value;
}
export async function readLegacyAdmission(userID?:string){
  return checked(await request<LegacyLabAdmission>(legacyLabPath(userID)),userID);
}
export async function changeLegacyAdmission(userID:string,input:LegacyLabAdmissionChange){
  return checked(await request<LegacyLabAdmission>(legacyLabPath(userID),'PUT',input),userID);
}
export function legacyLabError(cause:unknown){
  const code=cause instanceof ApiError?cause.code:'';
  const copy:Record<string,string>={
    VERSION_CONFLICT:'Допуск уже изменён. Проверьте актуальное состояние и укажите причину заново.',
    FORBIDDEN:'Право управления допуском изменилось. Обновите данные или обратитесь к администратору.',
    NOT_FOUND:'Учётная запись больше недоступна.',
    DEPENDENCY_UNAVAILABLE:'Не удалось проверить допуск. Повторите проверку позже.',
    LEGACY_LAB_NOT_ADMITTED:'Для новых запусков нужен допуск к лаборатории.',
  };
  return (code&&copy[code])||(cause instanceof Error?cause.message:'Не удалось проверить допуск.');
}
export function validAdmissionReason(reason:string){
  return !!reason.trim()&&Array.from(reason).length<=240&&!/[\u0000-\u001f\u007f-\u009f]/.test(reason);
}
