// One HTML escaper for the whole console.
//
// Before this file existed, `escapeHtml` was defined in fourteen files - nine of them at
// global scope in scripts that all load into the same page - so which implementation any
// call actually got was decided by script order. They were not equivalent: the regex-based
// ones left a single quote alone while the DOM-based ones did the same, and a sanitizer
// whose escaping set depends on which file happened to load last is exactly the thing that
// turns an attribute-escaping mistake into markup.
//
// The canonical rule is therefore the union: escape &, <, >, " and ' always, and do it with
// string replacement rather than textContent/innerHTML so the result is defined by this
// file rather than by a browser's serializer - the console's contract tests run in Node.
(function () {
    window.CSAI = window.CSAI || {};

    const ESCAPES = {
        '&': '&amp;',
        '<': '&lt;',
        '>': '&gt;',
        '"': '&quot;',
        "'": '&#39;'
    };

    // escapeHtml is safe for both text nodes and double- or single-quoted attributes, so a
    // caller never has to decide which of two escapers it needed.
    function escapeHtml(value) {
        if (value === null || value === undefined) return '';
        return String(value).replace(/[&<>"']/g, (ch) => ESCAPES[ch]);
    }

    window.CSAI.escapeHtml = escapeHtml;
    // Name kept distinct from the per-file escapeHtmlAttr helpers so the parity test can
    // tell "delegating to the canonical implementation" from "still has its own".
    window.CSAI.escapeHtmlAttr = escapeHtml;

    // A JavaScript string literal destined for an inline handler, and that same literal
    // escaped for the attribute around it. Kept separate from escapeHtml on purpose: they
    // escape for different target languages, and the composition order is what makes the
    // result safe - JSON.stringify first (so quotes and backslashes become JS), then the
    // HTML escaper (so the attribute's own quotes survive). Eight files each had a
    // byte-identical copy of these two, which is duplication worth owning in one place even
    // when the implementations are already correct.
    function escapeJsString(value) {
        return JSON.stringify(String(value === null || value === undefined ? '' : value));
    }

    window.CSAI.escapeJsString = escapeJsString;
    window.CSAI.escapeJsStringAttr = function (value) { return escapeHtml(escapeJsString(value)); };
})();
