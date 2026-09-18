(() => {
  const bindToggle = (selector, toggleId, siblingId) => {
    const control = document.querySelector(selector);
    const toggle = document.querySelector(toggleId);
    const sibling = document.querySelector(siblingId);
    if (!control || !toggle) return;

    const syncState = () => {
      control.setAttribute("aria-expanded", String(toggle.checked));
      control.setAttribute(
        "aria-label",
        toggle.checked ? control.dataset.coxLabelClose : control.dataset.coxLabelOpen,
      );
    };

    control.addEventListener("click", () => {
      if (sibling?.checked) sibling.click();
      toggle.click();
    });
    toggle.addEventListener("change", syncState);
    syncState();
  };

  bindToggle('.cox-control-enhanced[data-cox-control="navigation"]', "#__drawer", "#__search");
  bindToggle('.cox-control-enhanced[data-cox-control="search"]', "#__search", "#__drawer");
  document.documentElement.classList.add("cox-js-ready");

  const searchDialog = document.querySelector('.md-search[role="dialog"]');
  if (searchDialog) searchDialog.setAttribute("aria-label", "Search documentation");
})();
