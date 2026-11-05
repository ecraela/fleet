import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, useNavigate } from "react-router-dom";
import { RouterProvider } from "react-aria-components";
import { useManagerHref } from "./router";
import { App } from "./app";
import { readTheme } from "./theme";
import "@ecraela/ui/styles.css";
import "./standalone.css";
document.documentElement.dataset.theme = readTheme();
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <BrowserRouter>
      <Routing />
    </BrowserRouter>
  </StrictMode>,
);

function Routing() {
  const navigate = useNavigate();
  return (
    <RouterProvider navigate={navigate} useHref={useManagerHref}>
      <App />
    </RouterProvider>
  );
}
