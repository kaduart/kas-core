"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BffError } from "./api";
import type { AgendaBooking, AgendaCatalog, AgendaSlot, BookingWriteResponse } from "./contract";

/** Leitura/escrita da Agenda pelo BFF (/api/kas/agenda). A credencial do serviço fica no servidor. */

async function call<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, { cache: "no-store", ...init });
  if (res.status === 401) {
    // eslint-disable-next-line @next/next/no-location-assign-relative-destination
    window.location.assign("/login");
    throw new BffError(401, "session_invalid");
  }
  if (!res.ok) {
    const code = ((await res.json().catch(() => ({}))) as { error?: { code?: string } }).error?.code ?? "unknown";
    throw new BffError(res.status, code);
  }
  return res.json();
}

const post = <T,>(url: string, body: unknown) => call<T>(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });

const noRetry4xx = (n: number, e: unknown) => !(e instanceof BffError && e.status < 500) && n < 2;

export function useAgendaCatalog(tenantId: string) {
  return useQuery({ queryKey: ["agenda", tenantId, "catalogo"], queryFn: () => call<AgendaCatalog>("/api/kas/agenda/catalogo"), staleTime: 60_000, retry: noRetry4xx });
}

export function useAgendaBookings(tenantId: string, resourceId: string | undefined, from: string | undefined, to: string | undefined) {
  return useQuery({
    queryKey: ["agenda", tenantId, "agendamentos", resourceId, from, to],
    queryFn: () => call<{ bookings: AgendaBooking[] }>(`/api/kas/agenda/agendamentos?${new URLSearchParams({ recurso: resourceId!, de: from!, ate: to! })}`).then((r) => r.bookings),
    enabled: Boolean(resourceId && from && to),
    placeholderData: (prev) => prev,
    staleTime: 15_000,
    refetchInterval: 60_000,
    retry: noRetry4xx,
  });
}

export function useAgendaSlots(tenantId: string, q: { resourceId?: string; from?: string; to?: string; serviceId?: string; durationMin: number }, enabled: boolean) {
  const p = new URLSearchParams();
  if (q.resourceId) p.set("recurso", q.resourceId);
  if (q.from) p.set("de", q.from);
  if (q.to) p.set("ate", q.to);
  if (q.serviceId) p.set("servico", q.serviceId);
  else p.set("duracao", String(q.durationMin));
  return useQuery({
    queryKey: ["agenda", tenantId, "livres", p.toString()],
    queryFn: () => call<{ slots: AgendaSlot[] }>(`/api/kas/agenda/livres?${p}`).then((r) => r.slots),
    enabled: enabled && Boolean(q.resourceId && q.from && q.to),
    placeholderData: (prev) => prev,
    staleTime: 15_000,
    retry: noRetry4xx,
  });
}

export interface CreateInput {
  chave: string;
  recurso: string;
  cliente: string;
  servico?: string;
  inicio: string;
  fim: string;
  status?: "tentative" | "scheduled" | "confirmed";
}

/** Toda escrita, certa ou errada, recarrega a agenda: depois de um conflito a tela mostra o estado real. */
export function useAgendaWrites(tenantId: string) {
  const qc = useQueryClient();
  const refresh = () => qc.invalidateQueries({ queryKey: ["agenda", tenantId] });
  const create = useMutation({ mutationFn: (b: CreateInput) => post<BookingWriteResponse>("/api/kas/agenda/agendamentos", b), onSettled: refresh });
  const cancel = useMutation({
    mutationFn: (b: { id: string; versao: number }) => post<BookingWriteResponse>(`/api/kas/agenda/agendamentos/${encodeURIComponent(b.id)}/cancelar`, { versao: b.versao }),
    onSettled: refresh,
  });
  const reschedule = useMutation({
    mutationFn: (b: { id: string; versao: number; inicio: string; fim: string }) =>
      post<BookingWriteResponse>(`/api/kas/agenda/agendamentos/${encodeURIComponent(b.id)}/remarcar`, { versao: b.versao, inicio: b.inicio, fim: b.fim }),
    onSettled: refresh,
  });
  return { create, cancel, reschedule };
}

/** Mensagem curta, em português, para cada recusa do Go. */
export function agendaErrorText(e: unknown): string {
  if (!(e instanceof BffError)) return "Algo deu errado. Tente de novo.";
  switch (e.code) {
    case "slot_taken":
      return "Esse horário acabou de ser ocupado. A agenda foi atualizada; escolha outro.";
    case "client_busy":
      return "Este cliente já tem um horário que se sobrepõe a esse.";
    case "version_conflict":
      return "Alguém alterou este agendamento agora há pouco. Recarregamos a versão atual.";
    case "outside_opening_hours":
      return "Fora do horário de atendimento deste profissional.";
    case "agenda_read_only":
      return "Esta agenda vem de outro sistema e só pode ser alterada lá.";
    case "role_forbidden":
      return "Seu papel nesta empresa permite só consultar a agenda.";
    case "idempotency_mismatch":
      return "Este envio já foi usado para outro agendamento. Abra o horário de novo.";
    case "not_found":
      return "Não encontramos este agendamento, profissional ou serviço.";
    case "module_paused":
      return "A Agenda está pausada para esta empresa. Os dados continuam guardados.";
    case "module_not_contracted":
    case "tenant_forbidden":
      return "Esta empresa não tem acesso à Agenda.";
    case "invalid_request":
      return "Os dados enviados não são válidos. Confira o código do cliente e o horário.";
    case "capacity_exceeded":
      return "O servidor está ocupado. Tente de novo em alguns segundos.";
  }
  if (e.status === 502 || e.code === "core_unreachable") return "O KAS Core não respondeu. Tente de novo em alguns minutos.";
  return "Algo deu errado. Tente de novo.";
}
