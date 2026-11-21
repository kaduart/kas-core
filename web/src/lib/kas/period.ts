import type { Period } from "./types";

const DAYS: Record<Period, number> = { "7d": 7, "30d": 30, "90d": 90 };
export const PERIODS = Object.keys(DAYS) as Period[];

export function isPeriod(v: unknown): v is Period {
  return typeof v === "string" && v in DAYS;
}

/** Data de hoje (YYYY-MM-DD) no fuso da empresa. Brasil sem horário de verão desde 2019: -03:00 fixo. */
export function todaySaoPaulo(now = new Date()): string {
  return new Intl.DateTimeFormat("en-CA", { timeZone: "America/Sao_Paulo" }).format(now);
}

/**
 * Período do painel → intervalo que o KAS Core entende (RFC 3339 com o fuso de São Paulo),
 * contando o dia de hoje. "30d" em 21/11 = 23/10 00:00 até 21/11 23:59:59.
 */
export function periodRange(period: Period, now = new Date()): { from: string; to: string; fromDate: string; toDate: string } {
  const toDate = todaySaoPaulo(now);
  const d = new Date(`${toDate}T12:00:00Z`);
  d.setUTCDate(d.getUTCDate() - (DAYS[period] - 1));
  const fromDate = d.toISOString().slice(0, 10);
  return { from: `${fromDate}T00:00:00-03:00`, to: `${toDate}T23:59:59-03:00`, fromDate, toDate };
}

export const fmtDate = (iso: string) => `${iso.slice(8, 10)}/${iso.slice(5, 7)}/${iso.slice(0, 4)}`;
