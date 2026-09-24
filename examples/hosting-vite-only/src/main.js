document.querySelector('#app').textContent =
  `vite-only-v1:${import.meta.env.VITE_BUILD_MARKER || 'unset'}`
