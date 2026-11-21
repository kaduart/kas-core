"use client";

import { useCallback, useMemo, useState } from "react";
import { CalendarDays, ChevronLeft, ChevronRight, Lock, PlugZap } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { PageHeader } from "@/components/ui/page-header";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/cn";
import { ACTIVE_STATUSES, STATUS_LABEL, WRITER_ROLES, type AgendaBooking, type AgendaCatalog, type AgendaSlot } from "@/lib/kas/contract";
import { addDays, dayNumber, fmtDayLong, fmtDayMonth, fmtWeekday, hhmm, localDate, localMinutes, weekDays, zonedInstant } from "@/lib/kas/agenda-time";
import { agendaErrorText, useAgendaBookings, useAgendaCatalog, useAgendaSlots } from "@/lib/kas/agenda";
import { useTenant } from "@/lib/tenant/TenantProvider";
import { CalendarGrid, STATUS_STYLE } from "./CalendarGrid";
import { hourRange, placeBookings } from "./layout";
import { SidePanel, type Selection } from "./SidePanel";

const DEFAULT_DURATION = 40;
const field = "min-h-11 rounded-xl border border-[#d5ddd8] bg-white px-3 text-[14.5px] text-ink";

export function AgendaView() {
  const { mode } = useTenant();
  return mode === "live" ? <AgendaLive /> : <AgendaNeedsCore />;
}

/** No modo demonstração não há API: a Agenda não inventa horários. */
function AgendaNeedsCore() {
  return (
    <>
      <PageHeader icon={CalendarDays} title="Agenda" description="Horários por profissional, sala ou equipamento." />
      <Card className="max-w-2xl">
        <span className="mb-3 grid size-12 place-items-center rounded-2xl bg-brand-tint text-brand-dark">
          <PlugZap className="size-6" aria-hidden />
        </span>
        <h2 className="text-xl font-bold">A Agenda funciona só ligada ao KAS Core</h2>
        <p className="mt-1 text-muted">
          Para não mostrar horários inventados, esta tela não tem modo demonstração. Rode o KAS Core local com a empresa fictícia
          <b className="font-semibold text-ink"> clinica-aurora</b> e entre com um usuário dela.
        </p>
      </Card>
    </>
  );
}

function AgendaLive() {
  const { tenant } = useTenant();
  const catalog = useAgendaCatalog(tenant.id);

  if (catalog.isError) {
    return (
      <>
        <PageHeader icon={CalendarDays} title="Agenda" description="Horários por profissional, sala ou equipamento." />
        <Card role="alert" className="max-w-2xl">
          <p className="font-semibold">{agendaErrorText(catalog.error)}</p>
          <Button className="mt-3" onClick={() => catalog.refetch()}>
            Tentar de novo
          </Button>
        </Card>
      </>
    );
  }
  if (!catalog.data) {
    return (
      <>
        <PageHeader icon={CalendarDays} title="Agenda" description="Carregando profissionais e serviços…" />
        <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_360px]" aria-busy="true" aria-label="Carregando a agenda">
          <div className="h-[560px] animate-pulse rounded-[22px] bg-white" />
          <div className="h-72 animate-pulse rounded-[22px] bg-white" />
        </div>
      </>
    );
  }
  if (catalog.data.resources.filter((r) => r.active).length === 0) {
    return (
      <>
        <PageHeader icon={CalendarDays} title="Agenda" description="Horários por profissional, sala ou equipamento." />
        <Card className="max-w-2xl">
          <h2 className="text-xl font-bold">Nenhum profissional ou sala cadastrado</h2>
          <p className="mt-1 text-muted">Cadastre ao menos um recurso com horário de atendimento no KAS Core para a agenda aparecer aqui.</p>
        </Card>
      </>
    );
  }
  return <AgendaBoard catalog={catalog.data} />;
}

function AgendaBoard({ catalog }: { catalog: AgendaCatalog }) {
  const { tenant, user } = useTenant();
  const zone = catalog.zone;
  const resources = catalog.resources.filter((r) => r.active);
  const [view, setView] = useState<"dia" | "semana">("dia");
  const [day, setDay] = useState(() => localDate(new Date(), zone));
  const [resourceId, setResourceId] = useState(resources[0].id);
  const [serviceId, setServiceId] = useState("");
  const [picked, setSelection] = useState<Selection>(null);
  const resource = resources.find((r) => r.id === resourceId) ?? resources[0];
  const service = catalog.services.find((s) => s.id === serviceId);

  const roleWrites = user.roleKey ? WRITER_ROLES.includes(user.roleKey) : false;
  const writable = !catalog.read_only && roleWrites;
  const readOnlyReason = catalog.read_only
    ? "Esta agenda é espelhada de outro sistema. Para marcar, remarcar ou cancelar, use o sistema de origem."
    : "Seu papel nesta empresa é de consulta. Peça a um administrador ou à recepção para alterar a agenda.";

  const days = view === "dia" ? [day] : weekDays(day);
  const from = days[0];
  const to = days[days.length - 1];
  const bookings = useAgendaBookings(tenant.id, resource.id, zonedInstant(from, 0, zone).toISOString(), zonedInstant(addDays(to, 1), 0, zone).toISOString());
  const slots = useAgendaSlots(tenant.id, { resourceId: resource.id, from, to, serviceId: service?.id, durationMin: DEFAULT_DURATION }, true);

  // Uma chave por tentativa de agendamento: reenviar o mesmo formulário não duplica.
  const newKey = useCallback(() => `web-${crypto.randomUUID()}`, []);

  const all = useMemo(() => bookings.data ?? [], [bookings.data]);
  // O agendamento aberto acompanha a versão mais nova que a agenda trouxe.
  const selection: Selection = useMemo(() => {
    if (picked?.kind !== "booking") return picked;
    const fresh = all.find((b) => b.id === picked.booking.id);
    return fresh && fresh.version > picked.booking.version ? { kind: "booking", booking: fresh } : picked;
  }, [all, picked]);

  const visible = all.filter((b) => ACTIVE_STATUSES.includes(b.status) || (selection?.kind === "booking" && selection.booking.id === b.id));
  const canceledCount = all.filter((b) => b.status === "canceled").length;
  const slotList = slots.data ?? [];
  const range = hourRange(visible, slotList, zone);
  const placed = placeBookings(visible, zone);
  const daySlots = slotList.filter((s) => localDate(new Date(s.starts_at), zone) === day);
  const dayBookings = all.filter((b) => localDate(new Date(b.starts_at), zone) === day);
  const today = localDate(new Date(), zone);

  const step = view === "dia" ? 1 : 7;
  const label = view === "dia" ? fmtDayLong(day) : `${fmtDayMonth(from)} – ${fmtDayMonth(to)}`;

  const pickSlot = (s: AgendaSlot) => {
    setDay(localDate(new Date(s.starts_at), zone));
    setSelection({ kind: "slot", slot: s, key: newKey() });
  };
  const pickBooking = (b: AgendaBooking) => {
    setDay(localDate(new Date(b.starts_at), zone));
    setSelection({ kind: "booking", booking: b });
  };

  return (
    <>
      <PageHeader
        icon={CalendarDays}
        title="Agenda"
        description={`${resource.label} · ${label}`}
        actions={
          <Tabs value={view} onValueChange={(v) => (setView(v as "dia" | "semana"), setSelection(null))}>
            <TabsList aria-label="Visualização">
              <TabsTrigger value="dia">Dia</TabsTrigger>
              <TabsTrigger value="semana">Semana</TabsTrigger>
            </TabsList>
          </Tabs>
        }
      />

      {!writable ? (
        <div role="status" className="mb-4 flex items-start gap-3 rounded-2xl border border-[#c9d3cd] bg-white/70 px-4 py-3 text-[14px] text-[#45544d]">
          <Lock className="mt-0.5 size-4 shrink-0" aria-hidden />
          <span>
            <b className="font-semibold text-ink">Somente leitura.</b> {readOnlyReason}
          </span>
        </div>
      ) : null}

      <div className="mb-4 flex flex-wrap items-center gap-2">
        <label className="sr-only" htmlFor="agenda-recurso">
          Profissional ou sala
        </label>
        <select id="agenda-recurso" value={resource.id} onChange={(e) => (setResourceId(e.target.value), setSelection(null))} className={cn(field, "min-w-0 basis-full font-semibold sm:basis-auto sm:flex-1 sm:max-w-[280px]")}>
          {resources.map((r) => (
            <option key={r.id} value={r.id}>
              {r.label}
            </option>
          ))}
        </select>
        <label className="sr-only" htmlFor="agenda-servico">
          Serviço
        </label>
        <select id="agenda-servico" value={serviceId} onChange={(e) => (setServiceId(e.target.value), setSelection(null))} className={cn(field, "min-w-0 basis-full sm:basis-auto sm:flex-1 sm:max-w-[260px]")}>
          <option value="">Horário de {DEFAULT_DURATION} min</option>
          {catalog.services.map((s) => (
            <option key={s.id} value={s.id}>
              {s.label} · {s.duration_min} min
            </option>
          ))}
        </select>
        <div className="flex w-full items-center gap-1.5 sm:ml-auto sm:w-auto">
          <Button variant="outline" size="icon" onClick={() => setDay(addDays(day, -step))} aria-label={view === "dia" ? "Dia anterior" : "Semana anterior"}>
            <ChevronLeft aria-hidden />
          </Button>
          <Button variant="outline" className="flex-1 sm:flex-none" onClick={() => setDay(today)} disabled={days.includes(today)}>
            Hoje
          </Button>
          <Button variant="outline" size="icon" onClick={() => setDay(addDays(day, step))} aria-label={view === "dia" ? "Próximo dia" : "Próxima semana"}>
            <ChevronRight aria-hidden />
          </Button>
        </div>
      </div>

      <div className="grid items-start gap-4 xl:grid-cols-[minmax(0,1fr)_360px]">
        <Card className="min-w-0 px-3 py-4 md:px-5">
          {bookings.isError || slots.isError ? (
            <div role="alert" className="mb-3 flex flex-wrap items-center gap-3 rounded-xl bg-[#f8e3dd] px-3.5 py-2.5 text-[14px] text-[#8a2f1c]">
              <span className="font-medium">{agendaErrorText(bookings.error ?? slots.error)}</span>
              <Button size="sm" variant="outline" className="ml-auto" onClick={() => (bookings.refetch(), slots.refetch())}>
                Tentar de novo
              </Button>
            </div>
          ) : null}

          <div className="mb-3 flex flex-wrap items-center gap-x-4 gap-y-1.5 text-[12.5px] text-muted" aria-label="Legenda">
            {(["scheduled", "confirmed", "tentative"] as const).map((s) => (
              <span key={s} className="flex items-center gap-1.5">
                <span className={cn("size-3 rounded-[4px] border", STATUS_STYLE[s])} aria-hidden />
                {STATUS_LABEL[s]}
              </span>
            ))}
            <span className="flex items-center gap-1.5">
              <span className="size-3 rounded-[4px] border border-dashed border-brand/50" aria-hidden />
              Livre
            </span>
            {canceledCount ? <span className="ml-auto tabular-nums">{canceledCount} cancelado{canceledCount > 1 ? "s" : ""} oculto{canceledCount > 1 ? "s" : ""}</span> : null}
            {bookings.isFetching || slots.isFetching ? <span className="ml-auto animate-pulse">Atualizando…</span> : null}
          </div>

          {view === "semana" ? (
            <>
              <div className="hidden md:block">
                <CalendarGrid
                  days={days}
                  zone={zone}
                  range={range}
                  bookings={placed}
                  slots={slotList}
                  slotsInteractive={writable}
                  selectedBookingId={selection?.kind === "booking" ? selection.booking.id : undefined}
                  selectedSlotStart={selection?.kind === "slot" ? selection.slot.starts_at : undefined}
                  onPickSlot={pickSlot}
                  onPickBooking={pickBooking}
                  onPickDay={(d) => (setDay(d), setView("dia"))}
                  loading={bookings.isFetching}
                />
              </div>
              <WeekList days={days} zone={zone} today={today} bookings={visible} slots={slotList} onPickDay={(d) => (setDay(d), setView("dia"))} onPickBooking={pickBooking} />
            </>
          ) : visible.length === 0 && slotList.length === 0 && !bookings.isLoading && !slots.isLoading ? (
            <div className="grid place-items-center rounded-2xl bg-canvas px-6 py-14 text-center">
              <CalendarDays className="mb-3 size-8 text-muted" strokeWidth={1.6} aria-hidden />
              <p className="text-[16px] font-semibold">Sem atendimento de {resource.label.split(" · ")[0]} neste dia</p>
              <p className="mt-1 max-w-sm text-[14px] text-muted">Não há agendamentos nem horários abertos. Veja o próximo dia ou a semana inteira.</p>
              <div className="mt-4 flex flex-wrap justify-center gap-2">
                <Button variant="soft" onClick={() => setDay(addDays(day, 1))}>
                  Próximo dia <ChevronRight aria-hidden />
                </Button>
                <Button variant="outline" onClick={() => (setView("semana"), setSelection(null))}>
                  Ver a semana
                </Button>
              </div>
            </div>
          ) : (
            <CalendarGrid
              days={days}
              zone={zone}
              range={range}
              bookings={placed}
              slots={slotList}
              slotsInteractive={writable}
              selectedBookingId={selection?.kind === "booking" ? selection.booking.id : undefined}
              selectedSlotStart={selection?.kind === "slot" ? selection.slot.starts_at : undefined}
              onPickSlot={pickSlot}
              onPickBooking={pickBooking}
              loading={bookings.isFetching}
            />
          )}
          {!bookings.data && bookings.isLoading ? <p className="mt-2 text-[13px] text-muted">Carregando agendamentos…</p> : null}
        </Card>

        <SidePanel
          tenantId={tenant.id}
          zone={zone}
          day={day}
          resource={resource}
          service={service}
          writable={writable}
          readOnlyReason={readOnlyReason}
          daySlots={daySlots}
          dayBookings={dayBookings}
          slotsLoading={slots.isLoading}
          selection={selection}
          setSelection={setSelection}
          newKey={newKey}
        />
      </div>
    </>
  );
}

/** Semana no celular: um bloco por dia, sem rolagem lateral. */
function WeekList({
  days,
  zone,
  today,
  bookings,
  slots,
  onPickDay,
  onPickBooking,
}: {
  days: string[];
  zone: string;
  today: string;
  bookings: AgendaBooking[];
  slots: AgendaSlot[];
  onPickDay: (d: string) => void;
  onPickBooking: (b: AgendaBooking) => void;
}) {
  return (
    <ol className="flex flex-col divide-y divide-line md:hidden">
      {days.map((d) => {
        const list = bookings.filter((b) => localDate(new Date(b.starts_at), zone) === d).sort((a, b) => a.starts_at.localeCompare(b.starts_at));
        const free = slots.filter((s) => localDate(new Date(s.starts_at), zone) === d).length;
        return (
          <li key={d} className="flex gap-3 py-3">
            <button type="button" onClick={() => onPickDay(d)} className="flex w-12 shrink-0 flex-col items-center rounded-xl py-1 hover:bg-canvas" aria-label={`Abrir ${fmtDayLong(d)}`}>
              <span className="text-[12px] font-semibold uppercase text-muted">{fmtWeekday(d)}</span>
              <span className={cn("grid size-8 place-items-center rounded-full text-[15px] font-bold tabular-nums", d === today && "bg-brand text-white")}>{dayNumber(d)}</span>
            </button>
            <div className="min-w-0 flex-1">
              {list.length ? (
                <ul className="flex flex-col gap-1.5">
                  {list.map((b) => (
                    <li key={b.id}>
                      <button type="button" onClick={() => onPickBooking(b)} className={cn("flex w-full items-center gap-2 rounded-lg border px-2.5 py-1.5 text-left text-[13px]", STATUS_STYLE[b.status])}>
                        <b className="tabular-nums">{hhmm(localMinutes(b.starts_at, zone))}</b>
                        <span className="truncate">{b.client_ref}</span>
                      </button>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="pt-1.5 text-[13.5px] text-muted">Sem agendamentos</p>
              )}
              <button type="button" onClick={() => onPickDay(d)} className="mt-1.5 text-[13px] font-semibold text-brand-ink underline-offset-4 hover:underline">
                {free} {free === 1 ? "horário livre" : "horários livres"}
              </button>
            </div>
          </li>
        );
      })}
    </ol>
  );
}
