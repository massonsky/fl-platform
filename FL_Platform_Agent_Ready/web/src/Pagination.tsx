import {useState} from 'react';

// Pagination is local to the loaded API collection; no invented totals.
export function usePagination<T>(items: T[], resetKey = '', size = 8) {
  const [selection, setSelection] = useState({page: 0, key: resetKey});
  const pages = Math.max(1, Math.ceil(items.length / size));
  const page = selection.key === resetKey ? Math.min(selection.page, pages - 1) : 0;
  const move = (next: number) => setSelection({page: next, key: resetKey});
  return {
    items: items.slice(page * size, (page + 1) * size),
    controls: <div className="table-pagination">
      <span>{items.length ? `${page * size + 1}–${Math.min((page + 1) * size, items.length)} из ${items.length}` : 'Нет записей'}</span>
      {pages > 1 && <div>
        <button aria-label="Предыдущая страница" disabled={page === 0} onClick={() => move(page - 1)}>Назад</button>
        <span aria-live="polite">{page + 1} / {pages}</span>
        <button aria-label="Следующая страница" disabled={page === pages - 1} onClick={() => move(page + 1)}>Далее</button>
      </div>}
    </div>,
  };
}
