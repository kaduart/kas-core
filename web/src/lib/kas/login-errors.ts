import { CoreError } from "./core-client";

/** Mensagens em português simples. Nunca diz se o e-mail existe. */
export function loginErrorMessage(e: unknown): string {
  if (e instanceof CoreError) {
    if (e.code === "invalid_credentials" || e.code === "invalid_request") return "E-mail ou senha não conferem. Confira e tente de novo.";
    if (e.code === "login_locked") return "Muitas tentativas seguidas. Por segurança, o acesso fica travado por 15 minutos.";
    if (e.code === "no_tenant_access") return "Seu usuário ainda não tem acesso a nenhuma empresa neste painel. Fale com o administrador.";
  }
  return "O servidor de acesso não respondeu. Tente de novo em alguns minutos.";
}

/** Só caminhos internos do painel; nada de redirecionar para outro site. */
export function safeNext(raw: unknown): string {
  const v = typeof raw === "string" ? raw : "";
  return v.startsWith("/") && !v.startsWith("//") && !v.includes("\\") && !v.startsWith("/login") ? v : "/dashboard";
}

