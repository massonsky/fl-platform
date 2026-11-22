const paths: Record<string, string> = {
  organizations: 'M4 21V7h10v14M14 11h6v10M2 21h20M7 10h1m2 0h1m-4 4h1m2 0h1m6 0h1M8 21v-4h3v4',
  assistants: 'M8 4h8v4H8zM4 12h16v8H4zm4 4h1m6 0h1M12 8v4',
  chat: 'M4 4h16v12H9l-5 4zm4 4h8m-8 4h5',
  providers: 'M8 3v5m8-5v5M5 8h14v3a7 7 0 0 1-14 0zm7 10v3',
  arrow: 'M4 12h16m-6-6 6 6-6 6',
  experiments: 'M4 4h6v6H4zm10 0h6v6h-6zM4 14h6v6H4zm10 0h6v6h-6z',
  agents: 'M9 3h6v6H9zM3 15h6v6H3zm12 0h6v6h-6zM12 9v3M6 15v-3h12v3',
  users: 'M16 21v-3a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v3m18 0v-3a4 4 0 0 0-3-4M12 6a3 3 0 1 1-6 0 3 3 0 0 1 6 0m4-3a3 3 0 0 1 0 6',
  audit: 'M5 3h14v18H5zM9 8h6m-6 4h6m-6 4h4',
  account: 'M16 7a4 4 0 1 1-8 0 4 4 0 0 1 8 0M4 21v-2a8 8 0 0 1 16 0v2',
  logout: 'M9 4H4v16h5m5-13 5 5-5 5m-5-5h10',
  plus: 'M12 5v14M5 12h14',
  close: 'm6 6 12 12M6 18 18 6',
  pause: 'M8 5v14M16 5v14',
  play: 'm8 4 12 8-12 8z',
  layers: 'm12 3 10 5-10 5L2 8zm-10 9 10 5 10-5M2 17l10 5 10-5',
  download: 'M12 3v12m-5-5 5 5 5-5M4 16v5h16v-5',
};
export function Icon({name}: {name: string}) {
  return <svg className="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d={paths[name] || paths.experiments}/></svg>;
}
export function Brand(){return <span className="brand"><Icon name="layers"/><span>FL Platform</span></span>}
