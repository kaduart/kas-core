/** Geometria da grade da Agenda. Pura: testada em layout.test.ts. */
import type { AgendaBooking, AgendaSlot } from "@/lib/kas/contract";
import { localDate, localMinutes } from "@/lib/kas/agenda-time";

export interface Placed<T> {
  item: T;
  day: string;
  top: number; // minutos locais do início
  end: number; // minutos locais do fim
  tail: number; // minutos locais até onde o buffer bloqueia
  lane: number;
  lanes: number;
}

/** Faixa de horas da grade: cobre tudo o que aparece (no mínimo 4 h), arredondada à hora; 08–18 quando vazio. */
export function hourRange(bookings: AgendaBooking[], slots: AgendaSlot[], zone: string): { start: number; end: number } {
  let start = Infinity;
  let end = -Infinity;
  for (const b of bookings) {
    start = Math.min(start, localMinutes(b.starts_at, zone));
    end = Math.max(end, localMinutes(b.blocked_until, zone) || 24 * 60);
  }
  for (const s of slots) {
    start = Math.min(start, localMinutes(s.starts_at, zone));
    end = Math.max(end, localMinutes(s.ends_at, zone) || 24 * 60);
  }
  if (!Number.isFinite(start)) return { start: 8 * 60, end: 18 * 60 };
  start = Math.max(0, Math.floor(start / 60) * 60);
  end = Math.min(24 * 60, Math.ceil(Math.max(end, start + 4 * 60) / 60) * 60);
  return { start, end };
}

/**
 * Posiciona os agendamentos por dia e divide a largura quando se sobrepõem
 * (o espelho de outro sistema pode trazer sobreposições; a KAS nativa não cria).
 */
export function placeBookings(bookings: AgendaBooking[], zone: string): Placed<AgendaBooking>[] {
  const byDay = new Map<string, Placed<AgendaBooking>[]>();
  for (const b of bookings) {
    const day = localDate(new Date(b.starts_at), zone);
    const top = localMinutes(b.starts_at, zone);
    const sameDayEnd = localDate(new Date(b.ends_at), zone) === day;
    const end = sameDayEnd ? localMinutes(b.ends_at, zone) : 24 * 60;
    const tail = localDate(new Date(b.blocked_until), zone) === day ? Math.max(end, localMinutes(b.blocked_until, zone)) : 24 * 60;
    const list = byDay.get(day) ?? [];
    list.push({ item: b, day, top, end: Math.max(end, top + 10), tail, lane: 0, lanes: 1 });
    byDay.set(day, list);
  }
  const out: Placed<AgendaBooking>[] = [];
  for (const list of byDay.values()) {
    list.sort((a, b) => a.top - b.top || a.end - b.end);
    let cluster: Placed<AgendaBooking>[] = [];
    let clusterEnd = -1;
    const flush = () => {
      const lanesEnd: number[] = [];
      for (const p of cluster) {
        let lane = lanesEnd.findIndex((e) => e <= p.top);
        if (lane === -1) lane = lanesEnd.length;
        lanesEnd[lane] = p.end;
        p.lane = lane;
      }
      for (const p of cluster) p.lanes = lanesEnd.length;
      out.push(...cluster);
      cluster = [];
    };
    for (const p of list) {
      if (cluster.length && p.top >= clusterEnd) flush();
      cluster.push(p);
      clusterEnd = Math.max(clusterEnd, p.end);
    }
    flush();
  }
  return out;
}
