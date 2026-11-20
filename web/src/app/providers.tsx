"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Toaster } from "sonner";
import type { Session } from "@/lib/session";
import { TenantProvider } from "@/lib/tenant/TenantProvider";

export function Providers({ session, children }: { session: Session; children: ReactNode }) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { refetchOnWindowFocus: false } } }));
  return (
    <QueryClientProvider client={client}>
      <TenantProvider session={session}>
        {children}
        <Toaster position="bottom-center" toastOptions={{ style: { background: "var(--brand-deep)", color: "#fff", border: "0", borderRadius: 14 } }} />
      </TenantProvider>
    </QueryClientProvider>
  );
}
