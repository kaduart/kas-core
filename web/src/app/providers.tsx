"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Toaster } from "sonner";

export function Providers({ children }: { children: ReactNode }) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { refetchOnWindowFocus: false } } }));
  return (
    <QueryClientProvider client={client}>
      {children}
      <Toaster position="bottom-center" toastOptions={{ style: { background: "var(--brand-deep)", color: "#fff", border: "0", borderRadius: 14 } }} />
    </QueryClientProvider>
  );
}
