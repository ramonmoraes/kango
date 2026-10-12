/* Small browser helpers; Go renders every view and HTMX handles navigation/forms. */
(() => {
  let sortables = [], dragging = false, dirty = false, lastModal = false, refreshing = false, toastTimer;
  const $ = (s) => document.querySelector(s);
  function toast(message) {
    const el = $('#toast'); el.textContent = message; el.classList.add('visible');
    clearTimeout(toastTimer); toastTimer = setTimeout(() => el.classList.remove('visible'), 4500);
  }
  async function refresh() {
    if (refreshing) return;
    refreshing = true;
    try { await htmx.ajax('GET', location.pathname + location.search, {target:'#workspace', swap:'outerHTML'}); }
    finally { refreshing = false; }
  }
  async function mutate(op, args) {
    try {
      const response = await fetch('/actions/' + op, {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(args)});
      if (!response.ok) throw new Error(await response.text());
      await refresh();
    } catch (error) { toast(error.message || 'Could not save. Try again.'); await refresh(); }
  }
  function boardOptions() {
    const board = $('#card-board'); if (!board) return;
    const boardID = board.value;
    ['#card-column','#card-status'].forEach(selector => {
      const select = $(selector);
      Array.from(select.options).forEach(option => { option.hidden = option.dataset.board !== boardID; option.disabled = option.hidden; });
      if (select.selectedOptions[0]?.disabled) select.value = Array.from(select.options).find(option => !option.disabled)?.value || '';
    });
    const hiddenBoard = $('#card-form input[name="board_id"]'); if (hiddenBoard) hiddenBoard.value = boardID;
  }
  function checklist() {
    const area = $('#checklist'), preview = $('#checklist-preview'); if (!area || !preview) return;
    preview.replaceChildren();
    area.value.split('\n').forEach((line, index) => {
      line = line.trim(); if (!line) return;
      const label = document.createElement('label'); label.className = 'checklist-item';
      const check = document.createElement('input'); check.type = 'checkbox'; check.checked = /^\[x\]/i.test(line);
      const text = document.createElement('span'); text.textContent = line.replace(/^\[( |x)\]\s*/i, '');
      check.addEventListener('change', () => { const lines = area.value.split('\n'); lines[index] = (check.checked ? '[x] ' : '[ ] ') + text.textContent; area.value = lines.join('\n'); dirty = true; });
      label.append(check, text); preview.append(label);
    });
  }
  function init() {
    sortables.forEach(sortable => sortable.destroy()); sortables = [];
    const app = $('.app'); if (!app) return;
    if (app.dataset.filtered !== 'true') {
      document.querySelectorAll('.card-list').forEach(list => sortables.push(new Sortable(list, {
        group:'cards', animation:160, draggable:'.card', ghostClass:'sortable-ghost', delay:120, delayOnTouchOnly:true,
        onStart:() => { dragging = true; },
        onEnd:event => {
          setTimeout(() => { dragging = false; }, 100);
          if (event.from === event.to && event.oldDraggableIndex === event.newDraggableIndex) return;
          mutate('move_card', {id:Number(event.item.dataset.id), column_id:Number(event.to.dataset.column), position:event.newDraggableIndex});
        }
      })));
    }
    const columns = $('#columns');
    if (columns) sortables.push(new Sortable(columns, {
      animation:160, draggable:'.column', handle:'.column-handle', ghostClass:'sortable-ghost',
      onStart:() => { dragging = true; }, onEnd:event => {
        setTimeout(() => { dragging = false; }, 100);
        if (event.oldDraggableIndex === event.newDraggableIndex) return;
        mutate('reorder_columns', {board_id:Number(app.dataset.board), ids:Array.from(columns.querySelectorAll('.column')).map(el => Number(el.dataset.id))});
      }
    }));
    boardOptions(); checklist();
    const modal = $('.modal-backdrop');
    if (modal && !lastModal) setTimeout(() => modal.querySelector('[autofocus], input, select, button, a')?.focus(), 0);
    lastModal = Boolean(modal); dirty = false;
    const today = new Date(); const localDate = [today.getFullYear(), String(today.getMonth()+1).padStart(2,'0'), String(today.getDate()).padStart(2,'0')].join('-');
    document.querySelectorAll('[data-due]').forEach(el => { if (el.dataset.due < localDate) el.classList.add('overdue'); });
  }
  document.addEventListener('DOMContentLoaded', init);
  document.addEventListener('htmx:afterSwap', event => { if (event.detail.target?.id === 'workspace') init(); });
  document.addEventListener('htmx:historyRestore', init);
  document.addEventListener('htmx:responseError', event => toast(event.detail.xhr.responseText.trim() || 'Could not save. Try again.'));
  document.addEventListener('htmx:sendError', () => toast('The server is unavailable. Your changes have not been saved.'));
  document.addEventListener('input', event => {
    if (event.target.closest('#card-form')) dirty = true;
    if (event.target.id === 'checklist') checklist();
  });
  document.addEventListener('change', event => {
    if (event.target.closest('#card-form')) dirty = true;
    if (event.target.id === 'card-board') boardOptions();
  });
  document.addEventListener('htmx:confirm', event => {
    const el = event.detail.elt;
    const form = el.closest('form');
    const confirmMessage = form?.dataset.confirm;
    const editing = $('#card-form');
    const isPreview = el.dataset.editorTab === 'preview';
    const leavingDirty = dirty && editing && form !== editing && !isPreview;
    if (confirmMessage || leavingDirty) {
      event.preventDefault();
      if (leavingDirty && !window.confirm('You have unsaved card changes. Discard them?')) return;
      if (confirmMessage && !window.confirm(confirmMessage)) return;
      event.detail.issueRequest(true);
    }
  });
  document.addEventListener('click', event => {
    const tab = event.target.closest('[data-editor-tab]');
    if (tab) {
      const preview = tab.dataset.editorTab === 'preview'; $('#description').hidden = preview; $('#markdown-preview').hidden = !preview;
      document.querySelectorAll('[data-editor-tab]').forEach(el => el.classList.toggle('active', el === tab));
    }
    const copy = event.target.closest('[data-copy]');
    if (copy) { event.preventDefault(); navigator.clipboard.writeText(location.origin + copy.dataset.copy).then(() => toast('Card link copied'), () => toast('Could not copy the link.')); }
    const card = event.target.closest('.card');
    if (card && !event.target.closest('a,button,input') && !dragging) card.querySelector('.card-title').click();
    if (event.target.classList.contains('modal-backdrop')) $('.close-modal')?.click();
  });
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && $('.modal-backdrop')) $('.close-modal')?.click();
    if (event.key === 'Enter' && event.target.classList.contains('card')) event.target.querySelector('.card-title').click();
    if (event.key === 'Tab' && $('.modal-backdrop')) {
      const items = Array.from($('.modal').querySelectorAll('a,button,input,select,textarea,[tabindex="0"]')).filter(el => !el.disabled && !el.hidden && el.getClientRects().length);
      const first = items[0], last = items[items.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
    }
  });
  window.addEventListener('beforeunload', event => { if (dirty) { event.preventDefault(); event.returnValue = ''; } });
  setInterval(async () => {
    if (document.hidden || dragging || refreshing || $('.htmx-request')) return;
    try {
      const response = await fetch('/api/revision'); if (!response.ok) return;
      const data = await response.json(), app = $('.app');
      if (app && data.revision !== Number(app.dataset.revision)) {
        if ($('.modal-backdrop')) { if (!app.dataset.stale) { toast('Workspace updated. Close this dialog to see the latest changes.'); app.dataset.stale = 'true'; } }
        else await refresh();
      }
    } catch (_) { /* Keep the current view available while the server is offline. */ }
  }, 2500);
})();
