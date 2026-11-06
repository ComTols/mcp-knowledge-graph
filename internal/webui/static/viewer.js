"use strict";

(async () => {
  const out = document.getElementById("doc");
  const id = new URLSearchParams(location.search).get("id");

  function fail(text) {
    out.replaceChildren();
    const p = document.createElement("p");
    p.className = "error";
    p.textContent = text;
    out.append(p);
  }

  if (!id) {
    fail("Keine Dokument-ID angegeben (?id=…).");
    return;
  }

  let md;
  try {
    const res = await fetch("../graph/fulltext?id=" + encodeURIComponent(id), {
      credentials: "same-origin",
      cache: "no-store",
    });
    if (res.status === 404) throw new Error("Dokument nicht gefunden");
    if (!res.ok) throw new Error("HTTP " + res.status);
    md = await res.text();
  } catch (err) {
    fail("Dokument konnte nicht geladen werden: " + err.message);
    return;
  }

  // Document content is untrusted: always sanitize the rendered HTML.
  out.innerHTML = DOMPurify.sanitize(marked.parse(md, { gfm: true }));

  const h1 = out.querySelector("h1");
  if (h1) document.title = h1.textContent + " – Knowledge Graph";

  // External links open in a new tab without leaking the opener.
  for (const a of out.querySelectorAll("a[href]")) {
    const url = new URL(a.getAttribute("href"), location.href);
    if (url.origin !== location.origin) {
      a.target = "_blank";
      a.rel = "noopener noreferrer";
    }
  }
})();
