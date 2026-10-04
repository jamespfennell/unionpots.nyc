// Small progressive enhancements. Pages are swapped by htmx (hx-boost), so
// everything uses event delegation plus an init pass on each htmx:load.
(function () {
  "use strict";

  // Backfill section on the new-piece page: while open, its action select
  // replaces the "started by" radios.
  function syncBackfill(details) {
    var form = details.closest("form");
    var open = details.open;
    form.querySelectorAll("input[data-start]").forEach(function (r) { r.disabled = open; });
    details.querySelectorAll('select[name="action"]').forEach(function (s) { s.disabled = !open; });
  }

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

  // Show how many pieces an action will apply to once others are included,
  // e.g. "→ Finished today · 3 pieces".
  function syncAlsoCount(form) {
    var n = form.querySelectorAll('input[name="also"]:checked').length;
    form.querySelectorAll("[data-also-count]").forEach(function (el) {
      el.textContent = n ? " · " + (n + 1) + " pieces" : "";
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

  // Measurements: "Round" hides depth (the server copies width into it).
  function syncRound(box) {
    var depth = box.closest(".dims-inputs").querySelector("[data-depth]");
    if (depth) depth.disabled = box.checked;
  }
  document.addEventListener("change", function (e) {
    var t = e.target;
    if (!t.matches) return;
    if (t.matches("[data-round]")) syncRound(t);
  });

  // Glaze boxes: once the last one has text, add an empty one after it.
  document.addEventListener("input", function (e) {
    var t = e.target;
    if (!(t.matches && t.matches('input[name="glaze"]')) || t.value === "") return;
    var list = t.closest("[data-glaze-list]");
    var boxes = list.querySelectorAll('input[name="glaze"]');
    if (boxes[boxes.length - 1] !== t) return;
    var next = t.cloneNode(false);
    next.value = "";
    next.placeholder = "another glaze";
    list.appendChild(next);
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

  function init(root) {
    root.querySelectorAll("details[data-backfill]").forEach(syncBackfill);
    root.querySelectorAll("[data-round]").forEach(syncRound);
    root.querySelectorAll("form").forEach(function (f) {
      if (f.querySelector('input[name="also"]')) syncAlsoCount(f);
    });
  }

  document.addEventListener("toggle", function (e) {
    if (e.target.matches && e.target.matches("details[data-backfill]")) syncBackfill(e.target);
  }, true);

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
