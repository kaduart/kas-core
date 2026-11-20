"use client";

import { useState, type FormEvent } from "react";
import { ArrowLeft, AtSign, Bot, Globe, MessageCircle, MessageSquareText, Send } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { PageHeader } from "@/components/ui/page-header";
import { cn } from "@/lib/cn";
import { CONVERSATIONS, type Conversation } from "@/lib/kas/modules-fixtures";

const CH_ICON = { WhatsApp: MessageCircle, Instagram: AtSign, Site: Globe } as const;

export function OmnichannelView() {
  const [convs, setConvs] = useState<Conversation[]>(CONVERSATIONS);
  const [openId, setOpenId] = useState<string | null>(null);
  const [desktopId, setDesktopId] = useState(CONVERSATIONS[0].id);
  const [text, setText] = useState("");
  const current = convs.find((c) => c.id === (openId ?? desktopId)) ?? convs[0];
  const waiting = convs.filter((c) => c.waiting).length;

  const select = (id: string) => {
    setOpenId(id);
    setDesktopId(id);
    setConvs((all) => all.map((c) => (c.id === id ? { ...c, unread: 0 } : c)));
  };

  const send = (e: FormEvent) => {
    e.preventDefault();
    const t = text.trim();
    if (!t) return;
    const at = new Date().toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
    setConvs((all) => all.map((c) => (c.id === current.id ? { ...c, waiting: undefined, messages: [...c.messages, { from: "team", text: t, at }] } : c)));
    setText("");
  };

  return (
    <>
      <PageHeader
        icon={MessageSquareText}
        title="Omnichannel"
        description="WhatsApp, Instagram e site numa caixa só."
        actions={waiting ? <Badge tone="danger">{waiting} sem resposta</Badge> : <Badge>Todos respondidos</Badge>}
      />
      <div className="grid overflow-hidden rounded-[22px] bg-white shadow-[0_1px_3px_rgba(15,31,23,.05)] md:h-[calc(100dvh-230px)] md:min-h-[520px] md:grid-cols-[340px_1fr]">
        <ul className={cn("border-line md:border-r md:block overflow-y-auto", openId && "hidden")} aria-label="Conversas">
          {convs.map((c) => {
            const Icon = CH_ICON[c.channel];
            const last = c.messages[c.messages.length - 1];
            const active = c.id === current.id;
            return (
              <li key={c.id}>
                <button
                  onClick={() => select(c.id)}
                  className={cn("flex w-full items-start gap-3 border-b border-line px-4 py-3.5 text-left hover:bg-canvas", active && "md:bg-brand-tint")}
                  aria-current={active ? "true" : undefined}
                >
                  <span className="grid size-10 shrink-0 place-items-center rounded-full bg-brand-tint text-brand-dark">
                    <Icon className="size-5" aria-hidden />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center gap-2">
                      <b className="truncate text-[14.5px]">{c.name}</b>
                      <small className="ml-auto whitespace-nowrap text-[12px] text-muted">{last.at}</small>
                    </span>
                    <span className="block truncate text-[13px] text-[#2a3a33]">{last.text}</span>
                    <span className="mt-1 flex items-center gap-2">
                      {c.waiting ? <Badge tone="danger">esperando {c.waiting}</Badge> : <Badge tone="neutral">{c.channel}</Badge>}
                      {c.unread ? <span className="ml-auto grid size-5 place-items-center rounded-full bg-brand text-[11px] font-bold text-white">{c.unread}</span> : null}
                    </span>
                  </span>
                </button>
              </li>
            );
          })}
        </ul>

        <section className={cn("flex h-[calc(100dvh-170px)] min-h-[420px] flex-col md:h-auto md:min-h-0", !openId && "hidden md:flex")} aria-label={`Conversa com ${current.name}`}>
          <header className="flex items-center gap-2 border-b border-line px-3 py-2.5">
            <Button variant="ghost" size="icon" className="md:hidden" onClick={() => setOpenId(null)} aria-label="Voltar para conversas">
              <ArrowLeft aria-hidden />
            </Button>
            <b className="text-[15px]">{current.name}</b>
            <Badge tone="neutral">{current.channel}</Badge>
          </header>
          <ol className="flex flex-1 flex-col gap-2 overflow-y-auto bg-[#f7f9f8] p-4">
            {current.messages.map((m, i) => (
              <li key={i} className={cn("max-w-[80%] rounded-2xl px-3.5 py-2 text-[14px]", m.from === "client" ? "self-start bg-white" : "self-end bg-brand text-white", m.from === "amanda" && "bg-brand-dark")}>
                {m.from === "amanda" ? (
                  <span className="mb-0.5 flex items-center gap-1 text-[11.5px] font-semibold opacity-85">
                    <Bot className="size-3.5" aria-hidden /> Amanda IA
                  </span>
                ) : null}
                {m.text}
                <small className="ml-2 text-[11px] opacity-70">{m.at}</small>
              </li>
            ))}
          </ol>
          <form onSubmit={send} className="flex gap-2 border-t border-line p-3">
            <Input value={text} onChange={(e) => setText(e.target.value)} placeholder="Escreva a resposta" aria-label="Mensagem" />
            <Button type="submit" size="icon" aria-label="Enviar">
              <Send aria-hidden />
            </Button>
          </form>
        </section>
      </div>
    </>
  );
}
