import { defineConfig } from "vitepress";

export default defineConfig({
  lang: "en-US",
  title: "Rig",
  description: "Local-first Docker Compose deployment management.",
  base: "/rig/",
  ignoreDeadLinks: false,
  markdown: {
    config(markdown) {
      const renderLinkOpen = markdown.renderer.rules.link_open;
      markdown.renderer.rules.link_open = (tokens, index, options, environment, self) => {
        const token = tokens[index];
        if (token.attrGet("href") === "../TASKS.md#external-promotion-gates") {
          token.attrSet(
            "href",
            "https://github.com/tyhuang9/rig/blob/main/TASKS.md#external-promotion-gates",
          );
        }
        return renderLinkOpen
          ? renderLinkOpen(tokens, index, options, environment, self)
          : self.renderToken(tokens, index, options);
      };
    },
  },
  themeConfig: {
    nav: [
      { text: "Home", link: "/" },
      { text: "Getting started", link: "/getting-started" },
      { text: "Connect GitHub", link: "/connect-github" },
      {
        text: "Operations",
        items: [
          { text: "Docker Compose runtime", link: "/compose-runtime" },
          { text: "GitHub deployments", link: "/github-connected-deployments" },
          { text: "Webhook relay", link: "/relay-operations" },
        ],
      },
    ],
    sidebar: [
      { text: "Overview", link: "/" },
      { text: "Getting started", link: "/getting-started" },
      { text: "Connect GitHub", link: "/connect-github" },
      { text: "Docker Compose runtime", link: "/compose-runtime" },
      { text: "GitHub-connected deployments", link: "/github-connected-deployments" },
      { text: "Official webhook relay", link: "/relay-operations" },
    ],
    socialLinks: [
      { icon: "github", link: "https://github.com/tyhuang9/rig" },
    ],
    editLink: {
      pattern: "https://github.com/tyhuang9/rig/edit/main/docs/:path",
      text: "Edit this page on GitHub",
    },
    outline: {
      level: [2, 3],
      label: "On this page",
    },
    footer: {
      message: "Rig is under active development. Verify production behavior in your environment.",
    },
  },
});
