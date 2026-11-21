"use client";

import { useEffect, useRef, useState } from "react";
import { ArrowRightLeft, CalendarCheck2, CalendarX2, Clock3, Loader2, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/cn";
import { ACTIVE_STATUSES, REF_PATTERN, STATUS_LABEL, type AgendaBooking, type AgendaResource, type AgendaService, type AgendaSlot } from "@/lib/kas/contract";
import { fmtDayLong, hhmm, localDate, localMinutes } from "@/lib/kas/agenda-time";
import { agendaErrorText, useAgendaSlots, useAgendaWrites } from "@/lib/kas/agenda";
import { STATUS_STYLE } from "./CalendarGrid";

export type Selection = { kind: "slot"; slot: AgendaSlot; key: string } | { kind: "booking"; booking: AgendaBooking } | null;

const minutesBetween = (a: string, b: string) => Math.round((new Date(b).getTime() - new Date(a).getTime()) / 60000);

function SlotChips({ slots, zone, onPick, selected, empty }: { slots: AgendaSlot[]; zone: string; onPick: (s: AgendaSlot) => void; selected?: string; empty: string }) {
  if (slots.length === 0) return <p className="text-[14px] text-muted">{empty}</p>;
  return (
    <ul className="grid grid-cols-3 gap-2 sm:grid-cols-4 xl:grid-cols-3" aria-label="Horários livres">
      {slots.map((s) => (
        <li key={s.starts_at}>
          <button
            type="button"
            onClick={() => onPick(s)}
            aria-pressed={selected === s.starts_at}
            className={cn(
              "min-h-11 w-full rounded-xl border text-[14px] font-semibold tabular-nums transition-colors duration-150",
              selected === s.starts_at ? "border-accent bg-accent text-on-accent" : "border-[#d5ddd8] bg-white hover:border-brand hover:bg-brand-tint",
            )}
          >
            {hhmm(localMinutes(s.starts_at, zone))}
          </button>
        </li>
      ))}
    </ul>
  );
}

export interface SidePanelProps {
  tenantId: string;
  zone: string;
  day: string;
  resource: AgendaResource;
  service?: AgendaService;
  writable: boolean;
  readOnlyReason?: string;
  daySlots: AgendaSlot[];
  dayBookings: AgendaBooking[];
  slotsLoading: boolean;
  selection: Selection;
  setSelection: (s: Selection) => void;
  newKey: () => string;
}

export function SidePanel(p: SidePanelProps) {
  const { selection } = p;
  const ref = useRef<HTMLElement>(null);
  // Telas estreitas: o painel fica abaixo da grade; ao escolher algo, ele vem para a vista.
  const selected = selection?.kind === "slot" ? selection.slot.starts_at : selection?.kind === "booking" ? selection.booking.id : "";
  useEffect(() => {
    if (selected && window.matchMedia("(max-width: 1279px)").matches) ref.current?.scrollIntoView({ behavior: "smooth", block: "start" });
  }, [selected]);
  return (
    <Card ref={ref} className="scroll-mt-4 xl:sticky xl:top-4" aria-live="polite">
      {selection?.kind === "slot" ? (
        <CreateForm {...p} slot={selection.slot} idemKey={selection.key} />
      ) : selection?.kind === "booking" ? (
        <BookingDetail key={`${selection.booking.id}:${selection.booking.version}`} {...p} booking={selection.booking} />
      ) : (
        <DayOverview {...p} />
      )}
    </Card>
  );
}

function DayOverview({ zone, day, daySlots, dayBookings, slotsLoading, writable, readOnlyReason, setSelection, newKey }: SidePanelProps) {
  const active = dayBookings.filter((b) => ACTIVE_STATUSES.includes(b.status));
  return (
    <>
      <h3 className="text-[17px] font-bold tracking-tight first-letter:uppercase">{fmtDayLong(day)}</h3>
      <p className="mb-4 text-[13.5px] text-muted tabular-nums">
        {active.length} {active.length === 1 ? "agendamento" : "agendamentos"} · {daySlots.length} {daySlots.length === 1 ? "horário livre" : "horários livres"}
      </p>
      {writable ? (
        <>
          <h4 className="mb-2 text-[14px] font-semibold">Escolha um horário para agendar</h4>
          {slotsLoading && daySlots.length === 0 ? (
            <div className="h-24 animate-pulse rounded-xl bg-canvas" aria-label="Carregando horários" />
          ) : (
            <SlotChips slots={daySlots} zone={zone} onPick={(s) => setSelection({ kind: "slot", slot: s, key: newKey() })} empty="Nenhum horário livre neste dia. Tente outro dia ou outro serviço." />
          )}
        </>
      ) : (
        <p className="rounded-xl bg-canvas px-3.5 py-3 text-[14px] text-[#45544d]">{readOnlyReason}</p>
      )}
    </>
  );
}

function PanelHeading({ title, onClose }: { title: string; onClose: () => void }) {
  return (
    <div className="mb-3 flex items-center gap-2">
      <h3 className="text-[17px] font-bold tracking-tight">{title}</h3>
      <Button variant="ghost" size="icon" className="ml-auto size-9" onClick={onClose} aria-label="Fechar">
        <X aria-hidden />
      </Button>
    </div>
  );
}

function Summary({ rows }: { rows: [string, string][] }) {
  return (
    <dl className="mb-4 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 rounded-xl bg-canvas px-3.5 py-3 text-[14px]">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted">{k}</dt>
          <dd className="min-w-0 truncate font-semibold tabular-nums">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

const STATUS_CHOICES = [
  { v: "scheduled", label: "Agendado" },
  { v: "tentative", label: "Pré-agendado" },
  { v: "confirmed", label: "Confirmado" },
] as const;

function CreateForm({ tenantId, zone, resource, service, slot, idemKey, setSelection, writable, readOnlyReason }: SidePanelProps & { slot: AgendaSlot; idemKey: string }) {
  const { create } = useAgendaWrites(tenantId);
  const [client, setClient] = useState("");
  const [status, setStatus] = useState<(typeof STATUS_CHOICES)[number]["v"]>("scheduled");
  const [touched, setTouched] = useState(false);
  const start = localMinutes(slot.starts_at, zone);
  const end = start + minutesBetween(slot.starts_at, slot.ends_at);
  const clientOk = REF_PATTERN.test(client.trim());

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (!clientOk || create.isPending) return;
    create.mutate(
      { chave: idemKey, recurso: resource.id, cliente: client.trim(), servico: service?.id, inicio: slot.starts_at, fim: slot.ends_at, status },
      {
        onSuccess: (r) => {
          toast.success(r.replayed ? "Esse agendamento já tinha sido feito." : `Agendado às ${hhmm(start)}.`);
          setSelection({ kind: "booking", booking: r.booking });
        },
      },
    );
  };

  return (
    <form onSubmit={submit} noValidate>
      <PanelHeading title="Novo agendamento" onClose={() => setSelection(null)} />
      <Summary
        rows={[
          ["Com", resource.label],
          ["Dia", fmtDayLong(localDate(new Date(slot.starts_at), zone))],
          ["Horário", `${hhmm(start)} – ${hhmm(end)}`],
          ["Serviço", service ? `${service.label}${service.buffer_min ? ` (+${service.buffer_min} min de intervalo)` : ""}` : `${end - start} min`],
        ]}
      />
      {!writable ? (
        <p className="rounded-xl bg-canvas px-3.5 py-3 text-[14px]">{readOnlyReason}</p>
      ) : (
        <>
          <label htmlFor="agenda-cliente" className="mb-1.5 block text-[14px] font-semibold">
            Código do cliente
          </label>
          <input
            id="agenda-cliente"
            value={client}
            onChange={(e) => setClient(e.target.value)}
            onBlur={() => setTouched(true)}
            autoComplete="off"
            spellCheck={false}
            inputMode="text"
            aria-invalid={touched && !clientOk}
            aria-describedby="agenda-cliente-ajuda"
            placeholder="ex.: cli-1042"
            className={cn(
              "min-h-11 w-full rounded-xl border bg-white px-3.5 text-[15px] text-ink placeholder:text-muted",
              touched && !clientOk ? "border-[#d0644b]" : "border-[#d5ddd8]",
            )}
          />
          <p id="agenda-cliente-ajuda" className={cn("mt-1.5 text-[12.5px]", touched && !clientOk ? "text-[#a3402b]" : "text-muted")}>
            {touched && !clientOk ? "Use só letras, números, ponto, hífen ou dois-pontos, sem espaço." : "O código do cadastro, sem nome nem telefone. A agenda não guarda dados pessoais."}
          </p>

          <fieldset className="mt-4">
            <legend className="mb-1.5 text-[14px] font-semibold">Situação</legend>
            <div className="inline-flex rounded-xl bg-[#f1f3f2] p-[3px]" role="radiogroup">
              {STATUS_CHOICES.map((s) => (
                <button
                  key={s.v}
                  type="button"
                  role="radio"
                  aria-checked={status === s.v}
                  onClick={() => setStatus(s.v)}
                  className={cn("min-h-9 rounded-[10px] px-3 text-[13px] font-medium transition-colors", status === s.v && "bg-brand-dark text-white")}
                >
                  {s.label}
                </button>
              ))}
            </div>
          </fieldset>

          {create.isError ? (
            <p role="alert" className="mt-4 rounded-xl bg-[#f8e3dd] px-3.5 py-3 text-[14px] font-medium text-[#8a2f1c]">
              {agendaErrorText(create.error)}
            </p>
          ) : null}

          <div className="mt-5 flex flex-wrap gap-2">
            <Button type="submit" disabled={create.isPending} className="flex-1">
              {create.isPending ? <Loader2 className="animate-spin" aria-hidden /> : <CalendarCheck2 aria-hidden />}
              Agendar às {hhmm(start)}
            </Button>
            <Button type="button" variant="outline" onClick={() => setSelection(null)}>
              Voltar
            </Button>
          </div>
        </>
      )}
    </form>
  );
}

function BookingDetail({ tenantId, zone, day, resource, booking, writable, readOnlyReason, setSelection }: SidePanelProps & { booking: AgendaBooking }) {
  const { cancel, reschedule } = useAgendaWrites(tenantId);
  const [mode, setMode] = useState<"view" | "move" | "cancel">("view");
  const [target, setTarget] = useState<AgendaSlot | null>(null);
  const duration = minutesBetween(booking.starts_at, booking.ends_at);
  const moveSlots = useAgendaSlots(tenantId, { resourceId: booking.resource_id, from: day, to: day, durationMin: duration }, mode === "move");
  const start = localMinutes(booking.starts_at, zone);
  const active = ACTIVE_STATUSES.includes(booking.status);
  const busy = cancel.isPending || reschedule.isPending;
  const error = cancel.error ?? reschedule.error;

  const doCancel = () =>
    cancel.mutate(
      { id: booking.id, versao: booking.version },
      {
        onSuccess: (r) => {
          toast.success("Agendamento cancelado. O horário ficou livre.");
          setSelection({ kind: "booking", booking: r.booking });
        },
        onError: () => setMode("view"),
      },
    );
  const doMove = () => {
    if (!target) return;
    reschedule.mutate(
      { id: booking.id, versao: booking.version, inicio: target.starts_at, fim: target.ends_at },
      {
        onSuccess: (r) => {
          toast.success(`Remarcado para ${hhmm(localMinutes(r.booking.starts_at, zone))}.`);
          setSelection({ kind: "booking", booking: r.booking });
        },
        onError: () => setTarget(null),
      },
    );
  };

  return (
    <div>
      <PanelHeading title="Agendamento" onClose={() => setSelection(null)} />
      <span className={cn("mb-3 inline-flex rounded-full border px-2.5 py-0.5 text-[12.5px] font-semibold", STATUS_STYLE[booking.status].replace("line-through", ""))}>
        {STATUS_LABEL[booking.status]}
      </span>
      <Summary
        rows={[
          ["Cliente", booking.client_ref],
          ["Com", resource.label],
          ["Dia", fmtDayLong(localDate(new Date(booking.starts_at), zone))],
          ["Horário", `${hhmm(start)} – ${hhmm(start + duration)}`],
          ...(booking.buffer_min ? ([["Intervalo", `${booking.buffer_min} min depois`]] as [string, string][]) : []),
        ]}
      />

      {error ? (
        <p role="alert" className="mb-4 rounded-xl bg-[#f8e3dd] px-3.5 py-3 text-[14px] font-medium text-[#8a2f1c]">
          {agendaErrorText(error)}
        </p>
      ) : null}

      {!active ? (
        <p className="text-[14px] text-muted">
          {booking.status === "canceled" ? "Cancelado. O horário já está livre para outro agendamento." : "Este atendimento já foi encerrado e não pode ser alterado."}
        </p>
      ) : !writable ? (
        <p className="rounded-xl bg-canvas px-3.5 py-3 text-[14px] text-[#45544d]">{readOnlyReason}</p>
      ) : mode === "move" ? (
        <>
          <h4 className="mb-2 flex items-center gap-1.5 text-[14px] font-semibold">
            <Clock3 className="size-4" aria-hidden /> Novo horário em {fmtDayLong(day)}
          </h4>
          {moveSlots.isLoading ? (
            <div className="h-24 animate-pulse rounded-xl bg-canvas" aria-label="Carregando horários" />
          ) : (
            <SlotChips
              slots={moveSlots.data ?? []}
              zone={zone}
              selected={target?.starts_at}
              onPick={setTarget}
              empty="Nenhum horário livre com essa duração neste dia. Escolha outro dia na agenda e abra o agendamento de novo."
            />
          )}
          <div className="mt-4 flex flex-wrap gap-2">
            <Button onClick={doMove} disabled={!target || busy} className="flex-1">
              {reschedule.isPending ? <Loader2 className="animate-spin" aria-hidden /> : <ArrowRightLeft aria-hidden />}
              {target ? `Mover para ${hhmm(localMinutes(target.starts_at, zone))}` : "Escolha o horário"}
            </Button>
            <Button variant="outline" onClick={() => (setMode("view"), setTarget(null))}>
              Voltar
            </Button>
          </div>
        </>
      ) : mode === "cancel" ? (
        <div className="rounded-xl border border-[#e7b9ad] bg-[#fdf3f0] p-3.5">
          <p className="mb-3 text-[14px] font-medium text-[#8a2f1c]">Cancelar o horário das {hhmm(start)} com {booking.client_ref}? O horário volta a ficar livre.</p>
          <div className="flex flex-wrap gap-2">
            <Button onClick={doCancel} disabled={busy} className="bg-[#b4432c] hover:bg-[#93351f]">
              {cancel.isPending ? <Loader2 className="animate-spin" aria-hidden /> : <CalendarX2 aria-hidden />}
              Confirmar cancelamento
            </Button>
            <Button variant="outline" onClick={() => setMode("view")} disabled={busy}>
              Manter
            </Button>
          </div>
        </div>
      ) : (
        <div className="flex flex-wrap gap-2">
          <Button variant="soft" onClick={() => setMode("move")} className="flex-1">
            <ArrowRightLeft aria-hidden /> Remarcar
          </Button>
          <Button variant="outline" onClick={() => setMode("cancel")} className="flex-1 text-[#a3402b]">
            <CalendarX2 aria-hidden /> Cancelar
          </Button>
        </div>
      )}
    </div>
  );
}
