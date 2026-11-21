"use client";

import { useActionState, useState } from "react";
import { CircleAlert, Eye, EyeOff, LoaderCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { loginAction, type LoginState } from "./actions";

export function LoginForm({ next }: { next?: string }) {
  const [state, action, pending] = useActionState<LoginState, FormData>(loginAction, {});
  const [show, setShow] = useState(false);

  return (
    <form action={action} className="mt-7 flex flex-col gap-4" noValidate={false}>
      <input type="hidden" name="next" value={next ?? ""} />
      <label className="flex flex-col gap-1.5">
        <span className="text-[14px] font-semibold">E-mail</span>
        <Input
          name="email"
          type="email"
          inputMode="email"
          autoComplete="username"
          autoCapitalize="none"
          spellCheck={false}
          required
          defaultValue={state.email}
          aria-invalid={state.error ? true : undefined}
          className="min-h-12 bg-white text-base"
        />
      </label>
      <label className="flex flex-col gap-1.5">
        <span className="text-[14px] font-semibold">Senha</span>
        <span className="relative">
          <Input
            name="password"
            type={show ? "text" : "password"}
            autoComplete="current-password"
            required
            aria-invalid={state.error ? true : undefined}
            className="min-h-12 bg-white pr-12 text-base"
          />
          <button
            type="button"
            onClick={() => setShow((v) => !v)}
            aria-label={show ? "Esconder senha" : "Mostrar senha"}
            aria-pressed={show}
            className="absolute inset-y-0 right-0 grid w-12 place-items-center rounded-r-xl text-muted hover:text-ink"
          >
            {show ? <EyeOff className="size-5" aria-hidden /> : <Eye className="size-5" aria-hidden />}
          </button>
        </span>
      </label>

      <div aria-live="polite">
        {state.error ? (
          <p role="alert" className="flex items-start gap-2 rounded-xl bg-[#f8e3dd] px-3.5 py-3 text-[14px] font-medium text-[#8f3420]">
            <CircleAlert className="mt-px size-[18px] shrink-0" aria-hidden />
            {state.error}
          </p>
        ) : null}
      </div>

      <Button type="submit" size="lg" disabled={pending} className="min-h-[52px] w-full text-base">
        {pending ? <LoaderCircle className="animate-spin" aria-hidden /> : null}
        {pending ? "Entrando…" : "Entrar"}
      </Button>
    </form>
  );
}
