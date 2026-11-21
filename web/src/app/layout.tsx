import type { Metadata, Viewport } from "next";
import "@fontsource-variable/inter";
import { Providers } from "./providers";
import "./globals.css";

export const metadata: Metadata = {
  title: "KAS Core",
  description: "Plataforma de Gestão e Crescimento",
};

export const viewport: Viewport = { width: "device-width", initialScale: 1, themeColor: "#064023" };

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="pt-BR">
      <body>
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
