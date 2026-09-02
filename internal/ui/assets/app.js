(function () {
  // Every wiring function below runs again after each htmx swap, over a scope
  // that usually still contains elements from the last pass. Without a marker
  // the listeners stack: two click handlers on one dialog opener means a second
  // showModal() against an already-open <dialog>, which throws.
  function once(element, key) {
    if (element.dataset[key] === '1') {
      return false;
    }
    element.dataset[key] = '1';
    return true;
  }

  function activateTabs(scope) {
    var root = scope || document;
    root.querySelectorAll('[data-tab-button]').forEach(function (button) {
      if (!once(button, 'tabBound')) {
        return;
      }
      button.addEventListener('click', function () {
        selectTab(button);
      });
      button.addEventListener('keydown', function (event) {
        var step = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0;
        if (step === 0) {
          return;
        }
        event.preventDefault();
        var all = Array.prototype.slice.call(document.querySelectorAll('[data-tab-button]'));
        var next = all[(all.indexOf(button) + step + all.length) % all.length];
        selectTab(next);
        next.focus();
      });
    });
  }

  // A link such as /profile#keys names the tab it wants to open. Without this
  // the fragment was inert and the page opened on its default tab, leaving the
  // SSH key form on a hidden panel one link away from where it said it was.
  //
  // An unknown fragment is left alone: not every # on the site is a tab, and
  // an ordinary anchor must keep scrolling.
  function selectTabFromHash() {
    var name = window.location.hash.replace(/^#/, '');
    if (!name) {
      return;
    }
    var button = document.querySelector('[data-tab-button="' + CSS.escape(name) + '"]');
    if (button) {
      selectTab(button);
    }
  }

  window.addEventListener('hashchange', selectTabFromHash);

  // Queries from the document rather than the wiring scope: the scope is
  // whatever htmx last swapped, and the panels are not always inside it.
  function selectTab(button) {
    var name = button.getAttribute('data-tab-button');
    document.querySelectorAll('[data-tab-button]').forEach(function (candidate) {
      var active = candidate === button;
      candidate.classList.toggle('active', active);
      candidate.setAttribute('aria-selected', active ? 'true' : 'false');
      candidate.setAttribute('tabindex', active ? '0' : '-1');
    });
    document.querySelectorAll('[data-tab-panel]').forEach(function (panel) {
      panel.classList.toggle('active', panel.getAttribute('data-tab-panel') === name);
    });
    // A panel that was hidden has just gained a size. Anything that had to
    // measure itself - the terminal - could not until now, and an event is how
    // it hears without selectTab knowing what a terminal is.
    document.dispatchEvent(new CustomEvent('dwpk:tabshown', { detail: name }));
  }

  // wireOnboardingSteps drives the wizard's Next/Back buttons off the same
  // tab machinery as everything else on the page, so a step is just a panel
  // with a name - no separate stepper state to keep in sync. The buttons
  // carry data-step-to instead of data-tab-button precisely so they are not
  // themselves swept into the tab/arrow-key wiring above.
  function wireOnboardingSteps(scope) {
    (scope || document).querySelectorAll('[data-step-to]').forEach(function (button) {
      if (!once(button, 'stepBound')) {
        return;
      }
      button.addEventListener('click', function () {
        var target = document.querySelector('[data-tab-button="' + this.getAttribute('data-step-to') + '"]');
        if (target) {
          selectTab(target);
        }
      });
    });
  }

  function wireCopy(scope) {
    (scope || document).querySelectorAll('[data-copy-target]').forEach(function (button) {
      if (!once(button, 'copyBound')) {
        return;
      }
      button.addEventListener('click', function () {
        var target = document.getElementById(button.getAttribute('data-copy-target'));
        if (!target) {
          return;
        }
        var text = target.textContent || '';

        function done(ok) {
          // A silent no-op is indistinguishable from a broken button, so say
          // which of the two happened.
          var original = button.textContent;
          button.textContent = ok ? 'Copied' : 'Press ⌘C';
          announce(ok ? 'Copied to clipboard' : 'Select and press Command-C to copy');
          window.setTimeout(function () { button.textContent = original; }, 1500);
        }

        // navigator.clipboard exists only in a secure context - https, or
        // http on localhost. Reached over a plain-http hostname (an OrbStack
        // *.k8s.orb.local URL, a LAN IP) it is undefined, and the button used
        // to do nothing at all. Fall through rather than give up.
        if (navigator.clipboard && window.isSecureContext) {
          navigator.clipboard.writeText(text).then(function () { done(true); }, function () { done(legacyCopy(text)); });
          return;
        }
        done(legacyCopy(text) || selectText(target));
      });
    });
  }

  // execCommand('copy') is deprecated but is the only thing that works outside
  // a secure context, which is where the modern API is simply absent.
  function legacyCopy(text) {
    var scratch = document.createElement('textarea');
    scratch.value = text;
    scratch.setAttribute('readonly', '');
    scratch.className = 'visually-hidden';
    document.body.appendChild(scratch);
    scratch.select();
    var ok = false;
    try {
      ok = document.execCommand('copy');
    } catch (err) {
      ok = false;
    }
    document.body.removeChild(scratch);
    return ok;
  }

  // Last resort: leave the text selected so the keyboard shortcut works. The
  // button then tells the reader to press it.
  function selectText(node) {
    var range = document.createRange();
    range.selectNodeContents(node);
    var selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    return false;
  }

  // The live region is rendered once in the layout. Screen readers announce
  // whatever lands in it; sighted users never see it.
  function announce(message) {
    var region = document.getElementById('live-region');
    if (region) {
      region.textContent = message;
    }
  }

  // A real terminal. xterm.js is vendored under assets/vendor because the CSP
  // forbids a CDN; it is loaded by the pages that need it, so the rest of the
  // app does not pay for it.
  //
  // The websocket protocol is unchanged - {type,data,cols,rows} with
  // input/output/error/resize - so this is a new front end over the gateway's
  // existing exec path, not a second implementation of it (SPEC 8.4).
  function connectTerminal(scope) {
    (scope || document).querySelectorAll('[data-terminal]').forEach(function (mount) {
      if (!once(mount, 'terminalBound') || typeof window.Terminal !== 'function') {
        return;
      }
      var name = mount.getAttribute('data-workspace-name');
      var selector = '[data-workspace-name="' + name + '"]';
      var status = document.querySelector('[data-terminal-status]' + selector);
      var reconnect = document.querySelector('[data-terminal-reconnect]' + selector);
      var fullscreen = document.querySelector('[data-terminal-fullscreen]' + selector);
      var popout = document.querySelector('[data-terminal-popout]' + selector);
      var socket = null;
      var disposers = [];
      // Whether this terminal has ever had a session, which decides whether the
      // button offers to connect or to reconnect.
      var connected = false;

      var styles = getComputedStyle(document.documentElement);
      var term = new window.Terminal({
        cursorBlink: true,
        convertEol: true,
        fontFamily: styles.getPropertyValue('--dwpk-font-mono').trim() || 'monospace',
        fontSize: 13,
        // Right-click selects the word under the cursor before the paste
        // handler runs, which is what every native terminal does.
        rightClickSelectsWord: true,
        // Otherwise Option+arrow on a Mac inserts an accented character instead
        // of walking the shell's word boundaries.
        macOptionIsMeta: true,
        theme: {
          background: styles.getPropertyValue('--dwpk-terminal-bg').trim() || '#09090b',
          foreground: styles.getPropertyValue('--dwpk-terminal-ink').trim() || '#e4e4e7'
        }
      });

      var fit = null;
      if (window.FitAddon && window.FitAddon.FitAddon) {
        fit = new window.FitAddon.FitAddon();
        term.loadAddon(fit);
      }
      term.open(mount);

      // refit measures the mount and resizes the terminal to match.
      //
      // It compares before acting, and that guard is the whole fix for "the
      // window grows on every click". The fit addon reads the mount's computed
      // height; where that height is auto - full screen, and the pop-out page -
      // it is the height the LAST fit produced, so an unguarded refit ratchets
      // the terminal a row taller every time anything triggers it. Proposing
      // first and returning when nothing changed makes a repeated trigger free
      // and breaks the loop.
      function refit() {
        if (!fit) { return; }
        try {
          var proposed = fit.proposeDimensions();
          if (!proposed || !proposed.cols || !proposed.rows) {
            // Zero means the element is not laid out - it is still in a hidden
            // tab panel. Fitting now would measure nothing and lock in 80x24.
            return;
          }
          if (proposed.cols === term.cols && proposed.rows === term.rows) {
            return;
          }
          fit.fit();
        } catch (err) { /* not laid out yet */ }
      }

      function setState(state, message) {
        if (status) {
          status.textContent = message;
          status.className = 'terminal-status status-chip status-' + state;
        }
        var offline = state !== 'ok' && state !== 'busy';
        if (reconnect) {
          reconnect.hidden = !offline;
          // Hidden is not disabled: a button reached by keyboard a moment before
          // the state changed would still fire.
          reconnect.disabled = !offline;
          // "Reconnect" is a lie before the first connection. Since a tab now
          // waits to be opened, this button is usually the way in rather than
          // the way back.
          reconnect.textContent = connected ? 'Reconnect' : 'Connect';
        }
      }

      function sendSize() {
        if (socket && socket.readyState === WebSocket.OPEN) {
          socket.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
        }
      }

      // Closing the websocket is what kills the shell: the server's read loop
      // fails, cancels the context and tears the exec down. Dropping a socket
      // without closing it leaves that shell as a PID in the pod until the
      // connection times out, which is how repeated reconnects piled them up.
      function closeSocket() {
        if (!socket) { return; }
        var stale = socket;
        socket = null;
        try {
          stale.close(1000, 'closing');
        } catch (err) { /* already gone; nothing to release */ }
      }

      function open() {
        if (socket && (socket.readyState === WebSocket.CONNECTING ||
            socket.readyState === WebSocket.OPEN)) {
          // A live session already exists. Opening a second one on top of it is
          // exactly what this guard is here to prevent.
          return;
        }
        closeSocket();
        setState('busy', 'Connecting…');

        // Fit BEFORE connecting so the size sent with the first resize is the
        // real one. Connecting first told the server 120x40 while xterm was
        // still its un-fitted 80x24 default, and readline then drew every
        // wrapped line and every arrow-key movement at the wrong column.
        refit();

        var protocol = window.location.protocol === 'https:' ? 'wss://' : 'ws://';
        var basePath = document.body.getAttribute('data-base-path') || '';
        var opened = new WebSocket(protocol + window.location.host + basePath +
          '/w/' + encodeURIComponent(name) + '/terminal/ws');
        socket = opened;

        // A superseded socket still delivers its close event, and without this
        // the old session's "Disconnected" would land on top of the new one's
        // "Connected" a moment after it appeared.
        function current() { return socket === opened; }

        opened.addEventListener('open', function () {
          if (!current()) { opened.close(1000, 'superseded'); return; }
          connected = true;
          setState('ok', 'Connected');
          sendSize();
          term.focus();
        });
        opened.addEventListener('message', function (event) {
          if (!current()) { return; }
          try {
            var message = JSON.parse(event.data);
            if (message.type === 'output' || message.type === 'error') {
              term.write(message.data);
            }
          } catch (err) {
            term.write(event.data);
          }
        });
        opened.addEventListener('close', function (event) {
          if (!current()) { return; }
          socket = null;
          // 1000 is the shell exiting cleanly, which is not a failure.
          setState(event.code === 1000 ? 'idle' : 'bad',
            event.code === 1000 ? 'Session ended' : 'Disconnected');
          announce('Terminal disconnected');
        });
        opened.addEventListener('error', function () {
          if (!current()) { return; }
          setState('bad', 'Connection failed');
        });
      }

      // Selecting with the mouse copies, the way a terminal emulator does.
      //
      // Through the same fallback the copy buttons use: navigator.clipboard is
      // undefined outside a secure context, which is exactly where this runs
      // over plain http. Without the fallback this would silently do nothing on
      // the deployment it was asked for.
      function copySelection() {
        var selected = term.getSelection();
        if (!selected) { return false; }
        if (navigator.clipboard && window.isSecureContext) {
          navigator.clipboard.writeText(selected).then(function () {}, function () {
            legacyCopy(selected);
          });
          return true;
        }
        return legacyCopy(selected);
      }

      function paste() {
        if (navigator.clipboard && navigator.clipboard.readText && window.isSecureContext) {
          navigator.clipboard.readText().then(function (text) {
            if (text && socket && socket.readyState === WebSocket.OPEN) {
              socket.send(JSON.stringify({ type: 'input', data: text }));
            }
          }, function () { /* the browser refused; the keyboard shortcut still works */ });
          return true;
        }
        // Outside a secure context the page cannot read the clipboard at all.
        // Say so rather than appear to have done nothing.
        announce('Pasting needs Ctrl-Shift-V or the browser menu on this connection');
        return false;
      }

      term.onSelectionChange(function () {
        if (term.hasSelection()) {
          copySelection();
        }
      });

      // Everything not claimed here falls through to xterm, so the arrow keys,
      // Ctrl-C, Ctrl-R and the rest reach the shell untouched. Only the two
      // clipboard chords are intercepted, and only on keydown.
      term.attachCustomKeyEventHandler(function (event) {
        if (event.type !== 'keydown') { return true; }
        var modifier = event.ctrlKey || event.metaKey;
        if (!modifier || !event.shiftKey) { return true; }
        if (event.key === 'C' || event.key === 'c') {
          if (term.hasSelection()) {
            copySelection();
            return false;
          }
          return true;
        }
        if (event.key === 'V' || event.key === 'v') {
          paste();
          return false;
        }
        return true;
      });

      mount.addEventListener('contextmenu', function (event) {
        // rightClickSelectsWord has already run, so a right-click on a word
        // copies it and a right-click elsewhere pastes.
        if (term.hasSelection()) {
          copySelection();
        } else {
          paste();
        }
        event.preventDefault();
      });

      term.onData(function (data) {
        if (socket && socket.readyState === WebSocket.OPEN) {
          socket.send(JSON.stringify({ type: 'input', data: data }));
        }
      });
      term.onResize(sendSize);

      if (reconnect) {
        reconnect.addEventListener('click', function () {
          term.reset();
          open();
        });
      }
      if (fullscreen) {
        fullscreen.addEventListener('click', function () {
          var shell = mount.closest('[data-terminal-shell]') || mount;
          if (document.fullscreenElement) {
            document.exitFullscreen();
          } else if (shell.requestFullscreen) {
            shell.requestFullscreen();
          }
        });
      }

      if (popout) {
        popout.addEventListener('click', function () {
          // The new window opens its own session immediately. Leaving this one
          // running would put two shells in the pod for one person looking at
          // one terminal, and the tab's would sit there unread.
          closeSocket();
          setState('idle', 'Moved to the pop-out window');
        });
      }

      function listen(target, event, handler) {
        target.addEventListener(event, handler);
        disposers.push(function () { target.removeEventListener(event, handler); });
      }

      listen(window, 'resize', refit);
      listen(document, 'fullscreenchange', function () {
        // The element changes size before fullscreen settles, so refit after.
        window.setTimeout(refit, 50);
      });

      // The panel this lives in starts hidden, and a hidden element measures
      // zero - so the terminal cannot be sized, focused, or usefully connected
      // until it is shown. selectTab announces that; nothing else does.
      listen(document, 'dwpk:tabshown', function (event) {
        if (!mount.isConnected || !mount.offsetParent) { return; }
        if (event.detail !== 'terminal') { return; }
        refit();
        term.focus();
        open();
      });

      // Leaving the page ends the session now rather than when the connection
      // eventually times out, and releases the listeners with it.
      listen(window, 'pagehide', function () {
        closeSocket();
        disposers.forEach(function (dispose) { dispose(); });
        term.dispose();
      });

      // The standalone window IS the terminal, so it connects immediately. In a
      // tab it waits to be opened: loading a workspace page used to start a
      // shell in the pod whether or not anybody wanted one.
      if (mount.hasAttribute('data-terminal-autostart')) {
        open();
      } else {
        setState('idle', 'Not connected');
      }
    });
  }

  // currentTheme reads the attribute the server already stamped on <html> so
  // the toggle and the settings radios agree with what is on screen, not with
  // a cookie value that might be stale after a manual edit.
  function currentTheme() {
    var explicit = document.documentElement.getAttribute('data-theme');
    if (explicit === 'light' || explicit === 'dark') {
      return explicit;
    }
    var prefersDark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
    return prefersDark ? 'dark' : 'light';
  }

  // storeTheme persists the choice for the server to read on the next
  // request (themeFromRequest in theme.go). "system" clears the cookie
  // instead of writing a third value, since the server only ever recognises
  // light/dark and treats anything else as "no explicit choice."
  function storeTheme(theme) {
    if (theme === 'light' || theme === 'dark') {
      document.cookie = 'dwpk_ui_theme=' + theme + '; path=/; max-age=31536000; samesite=lax';
    } else {
      document.cookie = 'dwpk_ui_theme=; path=/; max-age=0; samesite=lax';
    }
  }

  // applyTheme updates the live page and every control that shows the current
  // choice, then persists it. "system" removes the attribute so the
  // prefers-color-scheme media query in app.css takes back over.
  function applyTheme(theme) {
    if (theme === 'light' || theme === 'dark') {
      document.documentElement.setAttribute('data-theme', theme);
    } else {
      document.documentElement.removeAttribute('data-theme');
    }
    storeTheme(theme);
    document.querySelectorAll('[data-theme-toggle]').forEach(function (button) {
      button.setAttribute('aria-pressed', currentTheme() === 'dark' ? 'true' : 'false');
    });
    document.querySelectorAll('[data-theme-option]').forEach(function (control) {
      if (control.tagName === 'SELECT') {
        control.value = theme;
      } else {
        control.checked = control.value === theme;
      }
    });
  }

  function wireThemeToggle(scope) {
    var root = scope || document;
    var buttons = root.querySelectorAll('[data-theme-toggle]');
    for (var i = 0; i < buttons.length; i++) {
      if (buttons[i].dataset.themeBound) {
        continue;
      }
      buttons[i].dataset.themeBound = '1';
      buttons[i].addEventListener('click', function () {
        applyTheme(currentTheme() === 'dark' ? 'light' : 'dark');
      });
    }
    root.querySelectorAll('[data-theme-option]').forEach(function (control) {
      if (!once(control, 'themeOptionBound')) {
        return;
      }
      control.addEventListener('change', function () {
        // A group of radios fires change on each one as the selection moves,
        // so only the checked one means anything. A select fires once, already
        // carrying the new value, and has no .checked at all - testing it
        // would read undefined and silently do nothing.
        if (this.type === 'radio' && !this.checked) {
          return;
        }
        applyTheme(this.value);
      });
    });
  }

  // <dialog> brings its own focus trap, ESC handling and backdrop, so opening
  // and closing it is all the JavaScript this needs.
  // A destructive form that will not submit until its subject is typed back.
  //
  // The server checks this too, and that is the check that counts - a POST can
  // be made without ever loading the page. This exists so the person sees the
  // requirement before they act rather than after, and so the button stops
  // being the thing you press to make a dialog go away.
  //
  // The button starts enabled in the markup: with JavaScript off the form still
  // works, and the server still refuses a mismatch.
  function wireConfirmName(scope) {
    (scope || document).querySelectorAll('[data-confirm-name]').forEach(function (field) {
      if (!once(field, 'confirmBound')) {
        return;
      }
      var submit = document.getElementById(field.getAttribute('data-confirm-submit'));
      if (!submit) {
        return;
      }

      function check() {
        var matches = field.value.trim() === field.getAttribute('data-confirm-name');
        submit.disabled = !matches;
        field.setAttribute('aria-invalid', matches ? 'false' : 'true');
      }

      check();
      field.addEventListener('input', check);
      // A dialog reopened after a cancelled attempt keeps whatever was typed,
      // which would leave the button enabled for a deletion nobody re-confirmed.
      var dialog = field.closest('dialog');
      if (dialog) {
        dialog.addEventListener('close', function () {
          field.value = '';
          check();
        });
      }
    });
  }

  function wireDialogs(scope) {
    var openers = scope.querySelectorAll('[data-dialog-open]');
    for (var i = 0; i < openers.length; i++) {
      if (!once(openers[i], 'dialogBound')) { continue; }
      openers[i].addEventListener('click', function () {
        var dialog = document.getElementById(this.getAttribute('data-dialog-open'));
        if (dialog && typeof dialog.showModal === 'function' && !dialog.open) { dialog.showModal(); }
      });
    }
    var closers = scope.querySelectorAll('[data-dialog-close]');
    for (var j = 0; j < closers.length; j++) {
      if (!once(closers[j], 'dialogBound')) { continue; }
      closers[j].addEventListener('click', function () {
        var dialog = document.getElementById(this.getAttribute('data-dialog-close'));
        if (dialog) { dialog.close(); }
      });
    }
  }

  // The command palette: cmd/ctrl-K opens it, typing filters the list, Enter
  // follows the first match. <dialog> supplies ESC, the backdrop and the focus
  // trap, so none of that is reimplemented here.
  function wirePalette(scope) {
    var palette = scope.querySelector ? scope.querySelector('#command-palette') : null;
    if (!palette || palette.dataset.wired === 'true') { return; }
    palette.dataset.wired = 'true';

    var input = palette.querySelector('[data-palette-filter]');
    var items = palette.querySelectorAll('[data-palette-item]');

    function visibleItems() {
      var shown = [];
      for (var i = 0; i < items.length; i++) {
        if (items[i].parentNode.hidden !== true) { shown.push(items[i]); }
      }
      return shown;
    }

    function filter() {
      var query = input.value.trim().toLowerCase();
      for (var i = 0; i < items.length; i++) {
        var matches = items[i].textContent.toLowerCase().indexOf(query) !== -1;
        items[i].parentNode.hidden = !matches;
      }
    }

    document.addEventListener('keydown', function (event) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        if (palette.open) { palette.close(); return; }
        input.value = '';
        filter();
        palette.showModal();
        input.focus();
      }
    });

    input.addEventListener('input', filter);
    input.addEventListener('keydown', function (event) {
      if (event.key !== 'Enter') { return; }
      event.preventDefault();
      var first = visibleItems()[0];
      if (first) { window.location.href = first.getAttribute('href'); }
    });
  }

  // wireEnvFields lets the create-workspace form add or remove KEY/VALUE rows.
  // A row is cloned from the last one in the group rather than built from a
  // string template, so its markup can only ever be what the server rendered.
  function wireEnvFields(scope) {
    (scope || document).querySelectorAll('[data-env-fields]').forEach(function (fields) {
      if (!once(fields, 'envFieldsBound')) { return; }
      var rows = fields.querySelector('[data-env-rows]');
      fields.addEventListener('click', function (event) {
        var add = event.target.closest('[data-env-add]');
        if (add) {
          var last = rows.querySelector('[data-env-row]:last-child');
          if (last) {
            var clone = last.cloneNode(true);
            clone.querySelectorAll('input').forEach(function (input) { input.value = ''; });
            rows.appendChild(clone);
          }
          return;
        }
        var remove = event.target.closest('[data-env-remove]');
        if (remove) {
          var row = remove.closest('[data-env-row]');
          // Always leave one row behind - an empty pair is dropped server-side,
          // so there is no need to protect against submitting it.
          if (row && rows.querySelectorAll('[data-env-row]').length > 1) {
            row.remove();
          }
        }
      });
    });
  }

  function boot(scope) {
    activateTabs(scope);
    wireOnboardingSteps(scope);
    wireCopy(scope);
    connectTerminal(scope);
    wireThemeToggle(scope);
    wireDialogs(scope);
    wireConfirmName(scope);
    wirePalette(scope);
    wireEnvFields(scope);
  }

  // The hash is read once, at load. boot() runs again after every htmx swap,
  // and re-reading there would drag the person back to the fragment's tab
  // every time a status card polled.
  document.addEventListener('DOMContentLoaded', function () {
    boot(document);
    selectTabFromHash();
  });
  document.body.addEventListener('htmx:afterSwap', function (event) { boot(event.target); });
}());
