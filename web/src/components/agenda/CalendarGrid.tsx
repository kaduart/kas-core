"use client";

import { useEffect, useState } from "react";
import { cn } from "@/lib/cn";
import { STATUS_LABEL, type AgendaBooking, type AgendaSlot } from "@/lib/kas/contract";
import { dayNumber, fmtWeekday, hhmm, localDate, localMinutes } from "@/lib/kas/agenda-time";
import type { Placed } from "./layout";

export const STATUS_STYLE: Record<string, string> = {
  confirmed: "border-brand-dark bg-brand-dark text-white",
  scheduled: "border-brand/40 bg-brand-tint text-brand-ink",
  tentative: "border-[#e2b66b] border-dashed bg-[#fff7ea] text-[#7a4a00]",
  completed: "border-[#cfd8d3] bg-[#eef1ef] text-[#45544d]",
  no_show: "border-[#e7b9ad] bg-[#f8e3dd] text-[#a3402b]",
  canceled: "border-[#d5ddd8] bg-white text-muted line-through",
};

function useNow(zone: string) {
  const [now, setNow] = useState<{ day: string; min: number } | null>(null);
  useEffect(() => {
    const tick = () => {
      const d = new Date();
      setNow({ day: localDate(d, zone), min: localMinutes(d.toISOString(), zone) });
    };
    tick();
    const t = setInterval(tick, 60_000);
    return () => clearInterval(t);
  }, [zone]);
  return now;
}

export interface GridProps {
  days: string[];
  zone: string;
  range: { start: number; end: number };
  bookings: Placed<AgendaBooking>[];
  slots: AgendaSlot[];
  selectedBookingId?: string;
  selectedSlotStart?: string;
  /** Horários livres só aparecem como botões quando a pessoa pode agendar (ou está remarcando). */
  slotsInteractive: boolean;
  onPickSlot: (s: AgendaSlot) => void;
  onPickBooking: (b: AgendaBooking) => void;
  onPickDay?: (day: string) => void;
  loading?: boolean;
}

/** Grade de horários: uma coluna por dia, posição e altura proporcionais ao tempo. */
export function CalendarGrid({ days, zone, range, bookings, slots, selectedBookingId, selectedSlotStart, slotsInteractive, onPickSlot, onPickBooking, onPickDay, loading }: GridProps) {
  const now = useNow(zone);
  const pxPerMin = days.length === 1 ? 1.5 : 1.15;
  const height = (range.end - range.start) * pxPerMin;
  const y = (min: number) => (Math.min(Math.max(min, range.start), range.end) - range.start) * pxPerMin;
  const hours: number[] = [];
  for (let m = range.start; m <= range.end; m += 60) hours.push(m);
  const week = days.length > 1;

  return (
    <div className="relative" aria-busy={loading || undefined}>
      {week ? (
        <div className="sticky top-0 z-[3] grid grid-cols-[52px_repeat(7,minmax(0,1fr))] border-b border-line bg-white pb-2">
          <span />
          {days.map((d) => {
            const today = now?.day === d;
            return (
              <button
                key={d}
                type="button"
                onClick={() => onPickDay?.(d)}
                className="flex flex-col items-center gap-0.5 rounded-xl py-1 text-center transition-colors hover:bg-canvas"
                aria-label={`Abrir o dia ${d}`}
              >
                <span className="text-[12px] font-semibold uppercase tracking-wide text-muted">{fmtWeekday(d)}</span>
                <span className={cn("grid size-8 place-items-center rounded-full text-[15px] font-bold tabular-nums", today && "bg-brand text-white")}>{dayNumber(d)}</span>
              </button>
            );
          })}
        </div>
      ) : null}

      <div className={cn("grid pt-3", week ? "grid-cols-[52px_repeat(7,minmax(0,1fr))]" : "grid-cols-[52px_minmax(0,1fr)]")}>
        <div className="relative" style={{ height }}>
          {hours.map((m) => (
            <span key={m} className="absolute right-2 -translate-y-1/2 text-[12px] font-medium tabular-nums text-muted" style={{ top: y(m) }}>
              {hhmm(m)}
            </span>
          ))}
        </div>

        {days.map((d) => {
          const daySlots = slots.filter((s) => localDate(new Date(s.starts_at), zone) === d);
          const dayBookings = bookings.filter((p) => p.day === d);
          const today = now?.day === d;
          return (
            <div
              key={d}
              className={cn("relative border-l border-line", today && week && "bg-brand-tint/25")}
              style={{
                height,
                backgroundImage: `repeating-linear-gradient(to bottom, var(--color-line) 0 1px, transparent 1px ${60 * pxPerMin}px)`,
              }}
            >
              {daySlots.map((s) => {
                const top = localMinutes(s.starts_at, zone);
                const end = localMinutes(s.ends_at, zone) || 24 * 60;
                const selected = selectedSlotStart === s.starts_at;
                const common = "absolute inset-x-1 overflow-hidden rounded-lg border text-left text-[12px] leading-tight";
                const style = { top: y(top) + 1, height: Math.max(y(end) - y(top) - 2, 14) };
                return slotsInteractive ? (
                  <button
                    key={s.starts_at}
                    type="button"
                    onClick={() => onPickSlot(s)}
                    aria-pressed={selected}
                    aria-label={`Horário livre às ${hhmm(top)}`}
                    className={cn(
                      common,
                      "z-[1] border-dashed px-2 py-1 font-medium transition-[background,border-color,color] duration-150",
                      selected ? "border-solid border-accent bg-accent text-on-accent" : "border-brand/35 bg-white/70 text-brand-ink hover:border-brand hover:bg-brand-tint",
                    )}
                    style={style}
                  >
                    <span className="tabular-nums">{hhmm(top)}</span>
                    {!week ? <span className="ml-1.5 text-brand-ink/70">livre</span> : null}
                  </button>
                ) : (
                  <span key={s.starts_at} aria-hidden className={cn(common, "border-dashed border-brand/20")} style={style} />
                );
              })}

              {dayBookings.map((p) => {
                const b = p.item;
                const w = 100 / p.lanes;
                const selected = selectedBookingId === b.id;
                const tall = (p.end - p.top) * pxPerMin >= 38;
                return (
                  <div key={b.id} className="absolute z-[2]" style={{ top: y(p.top) + 1, left: `calc(${p.lane * w}% + 3px)`, width: `calc(${w}% - 6px)` }}>
                    <button
                      type="button"
                      onClick={() => onPickBooking(b)}
                      aria-pressed={selected}
                      aria-label={`${STATUS_LABEL[b.status]}, ${hhmm(p.top)} a ${hhmm(p.end)}, cliente ${b.client_ref}`}
                      className={cn(
                        "flex w-full flex-col overflow-hidden rounded-lg border px-2 py-1 text-left text-[12px] leading-tight shadow-[0_1px_2px_rgba(15,31,23,.08)] transition-shadow duration-150",
                        STATUS_STYLE[b.status],
                        selected && "ring-[3px] ring-accent",
                      )}
                      style={{ height: Math.max(y(p.end) - y(p.top) - 2, 16) }}
                    >
                      <span className="font-bold tabular-nums">
                        {hhmm(p.top)}
                        {!week ? `–${hhmm(p.end)}` : ""}
                      </span>
                      {tall ? <span className="truncate opacity-90">{b.client_ref}</span> : null}
                    </button>
                    {p.tail > p.end ? (
                      <span
                        aria-hidden
                        title="Intervalo do serviço"
                        className="block rounded-b-md opacity-60"
                        style={{
                          height: y(p.tail) - y(p.end),
                          backgroundImage: "repeating-linear-gradient(135deg, var(--color-line) 0 4px, transparent 4px 8px)",
                        }}
                      />
                    ) : null}
                  </div>
                );
              })}

              {today && now && now.min > range.start && now.min < range.end ? (
                <span aria-hidden className="pointer-events-none absolute inset-x-0 z-[3] flex items-center" style={{ top: y(now.min) }}>
                  <span className="-ml-1 size-2 rounded-full bg-[#d64545]" />
                  <span className="h-px flex-1 bg-[#d64545]" />
                </span>
              ) : null}
            </div>
          );
        })}
      </div>
    </div>
  );
}
