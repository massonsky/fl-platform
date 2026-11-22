import type {components} from "./generated/schema";
export type Project = components["schemas"]["Project"];
export type Config = components["schemas"]["RunConfig"];
export type Experiment = components["schemas"]["Experiment"];
export type Run = components["schemas"]["Run"];
export type Metric = components["schemas"]["MetricPoint"];
export type Round = components["schemas"]["Round"];
export type Event = components["schemas"]["Event"];
export interface Preset {id: string; config: Config}
const errorMessages:Record<string,string>={AI_NOT_CONFIGURED:'Подключения моделей пока не настроены на сервере.',PROVIDER_NOT_ALLOWED:'Этот адрес не разрешён администратором сервера.',INVALID_PROVIDER_URL:'Нужен разрешённый HTTPS-адрес сервиса.',PROVIDER_URL_IMMUTABLE:'Для нового адреса создайте отдельное подключение.',PROVIDER_LIMIT:'Достигнут лимит: 20 подключений.',API_KEY_REQUIRED:'Введите API-ключ провайдера.',VERSION_CONFLICT:'Настройки уже изменились. Закройте форму, обновите список и повторите.',PROVIDER_DISABLED:'Подключение отключено. Включите его в настройках.',PROVIDER_AUTH_FAILED:'Провайдер отклонил API-ключ. Проверьте его в настройках подключения.',PROVIDER_RATE_LIMITED:'Провайдер ограничил запросы. Повторите позже.',PROVIDER_UNAVAILABLE:'Сервис моделей не отвечает или недоступен. Проверьте адрес и повторите позже.',INVALID_PROVIDER_RESPONSE:'Провайдер вернул несовместимый каталог моделей.',FORBIDDEN:'Недостаточно прав для этого действия.',LAST_ADMIN:'Нельзя отключить или понизить последнего администратора.',USERNAME_CONFLICT:'Такой логин уже занят.',CURRENT_PASSWORD_INVALID:'Текущий пароль неверен.',UNAUTHORIZED:'Войдите в систему повторно.',INVALID_REQUEST:'Проверьте заполненные поля.',RATE_LIMITED:'Слишком много попыток. Повторите через минуту.'};
Object.assign(errorMessages,{ASSISTANT_LIMIT:'Достигнут лимит: 50 профилей ассистентов.',ASSISTANT_DISABLED:'Ассистент отключён для новых диалогов.',CONSENT_REQUIRED:'Подтвердите передачу сообщений провайдеру.',GENERATION_ACTIVE:'Дождитесь завершения текущего ответа или остановите его.',IDEMPOTENCY_CONFLICT:'Этот запрос уже использован. Обновите историю.',HISTORY_TOO_LARGE:'Диалог достиг лимита. Начните новый.',CONVERSATION_LIMIT:'Достигнут лимит диалогов. Удалите ненужные.',PROVIDER_CHANGED:'Настройки провайдера изменились. Отправьте новое сообщение.',MODEL_REQUEST_REJECTED:'Провайдер отклонил запрос к этой модели.',GENERATION_FAILED:'Не удалось получить ответ.',GENERATION_TIMEOUT:'Время ожидания ответа истекло.',GENERATION_CANCELLED:'Ответ остановлен.',GENERATION_INTERRUPTED:'Ответ прерван. Можно отправить новое сообщение.',PROVIDER_STREAM_INTERRUPTED:'Провайдер прервал ответ.',PROVIDER_STREAM_ERROR:'Ошибка потока провайдера.',UNSUPPORTED_MODEL_OUTPUT:'Модель вернула неподдерживаемый формат ответа.',RESPONSE_TOO_LARGE:'Ответ превысил допустимый размер.'});
errorMessages.DEPENDENCY_UNAVAILABLE='Сервис временно недоступен. Повторите попытку позже.';
errorMessages.EXPORT_BUSY='Сервер готовит другой архив. Повторите попытку через несколько секунд.';
errorMessages.EXPORT_LIMIT_EXCEEDED='Архив превышает допустимый размер экспорта. Обратитесь к администратору.';
errorMessages.LAST_OWNER='Сначала назначьте другого действующего владельца активной организации.';
export function errorMessage(code:string){return errorMessages[code]||code||'Ошибка запроса'}
export class ApiError extends Error {constructor(public status: number, message: string, public code?: string) {super(message)}}
export let token = sessionStorage.getItem('fl-session') || '';
export function setToken(value: string) {token = value; value ? sessionStorage.setItem('fl-session', value) : sessionStorage.removeItem('fl-session')}
export async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const sessionAtStart=token;
  const response = await fetch(`/api/v1${path}`, {method, headers: {
    ...(token ? {Authorization: `Bearer ${token}`} : {}),
    ...(method === 'POST' ? {'Idempotency-Key': crypto.randomUUID()} : {}),
    ...(body === undefined ? {} : {'Content-Type': 'application/json'}),
  }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(30000)});
  if(sessionAtStart!==token) throw new Error('Сессия изменилась.');
  if (!response.ok) {
    const error = await response.json().catch(() => ({code: 'NETWORK_ERROR'}));
    if(sessionAtStart!==token) throw new Error('Сессия изменилась.');
    if (response.status === 401) {setToken(''); window.dispatchEvent(new Event('fl-unauthorized'))}
    throw new ApiError(response.status, errorMessages[error.code] || error.code || `HTTP ${response.status}`, error.code);
  }
  const result = await response.json();
  if(sessionAtStart!==token) throw new Error('Сессия изменилась.');
  return result;
}
export async function list<T>(path: string): Promise<T[]> {
  const items: T[] = []; let cursor: string | null = null;
  do {
    const separator = path.includes('?') ? '&' : '?';
    const page: {items: T[]; next_cursor: string | null} = await request(`${path}${separator}limit=100${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`);
    items.push(...page.items); cursor = page.next_cursor;
  } while (cursor);
  return items;
}
export async function download(run: string) {
  const response = await fetch(`/api/v1/runs/${run}/export`, {headers: {Authorization: `Bearer ${token}`}});
  if (!response.ok) throw new Error(`Экспорт недоступен (${response.status})`);
  const url = URL.createObjectURL(await response.blob()); const link = document.createElement('a');
  link.href = url; link.download = `run-${run}.zip`; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
}
export const statuses: Record<string, string> = {queued: 'В очереди', running: 'Обучение', completed: 'Завершён', cancelled: 'Отменён', failed: 'Ошибка', collecting: 'Сбор обновлений', aggregating: 'Агрегация', evaluating: 'Оценка'};
