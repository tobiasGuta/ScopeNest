export function initNavigation() {
  const tabs = document.querySelectorAll(".tab-bar .tab");
  const views = document.querySelectorAll("main > .view");

  // Determine if we are in the popup (toolbar) or side panel.
  // The popup typically has a small width or is opened via browser action.
  const isPopup = !window.matchMedia("(min-width: 600px)").matches && window.name !== "side-panel";
  
  if (isPopup) {
    document.querySelector(".tab-bar").style.display = "none";
    
    // Add event listener to open the side panel
    document.querySelector("#open-side-panel").addEventListener("click", () => {
      chrome.windows.getCurrent((win) => {
        chrome.sidePanel.open({ windowId: win.id }).then(() => {
          window.close(); // Close popup
        }).catch((err) => {
          console.error("Failed to open side panel:", err);
        });
      });
    });
  } else {
    document.querySelector("#open-side-panel").style.display = "none";
  }

  function switchTab(tabId) {
    if (isPopup && tabId !== "containers") {
      // In popup, we can only see containers
      return;
    }
    
    tabs.forEach(t => {
      const active = t.dataset.tab === tabId;
      t.classList.toggle("active", active);
      t.setAttribute("aria-selected", active);
    });

    views.forEach(v => {
      v.hidden = v.id !== `view-${tabId}`;
    });
  }

  tabs.forEach(t => {
    t.addEventListener("click", () => switchTab(t.dataset.tab));
  });

  // Default
  switchTab("containers");
}
