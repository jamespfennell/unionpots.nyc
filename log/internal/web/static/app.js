// Small progressive enhancements. Pages are swapped by htmx (hx-boost), so
// everything uses event delegation plus an init pass on each htmx:load.
(function () {
  "use strict";


  // Autosave status next to a heading: "Saving…" while there are unsaved
  // edits or a save is in flight, "Saved" briefly once done, and an error if
  // a save fails. Each edit bumps a counter; a save only counts as done if no
  // edits came after it.
  function setSaveState(form, state) {
    var section = form.closest("section") || document;
    var el = section.querySelector("[data-save-status]");
    if (!el) return;
    clearTimeout(el._fade);
    el.dataset.state = state;
    el.textContent = { dirty: "Saving…", saved: "Saved", error: "Couldn’t save. Check your connection." }[state];
    if (state === "saved") {
      el._fade = setTimeout(function () { el.textContent = ""; delete el.dataset.state; }, 2000);
    }
  }

  document.addEventListener("input", function (e) {
    var form = e.target.closest && e.target.closest("form[data-autosave]");
    if (!form) return;
    form._edits = (form._edits || 0) + 1;
    setSaveState(form, "dirty");
  });

  document.addEventListener("htmx:beforeRequest", function (e) {
    var form = e.detail.elt;
    if (form.matches && form.matches("form[data-autosave]")) form._sending = form._edits || 0;
  });

  document.addEventListener("htmx:afterRequest", function (e) {
    var form = e.detail.elt;
    if (!(form.matches && form.matches("form[data-autosave]"))) return;
    if (!e.detail.successful) {
      setSaveState(form, "error");
    } else if (form._sending === (form._edits || 0)) {
      setSaveState(form, "saved");
    }
  });

  // Once other pieces are included, the button says how many: "Mark
  // glazed" becomes "Mark 3 pieces glazed" (from its data-count-label).
  function syncAlsoCount(form) {
    var n = form.querySelectorAll('input[name="also"]:checked').length;
    form.querySelectorAll("[data-count-label]").forEach(function (el) {
      if (el.dataset.oneLabel === undefined) el.dataset.oneLabel = el.textContent;
      el.textContent = n ? el.dataset.countLabel.replace("{n}", n + 1) : el.dataset.oneLabel;
    });
  }

  document.addEventListener("change", function (e) {
    if (e.target.matches && e.target.matches('input[name="also"]')) syncAlsoCount(e.target.form);
  });

  // "How many pieces": typing a number selects "custom"; tapping 1–4 clears it.
  document.addEventListener("input", function (e) {
    if (!(e.target.matches && e.target.matches('input[name="count_custom"]'))) return;
    var custom = e.target.form.querySelector("[data-count-custom]");
    if (custom) custom.checked = e.target.value !== "";
    if (!custom.checked) {
      var one = e.target.form.querySelector('input[name="count"][value="1"]');
      if (one) one.checked = true;
    }
  });
  document.addEventListener("change", function (e) {
    var t = e.target;
    if (t.matches && t.matches('input[name="count"]') && t.value !== "custom") {
      var other = t.form.querySelector('input[name="count_custom"]');
      if (other) other.value = "";
    }
  });

  // --- Glaze text ---------------------------------------------------------
  // Known glaze names are recognised in free text with the same rules as the
  // server (model.MatchGlazes): ignoring case, whole words only, longest
  // names first, no overlaps.
  var wordChar = /[\p{L}\p{N}]/u;
  function isWordChar(c) { return c !== undefined && wordChar.test(c); }

  function matchGlazes(text, names) {
    var lower = text.toLowerCase();
    var taken = [];
    var matches = [];
    names.slice().sort(function (a, b) { return b.length - a.length; }).forEach(function (name) {
      var n = name.toLowerCase();
      if (!n.trim()) return;
      var i = 0;
      while ((i = lower.indexOf(n, i)) !== -1) {
        var end = i + n.length;
        var free = true;
        for (var k = i; k < end; k++) if (taken[k]) free = false;
        if (free && !isWordChar(text[i - 1]) && !isWordChar(text[end])) {
          for (k = i; k < end; k++) taken[k] = true;
          matches.push({ start: i, name: name });
          i = end;
        } else {
          i++;
        }
      }
    });
    matches.sort(function (a, b) { return a.start - b.start; });
    var seen = {};
    return matches.map(function (m) { return m.name; }).filter(function (n) {
      var k = n.toLowerCase();
      return seen[k] ? false : (seen[k] = true);
    });
  }

  function glazeNames(entry) { return JSON.parse(entry.dataset.glazeNames || "[]") || []; }

  // Preview: "Glazes: Pink · Red", so it's clear what will be recorded.
  function glazePreview(entry) {
    var text = entry.querySelector("[data-glaze-text]").value;
    var out = entry.querySelector("[data-glaze-preview]");
    var found = matchGlazes(text, glazeNames(entry));
    out.textContent = "";
    if (!text.trim()) return;
    if (!found.length) {
      out.textContent = "No known glazes recognised.";
      return;
    }
    out.appendChild(document.createTextNode("Glazes: "));
    found.forEach(function (name, i) {
      if (i) out.appendChild(document.createTextNode(" · "));
      var b = document.createElement("strong");
      b.textContent = name;
      out.appendChild(b);
    });
  }

  // Suggestions: known glazes starting with what's just been typed (which
  // may be several words, for names like "Floating Blue").
  function glazeSuggest(entry) {
    var box = entry.querySelector("[data-glaze-text]");
    var list = entry.querySelector("[data-glaze-suggest]");
    var before = box.value.slice(0, box.selectionStart);
    var names = glazeNames(entry);
    var best = null;
    for (var p = Math.max(0, before.length - 40); p < before.length; p++) {
      if (!isWordChar(before[p]) || isWordChar(before[p - 1])) continue;
      var typed = before.slice(p).toLowerCase();
      var hits = names.filter(function (n) {
        var l = n.toLowerCase();
        return l.indexOf(typed) === 0 && l !== typed;
      });
      if (hits.length) { best = { start: p, hits: hits.slice(0, 6) }; break; }
    }
    list.textContent = "";
    list.hidden = !best;
    if (!best) return;
    best.hits.forEach(function (name) {
      var b = document.createElement("button");
      b.type = "button";
      b.textContent = name;
      b.addEventListener("click", function () {
        var after = box.value.slice(box.selectionStart);
        box.value = box.value.slice(0, best.start) + name + after;
        var caret = best.start + name.length;
        box.focus();
        box.setSelectionRange(caret, caret);
        box.dispatchEvent(new Event("input", { bubbles: true }));
      });
      list.appendChild(b);
    });
  }

  document.addEventListener("input", function (e) {
    var entry = e.target.matches && e.target.matches("[data-glaze-text]") && e.target.closest("[data-glaze-entry]");
    if (!entry) return;
    glazePreview(entry);
    glazeSuggest(entry);
  });

  // "(add a glaze)": a name box that adds a known glaze without leaving the
  // page, then re-checks the text so the new name is recognised.
  function glazeAddClose(entry) {
    var form = entry.querySelector("[data-glaze-add]");
    form.hidden = true;
    form.querySelector("input").value = "";
    entry.querySelector("[data-glaze-add-open]").hidden = false;
  }
  function glazeAddSave(entry) {
    var input = entry.querySelector("[data-glaze-add] input");
    var name = input.value.trim();
    if (!name) { glazeAddClose(entry); return; }
    fetch("/glazes", {
      method: "POST",
      headers: { "Accept": "application/json" },
      body: new URLSearchParams({ title: name }),
    }).then(function (r) {
      if (!r.ok) throw new Error(r.status);
      return r.json();
    }).then(function (g) {
      var names = glazeNames(entry);
      if (names.every(function (n) { return n.toLowerCase() !== g.name.toLowerCase(); })) names.push(g.name);
      entry.dataset.glazeNames = JSON.stringify(names);
      glazeAddClose(entry);
      glazePreview(entry);
      // Save again (on the edit page) so the piece picks up the new glaze.
      entry.querySelector("[data-glaze-text]").dispatchEvent(new Event("change", { bubbles: true }));
    }).catch(function () {
      entry.querySelector("[data-glaze-preview]").textContent = "Couldn’t add the glaze. Check your connection.";
    });
  }
  document.addEventListener("click", function (e) {
    var t = e.target;
    var entry = t.closest && t.closest("[data-glaze-entry]");
    if (!entry) return;
    if (t.closest("[data-glaze-add-open]")) {
      t.closest("[data-glaze-add-open]").hidden = true;
      var form = entry.querySelector("[data-glaze-add]");
      form.hidden = false;
      form.querySelector("input").focus();
    } else if (t.closest("[data-glaze-add-save]")) {
      glazeAddSave(entry);
    }
  });
  document.addEventListener("keydown", function (e) {
    var input = e.target.closest && e.target.closest("[data-glaze-add] input");
    if (!input) return;
    var entry = input.closest("[data-glaze-entry]");
    if (e.key === "Enter") { e.preventDefault(); glazeAddSave(entry); }
    if (e.key === "Escape") glazeAddClose(entry);
  });

  // Rating pills: tapping the selected number again clears it. The radio's
  // state is remembered on pointerdown, before the browser checks it.
  document.addEventListener("pointerdown", function (e) {
    var label = e.target.closest && e.target.closest("label");
    var radio = label && label.querySelector("input[data-toggle-off]");
    if (radio) radio._wasChecked = radio.checked;
  });
  document.addEventListener("click", function (e) {
    var t = e.target;
    if (!(t.matches && t.matches("input[data-toggle-off]")) || !t._wasChecked) return;
    t.checked = false;
    t._wasChecked = false;
    t.dispatchEvent(new Event("change", { bubbles: true }));
  });

  // Choosing what happened (Started / Thrown / Built on the New page, or
  // Thrown / Built for a started piece) shows the inputs that belong to it
  // (elements with data-show-for), disabling the hidden ones so they aren't
  // sent, and updates the next-step button's wording.
  function syncChoice(form) {
    var chosen = form.querySelector('input[type="radio"][name="action"]:checked:not(:disabled)');
    if (!chosen) return;
    form.querySelectorAll("[data-show-for]").forEach(function (el) {
      var show = el.dataset.showFor === chosen.value;
      el.hidden = !show;
      el.querySelectorAll("input, select, textarea").forEach(function (i) { i.disabled = !show; });
    });
    var button = form.querySelector("button[data-count-label]");
    if (button && chosen.dataset.label) {
      button.dataset.countLabel = chosen.dataset.countLabel;
      button.dataset.oneLabel = chosen.dataset.label;
      button.textContent = chosen.dataset.label;
      syncAlsoCount(form);
    }
  }
  document.addEventListener("change", function (e) {
    var t = e.target;
    if (t.matches && t.matches('input[type="radio"][name="action"]')) syncChoice(t.form);
  });

  // The menu closes when you tap anywhere else.
  document.addEventListener("click", function (e) {
    document.querySelectorAll("details[data-menu][open]").forEach(function (m) {
      if (!m.contains(e.target)) m.open = false;
    });
  });

  function init(root) {
    root.querySelectorAll("form").forEach(function (f) {
      if (f.querySelector("[data-show-for]")) syncChoice(f);
    });
    root.querySelectorAll("[data-glaze-entry]").forEach(glazePreview);
    root.querySelectorAll("form").forEach(function (f) {
      if (f.querySelector('input[name="also"]')) syncAlsoCount(f);
    });
  }

  document.addEventListener("click", function (e) {
    var back = e.target.closest && e.target.closest("[data-back]");
    if (back && history.length > 1) {
      e.preventDefault();
      history.back();
    }
  });

  document.addEventListener("htmx:load", function (e) { init(e.target); });
  document.addEventListener("DOMContentLoaded", function () { init(document); });
})();
