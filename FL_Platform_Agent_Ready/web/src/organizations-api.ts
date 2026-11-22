// COM-01 DTOs: contracts/http/ORGANIZATIONS.md. No changes to legacy project ACL.
import {ApiError} from './api';
export type OrganizationRole='owner'|'admin'|'researcher'|'viewer';
export type MembershipStatus='active'|'suspended'|'revoked';
export interface Organization {
  id:string;name:string;kind:'personal'|'shared';state:'active'|'suspended';
  revision:number;created_at:string;role:OrganizationRole;
  membership_status:MembershipStatus;can_manage_members:boolean;
}
export interface Membership {
  user_id:string;display_name:string;role:OrganizationRole;
  status:MembershipStatus;revision:number;updated_at:string;
}
export const organizationRoles:Record<OrganizationRole,string>={
  owner:'Владелец',admin:'Администратор',researcher:'Исследователь',viewer:'Наблюдатель',
};
export const membershipStatuses:Record<MembershipStatus,string>={
  active:'Активен',suspended:'Приостановлен',revoked:'Отозван',
};
export function organizationError(cause:unknown){
  const message=cause instanceof Error?cause.message:'Не удалось выполнить действие.';
  const code=cause instanceof ApiError?cause.code||message:message;
  const messages:Record<string,string>={
    LAST_OWNER:'Нельзя изменить доступ последнего владельца. Сначала назначьте другого действующего владельца.',
    ORGANIZATION_LIMIT:'Достигнут лимит создания организаций. Обратитесь к администратору платформы.',
    NOT_FOUND:'Организация или участник больше недоступны. Обновите список.',
    ORGS_NOT_CONFIGURED:'Раздел готовится к запуску.',
  };
  return messages[code]||message;
}
