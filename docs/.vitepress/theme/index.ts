import type { Theme } from "vitepress/client";
import DefaultTheme from "vitepress/theme";
import AccessibleHome from "./AccessibleHome.vue";
import "./custom.css";

export default {
  extends: DefaultTheme,
  enhanceApp({ app }) {
    app.component("accessible-home", AccessibleHome);
  },
} satisfies Theme;
