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
    if (e.target.matches && e.target.matches('input[name="also"]')) {
      syncAlsoCount(e.target.form);
      bisquePrice(e.target.form);
    }
  });

  // A measurement as the server reads it (model.ParseMeasure): "4.5",
  // "4 1/2" or "3/4". NaN if it isn't one.
  function parseMeasure(s) {
    var m = s.trim().match(/^(?:(\d+) +)?(\d+)\/(\d+)$/);
    var v = m ? (m[1] ? +m[1] : 0) + m[2] / m[3] : /^(\d+\.?\d*|\.\d+)$/.test(s.trim()) ? +s.trim() : NaN;
    return v > 0 && isFinite(v) ? v : NaN;
  }

  // The volume (height × width × depth) of a row of dimension inputs, by
  // its key ("step" → h_step, w_step, d_step). NaN until all three are given.
  function dimsVolume(form, key) {
    return ["h", "w", "d"].reduce(function (v, k) {
      return v * parseMeasure(form.querySelector('[name="' + k + "_" + key + '"]').value);
    }, 1);
  }

  // Fills el with a heading line and the firing fee for vol cubic inches at
  // each rate, one line (span) each.
  function feeLines(el, first, vol) {
    var rates = el.closest("[data-rates]").dataset.rates.split(" ").map(Number);
    el.replaceChildren.apply(el, [first].concat(rates.map(function (r) {
      return "$" + r.toFixed(2) + "/cu in: $" + (vol * r).toFixed(2);
    })).map(function (t) {
      var s = document.createElement("span");
      s.textContent = t;
      return s;
    }));
  }

  function cubicInches(vol) { return Math.round(vol * 10) / 10 + " cu in"; }

  // Queued for bisque: what firing costs for this piece; with others
  // included, the total goes under the piece picker.
  function bisquePrice(form) {
    var out = form && form.querySelector("[data-bisque-price]");
    if (!out) return;
    var total = form.querySelector("[data-bisque-total]");
    var vol = dimsVolume(form, "step");
    var n = form.querySelectorAll('input[name="also"]:checked').length + 1;
    out.hidden = isNaN(vol);
    if (total) total.hidden = out.hidden || n === 1;
    if (out.hidden) return;
    feeLines(out, "Volume: " + cubicInches(vol) + ". Firing fee:", vol);
    if (total && !total.hidden) feeLines(total, "Total firing fee:", n * vol);
  }

  // Firing calculators, two separate ways: a stack measured as a whole, or
  // the ticked pieces, each at its queued size (just the fee: their total
  // volume means nothing). Both show 0 until something is entered or ticked.
  function firingCalc(form) {
    if (!form || !form.matches("[data-firing]")) return;
    var vol = dimsVolume(form, "stack");
    if (isNaN(vol)) vol = 0;
    feeLines(form.querySelector("[data-firing-stack]"), "Volume: " + cubicInches(vol) + ". Firing fee:", vol);
    var pieces = form.querySelector("[data-firing-pieces]");
    if (pieces) {
      var ticked = form.querySelectorAll('input[name="piece"]:checked');
      feeLines(pieces, "Firing fee for " + ticked.length + (ticked.length === 1 ? " piece:" : " pieces:"),
        Array.prototype.reduce.call(ticked, function (v, c) { return v + Number(c.dataset.volume); }, 0));
    }
  }
  document.addEventListener("change", function (e) {
    if (e.target.matches && e.target.matches('input[name="piece"]')) firingCalc(e.target.form);
  });
  document.addEventListener("input", function (e) {
    if (!(e.target.matches && e.target.matches('.dims input'))) return;
    bisquePrice(e.target.form);
    firingCalc(e.target.form);
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

  // Photos: each chosen photo is uploaded on its own request (phone photos
  // are large), with progress next to the heading. Each response is the
  // updated photos section, swapped in once all are done.
  document.addEventListener("change", function (e) {
    var input = e.target;
    var form = input.matches && input.matches('input[type="file"]') && input.closest("form[data-photo-upload]");
    if (!form || !input.files.length) return;
    var files = Array.prototype.slice.call(input.files);
    var section = form.closest("[data-photos]");
    var status = section.querySelector("[data-photo-status]");
    var label = form.querySelector(".photo-add");
    label.classList.add("busy");
    input.disabled = true;
    var html = null, failed = [];
    function say(text, state) {
      status.textContent = text;
      if (state) status.dataset.state = state; else delete status.dataset.state;
    }
    function next(i) {
      if (i === files.length) return finish();
      say(files.length > 1 ? "Uploading " + (i + 1) + " of " + files.length + "…" : "Uploading…");
      var body = new FormData();
      body.append("photos", files[i]);
      fetch(form.action, { method: "POST", headers: { "X-Photos": "1" }, body: body })
        .then(function (r) {
          return r.text().then(function (t) {
            if (r.ok) html = t;
            else failed.push(r.status === 400 ? t.trim() : files[i].name + ": couldn’t upload.");
          });
        })
        .catch(function () { failed.push(files[i].name + ": couldn’t upload. Check your connection."); })
        .then(function () { next(i + 1); });
    }
    function finish() {
      var target = section;
      if (html) {
        var tmp = document.createElement("div");
        tmp.innerHTML = html.trim();
        target = tmp.firstElementChild;
        section.replaceWith(target);
        if (window.htmx) htmx.process(target);
      } else {
        label.classList.remove("busy");
        input.disabled = false;
      }
      input.value = "";
      var st = target.querySelector("[data-photo-status]");
      if (failed.length) {
        st.textContent = failed.join(" ");
        st.dataset.state = "error";
      } else {
        st.textContent = files.length > 1 ? "Added " + files.length + " photos" : "Added";
        st.dataset.state = "saved";
        setTimeout(function () { st.textContent = ""; delete st.dataset.state; }, 2500);
      }
    }
    next(0);
  });

  // Clay pills: one clay unless "+ another clay" was tapped. This runs in the
  // capture phase, before htmx's autosave reads the form, so only the new
  // choice is saved.
  document.addEventListener("change", function (e) {
    var input = e.target;
    var pills = input.matches && input.matches('input[name="clay"]') && input.closest("[data-clay-pills]");
    if (!pills || !input.checked || pills.hasAttribute("data-multi")) return;
    pills.querySelectorAll('input[name="clay"]').forEach(function (other) {
      if (other !== input) other.checked = false;
    });
  }, true);
  document.addEventListener("click", function (e) {
    var link = e.target.closest && e.target.closest("[data-clay-multi]");
    if (!link) return;
    link.closest("[data-clay-pills]").setAttribute("data-multi", "");
    link.hidden = true;
  });

  // Step dates save as soon as a date in range is picked (a half-typed
  // date is out of range, so it waits).
  document.addEventListener("change", function (e) {
    var input = e.target;
    if (!(input.matches && input.matches("form[data-step-date] input[type=date]"))) return;
    if (input.value && input.checkValidity()) htmx.trigger(input.form, "save");
  });

  // Filter forms apply as soon as a dropdown changes. Filters left at "Any"
  // are left out of the address, so a bookmarked view reads cleanly.
  document.addEventListener("change", function (e) {
    var form = e.target.matches && e.target.matches("select") && e.target.closest("form[data-autosubmit]");
    if (!form) return;
    form.querySelectorAll("select").forEach(function (s) { s.disabled = s.value === ""; });
    form.requestSubmit();
  });

  // The menu closes when you tap anywhere else.
  document.addEventListener("click", function (e) {
    document.querySelectorAll("details[data-menu][open]").forEach(function (m) {
      if (!m.contains(e.target)) m.open = false;
    });
  });

  // --- Photo viewer -------------------------------------------------------
  // Moving between photos (arrows, swipes, ← →) replaces the page in the
  // history, so Back and ✕ (or Escape, or tapping the black around the
  // photo) go straight back to the piece.
  function viewerGo(which) {
    var link = document.querySelector("[data-viewer-" + which + "]");
    if (link) location.replace(link.href);
  }
  document.addEventListener("click", function (e) {
    var nav = e.target.closest && e.target.closest("[data-viewer-prev], [data-viewer-next]");
    if (!nav) return;
    e.preventDefault();
    location.replace(nav.href);
  });
  document.addEventListener("click", function (e) {
    // Only the black itself: not the photo, the text or the buttons on it.
    if (!e.target.hasAttribute || !e.target.hasAttribute("data-viewer-backdrop")) return;
    document.querySelector("[data-viewer-close]").click();
  });
  document.addEventListener("keydown", function (e) {
    if (!document.querySelector("[data-viewer]") || e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.key === "ArrowLeft") viewerGo("prev");
    else if (e.key === "ArrowRight") viewerGo("next");
    else if (e.key === "Escape") document.querySelector("[data-viewer-close]").click();
  });
  var swipe = null;
  document.addEventListener("touchstart", function (e) {
    swipe = e.touches.length === 1 && e.target.closest && e.target.closest("[data-viewer-stage]")
      ? { x: e.touches[0].clientX, y: e.touches[0].clientY } : null;
  }, { passive: true });
  document.addEventListener("touchend", function (e) {
    if (!swipe) return;
    var dx = e.changedTouches[0].clientX - swipe.x, dy = e.changedTouches[0].clientY - swipe.y;
    swipe = null;
    if (Math.abs(dx) > 50 && Math.abs(dx) > 2 * Math.abs(dy)) viewerGo(dx < 0 ? "next" : "prev");
  }, { passive: true });

  // Share sends the photo file itself (to WhatsApp, Messages, …) through the
  // system share sheet. The file is fetched when the viewer opens (it's the
  // photo on screen, so it comes from the cache): browsers only allow
  // sharing straight from the tap, not after waiting for a download. Where
  // files can't be shared, the link downloads the photo instead.
  function prepareShare(link) {
    if (!navigator.canShare) return;
    fetch(link.href).then(function (r) { return r.ok ? r.blob() : null; }).then(function (blob) {
      if (!blob) return;
      var file = new File([blob], link.getAttribute("download"), { type: "image/jpeg" });
      if (navigator.canShare({ files: [file] })) link._file = file;
    }).catch(function () {});
  }
  document.addEventListener("click", function (e) {
    var link = e.target.closest && e.target.closest("[data-share-photo]");
    if (!link || !link._file) return;
    e.preventDefault();
    navigator.share({ files: [link._file] }).catch(function () {});
  });

  function init(root) {
    root.querySelectorAll("[data-share-photo]").forEach(prepareShare);
    root.querySelectorAll("form").forEach(function (f) {
      if (f.querySelector("[data-show-for]")) syncChoice(f);
    });
    root.querySelectorAll("[data-glaze-entry]").forEach(glazePreview);
    root.querySelectorAll("form").forEach(function (f) {
      if (f.querySelector('input[name="also"]')) syncAlsoCount(f);
      bisquePrice(f);
      firingCalc(f);
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
