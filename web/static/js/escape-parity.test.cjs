// Gate for the one-escaper rule: web/static/js/escape.js holds the only implementation,
// and every per-file escapeHtml / escapeHtmlLocal / escapeHtmlAttr is a one-line delegation.
//
// This exists because fourteen files each defined escapeHtml, nine of them at global scope
// in scripts that all load into the same page, so the escaping set in force depended on
// script order - and two of the implementations (projects.js, wechat-robot.js) were
// regex-based and left a single quote alone while the others did not. A sanitizer whose
// behavior is chosen by load order is a latent injection, not a style problem.
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const JS_DIR = __dirname;
const TemplateDir = () => path.join(__dirname, '..', '..', 'templates');

function listSources() {
	return fs.readdirSync(JS_DIR)
		.filter((f) => f.endsWith('.js'))
		.map((f) => [f, fs.readFileSync(path.join(JS_DIR, f), 'utf8')]);
}

// balancedBody returns the text between a function's braces, so a delegation can be
// recognized by its whole body rather than by a prefix that a longer implementation also
// starts with.
function balancedBody(src, openBrace) {
	let depth = 0;
	for (let i = openBrace; i < src.length; i++) {
		if (src[i] === '{') depth++;
		else if (src[i] === '}') {
			depth--;
			if (depth === 0) return src.slice(openBrace + 1, i);
		}
	}
	return null;
}

const DECLARATION = /(?:function|const)\s+(escapeHtml|escapeHtmlLocal|escapeHtmlAttr)\s*(?:=\s*)?\(([^)]*)\)\s*(?:=>\s*)?\{/g;

test('the canonical escaper escapes everything a text node or a quoted attribute needs', () => {
	const src = fs.readFileSync(path.join(JS_DIR, 'escape.js'), 'utf8');
	const sandbox = { window: {}, document: undefined, console };
	sandbox.globalThis = sandbox;
	vm.createContext(sandbox);
	vm.runInContext(src, sandbox, { filename: 'escape.js' });
	const escape = sandbox.window.CSAI && sandbox.window.CSAI.escapeHtml;
	assert.strictEqual(typeof escape, 'function', 'escape.js must define CSAI.escapeHtml');

	assert.strictEqual(escape('<script>'), '&lt;script&gt;');
	assert.strictEqual(escape('a & b'), 'a &amp; b');
	// The two cases the old regex implementations disagreed about, and the two that let an
	// attribute break out of its quotes.
	assert.strictEqual(escape(`he said "hi"`), 'he said &quot;hi&quot;');
	assert.strictEqual(escape("it's"), 'it&#39;s');
	assert.strictEqual(escape(null), '');
	assert.strictEqual(escape(undefined), '');
	// Escaping must not be lost on a value that already looks like markup - double escaping
	// is ugly, under-escaping is a bug, and only one of those is silent.
	assert.strictEqual(escape('&lt;'), '&amp;lt;');
	assert.strictEqual(sandbox.window.CSAI.escapeHtmlAttr('x"y'), 'x&quot;y');
});

test('no file keeps its own HTML escaper', () => {
	const offenders = [];
	let delegating = 0;
	for (const [name, src] of listSources()) {
		if (name === 'escape.js') continue;
		for (const match of src.matchAll(DECLARATION)) {
			const body = balancedBody(src, src.indexOf('{', match.index + match[0].length - 1));
			assert.notStrictEqual(body, null, `${name}: unbalanced ${match[1]}`);
			const arg = (match[2] || '').split(',')[0].trim() || 'value';
			const canonical = new RegExp(`^\\s*return\\s+CSAI\\.(escapeHtml|escapeHtmlAttr)\\(${arg}\\);?\\s*$`);
			if (!canonical.test(body)) offenders.push(`${name}: ${match[1]}`);
			delegating++;
		}
	}
	// An empty scan would mean the regex stopped matching, which is how a gate like this
	// goes green while the duplication comes back.
	assert.ok(delegating >= 15, `expected the console to still carry its per-file escapers, found ${delegating}`);
	assert.deepStrictEqual(offenders, [], 'these files still implement escaping instead of delegating to CSAI.escapeHtml');
});

test('every template that loads a delegating script loads escape.js first', () => {
	// The console is one template, but api-docs.js and friends belong to other pages, so the
	// order is asserted per template rather than against index.html alone - a script loaded
	// only from api-docs.html would otherwise be checked by nothing.
	const templates = fs.readdirSync(TemplateDir())
		.filter((f) => f.endsWith('.html'))
		.map((f) => [f, fs.readFileSync(path.join(TemplateDir(), f), 'utf8')]);

	const loaders = {};
	for (const [name, src] of listSources()) {
		if (name === 'escape.js') continue;
		DECLARATION.lastIndex = 0;
		if (!DECLARATION.test(src)) continue;
		DECLARATION.lastIndex = 0;
		loaders[name] = templates.filter(([, html]) => html.includes(`/static/js/${name}`)).map(([f]) => f);
		assert.ok(loaders[name].length > 0, `${name} defines an escaper but no template loads it`);
	}

	for (const [template, html] of templates) {
		const escapeAt = html.indexOf('/static/js/escape.js');
		for (const [script, loadedFrom] of Object.entries(loaders)) {
			if (!loadedFrom.includes(template)) continue;
			assert.ok(escapeAt >= 0, `${template} loads ${script} but never loads escape.js`);
			assert.ok(escapeAt < html.indexOf(`/static/js/${script}`), `${template} must load escape.js before ${script}`);
		}
	}
});

test('the escapers that used to differ now behave the same', () => {
	// projects.js and wechat-robot.js were the regex implementations; if either grows a
	// local replace() back, the load-order bug returns with it.
	for (const name of ['projects.js', 'wechat-robot.js']) {
		const src = fs.readFileSync(path.join(JS_DIR, name), 'utf8');
		for (const match of src.matchAll(DECLARATION)) {
			const body = balancedBody(src, src.indexOf('{', match.index + match[0].length - 1));
			assert.ok(!/\.replace\(/.test(body || ''), `${name}: ${match[1]} hand-rolls escaping again`);
			assert.match(body || '', /CSAI\.escapeHtml/, `${name}: ${match[1]} must delegate`);
		}
		DECLARATION.lastIndex = 0;
	}
});
