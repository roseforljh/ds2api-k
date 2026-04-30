const test = require('node:test');
const assert = require('node:assert/strict');
const { mkdtemp, readFile, rm } = require('node:fs/promises');
const { tmpdir } = require('node:os');
const { join, resolve } = require('node:path');
const { pathToFileURL } = require('node:url');

async function loadChatHistoryModule() {
  const { rolldown } = await import('../../webui/node_modules/rolldown/dist/index.mjs');
  const repoRoot = resolve(__dirname, '..', '..');
  const sourcePath = join(repoRoot, 'webui', 'src', 'features', 'chatHistory', 'ChatHistoryContainer.jsx');
  const tempDir = await mkdtemp(join(tmpdir(), 'chat-history-ui-'));
  const outputPath = join(tempDir, 'ChatHistoryContainer.mjs');

  const bundle = await rolldown({
    input: sourcePath,
    platform: 'node',
    plugins: [{
      name: 'export-detail-conversation-for-test',
      async load(id) {
        if (id === sourcePath) {
          const source = await readFile(sourcePath, 'utf8');
          return `${source}\nexport { DetailConversation };\n`;
        }
      },
    }],
  });
  await bundle.write({
    file: outputPath,
    format: 'esm',
  });

  const mod = await import(pathToFileURL(outputPath).href);
  return { mod, cleanup: () => rm(tempDir, { recursive: true, force: true }) };
}

test('chat history detail renders assistant reasoning before final content', async () => {
  const { renderToStaticMarkup } = require('../../webui/node_modules/react-dom/server');
  const React = require('../../webui/node_modules/react');
  const { mod, cleanup } = await loadChatHistoryModule();

  try {
    const html = renderToStaticMarkup(React.createElement(mod.DetailConversation, {
      selectedItem: {
        id: 'history-1',
        status: 'success',
        reasoning_content: '先分析用户问题',
        content: '最终答案',
      },
      t: key => ({
        'chatHistory.role.assistant': 'Assistant',
        'chatHistory.reasoningTrace': '思维链过程',
        'chatHistory.emptyAssistantOutput': 'No output',
        'chatHistory.emptyUserInput': 'No input',
        'chatHistory.backToBottom': 'Back to bottom',
      }[key] || key),
      detailScrollRef: { current: null },
      assistantStartRef: { current: null },
      bottomButtonClassName: '',
      onMessage: () => {},
    }));

    assert.match(html, /思维链过程/);
    assert.match(html, /先分析用户问题/);
    assert.match(html, /最终答案/);
    assert.ok(html.indexOf('先分析用户问题') < html.indexOf('最终答案'));
  } finally {
    await cleanup();
  }
});
