(() => {
  const form = document.getElementById("syncForm");
  if (!form || !window.ReadableStream) return;

  const panel = document.getElementById("syncProgress");
  const message = document.getElementById("progressMessage");
  const count = document.getElementById("progressCount");
  const bar = document.getElementById("progressBar");
  const items = document.getElementById("liveItems");
  const button = form.querySelector("button[type=submit]");
  const announcementCount = document.getElementById("announcementCount");
  const positionCount = document.getElementById("positionCount");

  const setStatus = (event) => {
    if (event.Message) message.textContent = event.Message;
    if (event.Total > 0) {
      bar.max = event.Total;
      bar.value = Math.min(event.Current || 0, event.Total);
      count.textContent = String(event.Current || 0) + " / " + String(event.Total);
    } else if (event.Page > 0) {
      bar.removeAttribute("value");
      count.textContent = "列表第 " + String(event.Page) + " 页";
    }
  };

  const addItem = (event) => {
    const row = document.createElement("li");
    const company = document.createElement("strong");
    const positions = document.createElement("span");
    company.textContent = event.Company;
    positions.textContent = (event.Positions || []).join(" · ") || "岗位见公告正文";
    row.append(company, positions);
    items.prepend(row);
    while (items.children.length > 20) items.lastElementChild.remove();

    if (event.Inserted > 0) {
      announcementCount.textContent = String(Number(announcementCount.textContent) + 1);
      positionCount.textContent = String(Number(positionCount.textContent) + (event.Positions || []).length);
    }
  };

  const refreshURL = () => {
    const params = new URLSearchParams(new FormData(document.getElementById("profileForm")));
    const syncData = new FormData(form);
    params.set("published_since", syncData.get("published_since") || "");
    params.set("published_until", syncData.get("published_until") || "");
    params.set("keyword", syncData.get("keyword") || "");
    params.set("force_refresh", syncData.get("force_refresh") || "");
    return "/?" + params.toString();
  };

  form.addEventListener("submit", async (submitEvent) => {
    submitEvent.preventDefault();
    panel.hidden = false;
    panel.classList.remove("has-error");
    items.replaceChildren();
    button.disabled = true;
    button.textContent = "正在同步";
    message.textContent = "正在连接";
    count.textContent = "";
    bar.removeAttribute("value");

    try {
      const response = await fetch("/sync/stream", {
        method: "POST",
        body: new URLSearchParams(new FormData(form)),
        headers: { Accept: "application/x-ndjson" },
      });
      if (!response.ok || !response.body) {
        throw new Error("服务器返回 " + String(response.status));
      }

      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      let complete = false;
      for (;;) {
        const chunk = await reader.read();
        buffer += decoder.decode(chunk.value || new Uint8Array(), { stream: !chunk.done });
        const lines = buffer.split("\n");
        buffer = lines.pop() || "";
        for (const line of lines) {
          if (!line.trim()) continue;
          const event = JSON.parse(line);
          setStatus(event);
          if (event.Type === "item") addItem(event);
          if (event.Type === "error") throw new Error(event.Message);
          if (event.Type === "complete") complete = true;
        }
        if (chunk.done) break;
      }
      if (!complete) throw new Error("同步连接提前结束");
      message.textContent += "，正在刷新结果";
      window.setTimeout(() => window.location.assign(refreshURL()), 700);
    } catch (error) {
      message.textContent = error instanceof Error ? error.message : "同步失败";
      panel.classList.add("has-error");
      button.disabled = false;
      button.textContent = "重新同步";
    }
  });
})();
