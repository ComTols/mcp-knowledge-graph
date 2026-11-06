"use strict";

(async () => {
  const $ = (id) => document.getElementById(id);
  const msg = $("msg");
  const tip = $("tip");

  function say(text) {
    msg.textContent = text;
    msg.hidden = !text;
  }

  let graph;
  try {
    // Relative URL keeps working behind a path prefix; the browser re-sends
    // the credentials it asked for when opening /ui/.
    const res = await fetch("../graph", { credentials: "same-origin", cache: "no-store" });
    if (!res.ok) throw new Error("HTTP " + res.status);
    graph = await res.json();
  } catch (err) {
    say("Graph konnte nicht geladen werden: " + err.message);
    return;
  }

  // The API expresses the primary parent as data.parent (Cytoscape compound
  // node). The UI wants a plain edge instead, so convert it here.
  const nodes = graph.elements.nodes.map((n) => {
    const { parent, ...data } = n.data;
    return { data, _parent: parent };
  });
  const edges = graph.elements.edges.map((e) => ({ ...e, classes: "secondary-parent" }));
  for (const n of nodes) {
    if (n._parent) {
      edges.push({
        data: { id: n._parent + ">" + n.data.id + ":primary", source: n._parent, target: n.data.id },
        classes: "primary-parent",
      });
    }
  }
  const elements = { nodes: nodes.map((n) => ({ data: n.data })), edges };

  const dark = matchMedia("(prefers-color-scheme: dark)").matches;
  const text = dark ? "#e4e7ee" : "#1d2330";

  const cy = cytoscape({
    container: $("cy"),
    elements,
    wheelSensitivity: 0.3,
    style: [
      { selector: "node", style: {
          "background-color": "#6366f1", label: "data(name)", color: text, "font-size": 11,
          "text-valign": "bottom", "text-margin-y": 4, "text-wrap": "ellipsis", "text-max-width": 120,
          width: 18, height: 18 } },
      { selector: "edge", style: { "curve-style": "bezier", "target-arrow-shape": "triangle", "arrow-scale": 0.8 } },
      { selector: "edge.primary-parent", style: {
          "line-color": "#3b82f6", "target-arrow-color": "#3b82f6", width: 2.5 } },
      { selector: "edge.secondary-parent", style: {
          "line-color": "#f59e0b", "target-arrow-color": "#f59e0b", width: 1.2, opacity: 0.45,
          "line-style": "dashed" } },
      { selector: "node.hl", style: { "background-color": "#ef4444", "z-index": 10 } },
      { selector: "edge.hl", style: { opacity: 1, width: 3 } },
      { selector: ".dim", style: { opacity: 0.15 } },
    ],
  });

  $("stats").textContent = nodes.length + " Dokumente, " + edges.length + " Verbindungen";

  function layout() {
    const name = $("layout").value;
    const opts = { name, animate: false, fit: true, padding: 30 };
    if (name === "breadthfirst") {
      // roots = nodes without a primary parent
      opts.roots = cy.nodes().filter((n) => n.incomers("edge.primary-parent").empty());
      opts.directed = true;
      opts.spacingFactor = 1.1;
    }
    if (name === "cose") opts.nodeRepulsion = () => 9000;
    cy.layout(opts).run();
  }
  $("layout").addEventListener("change", layout);
  $("fit").addEventListener("click", () => cy.fit(undefined, 30));
  layout();

  cy.on("mouseover", "node", (ev) => {
    const n = ev.target;
    const hood = n.closedNeighborhood();
    cy.elements().addClass("dim");
    hood.removeClass("dim");
    n.addClass("hl");
    n.connectedEdges().addClass("hl");

    // textContent only: titles and summaries are untrusted data.
    tip.replaceChildren();
    const title = document.createElement("b");
    title.textContent = n.data("name");
    const body = document.createElement("p");
    body.textContent = n.data("summary") || "";
    tip.append(title, body);
    tip.hidden = false;
    move(ev.originalEvent);
  });
  cy.on("mouseout", "node", () => {
    cy.elements().removeClass("dim hl");
    tip.hidden = true;
  });
  cy.on("tap", "node", (ev) => {
    location.href = "viewer?id=" + encodeURIComponent(ev.target.id());
  });
  cy.on("mouseover", "node", () => { $("cy").style.cursor = "pointer"; });
  cy.on("mouseout", "node", () => { $("cy").style.cursor = ""; });
  cy.on("mousemove", "node", (ev) => move(ev.originalEvent));
  cy.on("pan zoom drag", () => { tip.hidden = true; });

  function move(e) {
    if (!e) return;
    const pad = 14;
    const w = tip.offsetWidth, h = tip.offsetHeight;
    const x = Math.min(e.clientX + pad, innerWidth - w - 8);
    const y = e.clientY + pad + h > innerHeight ? e.clientY - h - pad : e.clientY + pad;
    tip.style.left = Math.max(8, x) + "px";
    tip.style.top = Math.max(8, y) + "px";
  }
})();
