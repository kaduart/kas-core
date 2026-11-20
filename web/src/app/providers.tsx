"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { TenantProvider } from "@/lib/tenant/TenantProvider";
import type { TenantBrand } from "@/lib/tenant/types";

export function Providers({ initialTenantId, initialBrands, children }: { initialTenantId: string; initialBrands: Record<string, TenantBrand>; children: ReactNode }) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { refetchOnWindowFocus: false } } }));
  return (
    <QueryClientProvider client={client}>
      <TenantProvider initialTenantId={initialTenantId} initialBrands={initialBrands}>{children}</TenantProvider>
    </QueryClientProvider>
  );
}
