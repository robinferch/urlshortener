import { StrictMode, lazy } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import "./index.css";
import { AuthProvider, useAuth } from "./auth";
import { ToastProvider } from "./ui";
import Layout from "./pages/Layout";
import Login from "./pages/Login";
import Links from "./pages/Links";

// Chart-heavy and admin pages load on demand to keep the first paint small.
const Dashboard = lazy(() => import("./pages/Dashboard"));
const LinkDetail = lazy(() => import("./pages/LinkDetail"));
const Domains = lazy(() => import("./pages/Domains"));
const Users = lazy(() => import("./pages/Users"));
const Import = lazy(() => import("./pages/Import"));
const Settings = lazy(() => import("./pages/Settings"));

function AppRoutes() {
  const { user } = useAuth();
  if (!user) {
    return (
      <Routes>
        <Route path="*" element={<Login />} />
      </Routes>
    );
  }
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<Dashboard />} />
        <Route path="links" element={<Links />} />
        <Route path="links/:id" element={<LinkDetail />} />
        <Route path="domains" element={<Domains />} />
        {user.isAdmin && <Route path="users" element={<Users />} />}
        {user.isAdmin && <Route path="import" element={<Import />} />}
        <Route path="settings" element={<Settings />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <BrowserRouter>
      <ToastProvider>
        <AuthProvider>
          <AppRoutes />
        </AuthProvider>
      </ToastProvider>
    </BrowserRouter>
  </StrictMode>,
);
