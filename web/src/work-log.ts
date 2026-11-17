import { Editor } from '@tiptap/core';
import StarterKit from '@tiptap/starter-kit';
import { Markdown } from '@tiptap/markdown';
import DOMPurify from 'dompurify';
import { marked } from 'marked';
import { Bold, Italic, Strikethrough, Link, Unlink, List, ListOrdered, Quote, CodeXml, RemoveFormatting, Redo2 } from 'lucide';

export const workLogIcons = { Bold, Italic, Strikethrough, Link, Unlink, List, ListOrdered, Quote, CodeXml, RemoveFormatting, Redo2 };

function safeLink(href: string) {
  try {
    const url = new URL(href);
    return ((url.protocol === 'https:' || url.protocol === 'http:') && Boolean(url.hostname) && !url.username && !url.password) ||
      (url.protocol === 'mailto:' && Boolean(url.pathname));
  } catch { return false; }
}

export function renderWorkLog(source: string) {
  const renderer = new marked.Renderer();
  renderer.html = ({ text }) => text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
  const fragment = DOMPurify.sanitize(marked.parse(source, { async: false, gfm: true, breaks: true, renderer }), {
    ALLOWED_TAGS: ['p', 'br', 'strong', 'em', 's', 'del', 'ul', 'ol', 'li', 'blockquote', 'code', 'pre', 'h1', 'h2', 'h3', 'a', 'hr'],
    ALLOWED_ATTR: ['href', 'title', 'start'], ALLOW_DATA_ATTR: false, ALLOW_ARIA_ATTR: false, RETURN_DOM_FRAGMENT: true,
  });
  fragment.querySelectorAll('a').forEach(anchor => {
    if (safeLink(anchor.getAttribute('href') || '')) {
      anchor.target = '_blank';
      anchor.rel = 'noopener noreferrer nofollow';
    } else anchor.removeAttribute('href');
  });
  const container = document.createElement('div');
  container.append(fragment);
  return container.innerHTML;
}

export function createWorkLogEditor(root: HTMLElement, content: string, onChange: (source: string) => void) {
  const tools = [
    ['bold', 'Bold', 'bold'], ['italic', 'Italic', 'italic'], ['strike', 'Strikethrough', 'strikethrough'],
    ['link', 'Insert or edit link', 'link'], ['unlink', 'Remove link', 'unlink'],
    ['bulletList', 'Bullet list', 'list'], ['orderedList', 'Numbered list', 'list-ordered'],
    ['blockquote', 'Block quote', 'quote'], ['code', 'Inline code', 'code'],
    ['codeBlock', 'Code block', 'code-xml'], ['clear', 'Clear formatting', 'remove-formatting'],
    ['undo', 'Undo', 'undo-2'], ['redo', 'Redo', 'redo-2'],
  ];
  root.innerHTML = `<div class="work-log-toolbar" role="toolbar" aria-label="Work log formatting">
    <select aria-label="Text style"><option value="paragraph">Normal text</option><option value="1">Heading 1</option><option value="2">Heading 2</option><option value="3">Heading 3</option></select>
    ${tools.map(([command, label, icon]) => `<button type="button" data-command="${command}" aria-label="${label}" title="${label}"><i data-lucide="${icon}"></i></button>`).join('')}
    </div><div class="work-log-link-panel" hidden>
    <label for="work-log-link-url">Link URL</label><div><input id="work-log-link-url" type="url" autocomplete="off" spellcheck="false"><button type="button" data-apply-link title="Apply link" aria-label="Apply link"><i data-lucide="check"></i></button><button type="button" data-cancel-link title="Cancel link" aria-label="Cancel link"><i data-lucide="x"></i></button></div>
    <p role="alert" hidden>Enter a valid http, https, or mailto URL.</p></div><div class="work-log-surface"></div>`;
  const toolbar = root.querySelector<HTMLElement>('.work-log-toolbar')!;
  const style = toolbar.querySelector('select')!;
  const linkPanel = root.querySelector<HTMLElement>('.work-log-link-panel')!;
  const linkInput = linkPanel.querySelector('input')!;
  const linkError = linkPanel.querySelector('p')!;
  const buttons = [...toolbar.querySelectorAll<HTMLButtonElement>('[data-command]')];
  const editor = new Editor({
    element: root.querySelector<HTMLElement>('.work-log-surface')!,
    extensions: [StarterKit.configure({ heading: { levels: [1, 2, 3] }, link: {
      openOnClick: false, isAllowedUri: safeLink, HTMLAttributes: { target: '_blank', rel: 'noopener noreferrer nofollow' },
    } }), Markdown],
    content, contentType: 'markdown',
    editorProps: { attributes: { id: 'work-log-editor', class: 'rich-text', role: 'textbox', 'aria-labelledby': 'work-log-label', 'aria-multiline': 'true', 'aria-required': 'true', spellcheck: 'true' } },
    onUpdate: () => { updateToolbar(); onChange(editor.getMarkdown()); },
    onSelectionUpdate: () => updateToolbar(),
    onTransaction: () => updateToolbar(),
  });
  function updateToolbar() {
    if (editor.isDestroyed) return;
    const heading = [1, 2, 3].find(level => editor.isActive('heading', { level }));
    style.value = heading ? String(heading) : 'paragraph';
    style.disabled = !editor.isEditable;
    buttons.forEach(button => {
      const command = button.dataset.command!;
      const toggle = ['bold', 'italic', 'strike', 'link', 'bulletList', 'orderedList', 'blockquote', 'code', 'codeBlock'].includes(command);
      if (toggle) button.setAttribute('aria-pressed', String(editor.isActive(command)));
      button.disabled = !editor.isEditable || (command === 'undo' && !editor.can().undo()) ||
        (command === 'redo' && !editor.can().redo()) || (command === 'unlink' && !editor.isActive('link'));
    });
  }
  function closeLink() { linkPanel.hidden = true; editor.commands.focus(); }
  function openLink() {
    linkPanel.hidden = false;
    linkError.hidden = true;
    linkInput.value = String(editor.getAttributes('link').href || '');
    linkInput.focus();
  }
  function applyLink() {
    const href = linkInput.value.trim();
    if (!safeLink(href)) { linkError.hidden = false; linkInput.focus(); return; }
    if (editor.state.selection.empty && !editor.isActive('link')) {
      editor.chain().focus().insertContent({ type: 'text', text: href, marks: [{ type: 'link', attrs: { href } }] }).run();
    } else editor.chain().focus().extendMarkRange('link').setLink({ href }).run();
    closeLink();
  }
  const commands: Record<string, () => void> = {
    bold: () => editor.chain().focus().toggleBold().run(), italic: () => editor.chain().focus().toggleItalic().run(),
    strike: () => editor.chain().focus().toggleStrike().run(), link: openLink,
    unlink: () => editor.chain().focus().extendMarkRange('link').unsetLink().run(),
    bulletList: () => editor.chain().focus().toggleBulletList().run(), orderedList: () => editor.chain().focus().toggleOrderedList().run(),
    blockquote: () => editor.chain().focus().toggleBlockquote().run(), code: () => editor.chain().focus().toggleCode().run(),
    codeBlock: () => editor.chain().focus().toggleCodeBlock().run(), clear: () => editor.chain().focus().unsetAllMarks().clearNodes().run(),
    undo: () => editor.chain().focus().undo().run(), redo: () => editor.chain().focus().redo().run(),
  };
  buttons.forEach(button => {
    button.addEventListener('mousedown', event => event.preventDefault());
    button.addEventListener('click', () => { if (editor.isEditable) commands[button.dataset.command!]?.(); });
  });
  style.addEventListener('change', () => {
    if (style.value === 'paragraph') editor.chain().focus().setParagraph().run();
    else editor.chain().focus().setHeading({ level: Number(style.value) as 1 | 2 | 3 }).run();
  });
  linkPanel.querySelector('[data-apply-link]')!.addEventListener('click', applyLink);
  linkPanel.querySelector('[data-cancel-link]')!.addEventListener('click', closeLink);
  linkInput.addEventListener('keydown', event => {
    if (event.key === 'Enter') { event.preventDefault(); applyLink(); }
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); closeLink(); }
  });
  editor.view.dom.addEventListener('keydown', event => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k' && editor.isEditable) {
      event.preventDefault(); openLink();
    }
  });
  updateToolbar();
  return {
    isEmpty: () => editor.isEmpty,
    setEditable(editable: boolean) { editor.setEditable(editable); if (!editable) linkPanel.hidden = true; updateToolbar(); },
    destroy() { editor.destroy(); },
  };
}
