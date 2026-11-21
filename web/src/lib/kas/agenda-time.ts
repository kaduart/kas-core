/**
 * Datas da Agenda no fuso da empresa (vem do catálogo do Go), não no fuso do navegador.
 * Pura: sem React, testada em agenda-time.test.ts.
 */

const partsCache = new Map<string, Intl.DateTimeFormat>();
function partsFormat(zone: string): Intl.DateTimeFormat {
  let f = partsCache.get(zone);
  if (!f) {
    f = new Intl.DateTimeFormat("en-US", { timeZone: zone, hourCycle: "h23", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" });
    partsCache.set(zone, f);
  }
  return f;
}

function wall(d: Date, zone: string) {
  const p: Record<string, number> = {};
  for (const x of partsFormat(zone).formatToParts(d)) if (x.type !== "literal") p[x.type] = Number(x.value);
  return { y: p.year, m: p.month, d: p.day, h: p.hour % 24, min: p.minute, s: p.second };
}

/** Diferença (ms) entre o relógio local do fuso e UTC naquele instante. */
function offsetMs(d: Date, zone: string): number {
  const w = wall(d, zone);
  return Date.UTC(w.y, w.m - 1, w.d, w.h, w.min, w.s) - Math.floor(d.getTime() / 1000) * 1000;
}

const pad = (n: number) => String(n).padStart(2, "0");

/** Data local (YYYY-MM-DD) de um instante, no fuso dado. */
export function localDate(d: Date, zone: string): string {
  const w = wall(d, zone);
  return `${w.y}-${pad(w.m)}-${pad(w.d)}`;
}

/** Minutos desde a meia-noite local de um instante ISO. */
export function localMinutes(iso: string, zone: string): number {
  const w = wall(new Date(iso), zone);
  return w.h * 60 + w.min;
}

/** Instante UTC de uma data local + minutos, no fuso dado (acerta horário de verão). */
export function zonedInstant(date: string, minutes: number, zone: string): Date {
  const [y, m, d] = date.split("-").map(Number);
  const guess = Date.UTC(y, m - 1, d, 0, minutes);
  const first = guess - offsetMs(new Date(guess), zone);
  return new Date(guess - offsetMs(new Date(first), zone));
}

export function addDays(date: string, n: number): string {
  const [y, m, d] = date.split("-").map(Number);
  const t = new Date(Date.UTC(y, m - 1, d + n));
  return `${t.getUTCFullYear()}-${pad(t.getUTCMonth() + 1)}-${pad(t.getUTCDate())}`;
}

/** 0 = domingo. */
export function weekdayOf(date: string): number {
  const [y, m, d] = date.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d)).getUTCDay();
}

/** Segunda-feira da semana da data. */
export function weekStart(date: string): string {
  return addDays(date, -((weekdayOf(date) + 6) % 7));
}

export function weekDays(date: string): string[] {
  const s = weekStart(date);
  return Array.from({ length: 7 }, (_, i) => addDays(s, i));
}

export const hhmm = (minutes: number) => `${pad(Math.floor(minutes / 60))}:${pad(minutes % 60)}`;

const dayLong = new Intl.DateTimeFormat("pt-BR", { timeZone: "UTC", weekday: "long", day: "numeric", month: "long" });
const dayShort = new Intl.DateTimeFormat("pt-BR", { timeZone: "UTC", weekday: "short" });
const dayMonth = new Intl.DateTimeFormat("pt-BR", { timeZone: "UTC", day: "2-digit", month: "short" });
const utcOf = (date: string) => {
  const [y, m, d] = date.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d, 12));
};

/** "segunda-feira, 30 de novembro" */
export const fmtDayLong = (date: string) => dayLong.format(utcOf(date));
/** "seg" */
export const fmtWeekday = (date: string) => dayShort.format(utcOf(date)).replace(".", "");
/** "30 de nov." → "30 nov" */
export const fmtDayMonth = (date: string) => dayMonth.format(utcOf(date)).replace(/\s*de\s*/g, " ").replace(".", "");
export const dayNumber = (date: string) => Number(date.slice(8, 10));
