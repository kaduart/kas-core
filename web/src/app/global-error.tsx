"use client";

/** Erro antes de existir sessão (ex.: domínio sem empresa ou KAS Core fora do ar no modo real). */
export default function GlobalError({ error, reset }: { error: Error & { digest?: string }; reset: () => void }) {
  return (
    <html lang="pt-BR">
      <body style={{ fontFamily: "system-ui, sans-serif", background: "#F3F6F4", color: "#13221B", display: "grid", placeItems: "center", minHeight: "100vh", margin: 0, padding: 16 }}>
        <main style={{ maxWidth: 480, background: "#fff", borderRadius: 22, padding: 24 }}>
          <h1 style={{ fontSize: 20, margin: 0 }}>Não foi possível abrir o painel</h1>
          <p style={{ color: "#5B6B63" }}>Confira se o endereço é o da sua empresa. Se estiver certo, o KAS Core pode estar indisponível: tente de novo em instantes.</p>
          {error.digest ? <code style={{ fontSize: 13, color: "#5B6B63" }}>{error.digest}</code> : null}
          <p><button onClick={reset} style={{ minHeight: 44, borderRadius: 12, border: 0, padding: "0 16px", background: "#0B5D34", color: "#fff", fontWeight: 600 }}>Tentar de novo</button></p>
        </main>
      </body>
    </html>
  );
}
