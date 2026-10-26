// Ulepszenia opcjonalne (aplikacja działa także bez JavaScriptu):
// skróty A–D / 1–4 na ekranie pytania, drukowanie wyniku z rozwiniętym omówieniem.
document.addEventListener("DOMContentLoaded", function () {
  var form = document.getElementById("qform");
  if (form) {
    document.addEventListener("keydown", function (e) {
      if (e.ctrlKey || e.metaKey || e.altKey) return;
      var map = { a: 0, b: 1, c: 2, d: 3, "1": 0, "2": 1, "3": 2, "4": 3 };
      var i = map[(e.key || "").toLowerCase()];
      if (i === undefined) return;
      var radios = form.querySelectorAll('input[name="odp"]');
      if (radios[i]) { radios[i].checked = true; radios[i].focus(); }
    });
  }
  var p = document.getElementById("drukuj");
  if (p) p.addEventListener("click", function () {
    document.querySelectorAll("details.ans").forEach(function (d) { d.open = true; });
    window.print();
  });
});
